package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const identityTestProject = "5bc1a0e0a2fa4a6b9f4b8f0f2ba7b1cd"

func TestParseShootImportID(t *testing.T) {
	for _, tc := range []struct {
		name, id, wantProject, wantShoot string
		wantErr                          bool
	}{
		// The pre-existing format, which must keep working.
		{name: "bare name leaves the project to the provider", id: "my-shoot", wantShoot: "my-shoot"},
		{name: "project and name", id: "proj-1234/my-shoot", wantProject: "proj-1234", wantShoot: "my-shoot"},
		{name: "surrounding space is trimmed", id: "  proj-1234 / my-shoot ", wantProject: "proj-1234", wantShoot: "my-shoot"},
		{name: "empty", id: "", wantErr: true},
		{name: "empty shoot name", id: "proj-1234/", wantErr: true},
		{name: "empty project", id: "/my-shoot", wantErr: true},
		{name: "too many segments", id: "a/b/c", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project, shoot, err := parseShootImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q, got project=%q shoot=%q", tc.id, project, shoot)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if project != tc.wantProject || shoot != tc.wantShoot {
				t.Errorf("parseShootImportID(%q) = (%q, %q), want (%q, %q)",
					tc.id, project, shoot, tc.wantProject, tc.wantShoot)
			}
		})
	}
}

// mockShootAPI serves shoots through create, get and delete, plus admin
// kubeconfigs for them. A deleted shoot answers 404, as it does live.
type mockShootAPI struct {
	mu     sync.Mutex
	shoots map[string]bool // names of the shoots that exist
	// failReconcile makes the shoot's last operation fail, so Create stops
	// after it has persisted the shoot but before the refresh that follows.
	failReconcile bool
}

func testShoot(name string) api.GardenerShootShoot {
	return api.GardenerShootShoot{
		Name:             name,
		CloudProfileName: "cleuracloud",
		Kubernetes:       api.GardenerShootKubernetes{Version: "1.35.6"},
		Maintenance: api.GardenerShootMaintenance{
			AutoUpdate: api.GardenerShootAutoUpdate{KubernetesVersion: true, MachineImageVersion: true},
			TimeWindow: api.GardenerShootTimeWindow{Begin: "020000+0000", End: "060000+0000"},
		},
		Networking: api.GardenerShootNetworking{Nodes: "10.250.0.0/16", Type: "calico"},
		ShootProvider: api.GardenerShootProvider{
			ControlPlaneConfig: api.GardenerShootControlPlaneConfig{LoadBalancerProvider: "amphora"},
			InfrastructureConfig: api.GardenerShootInfrastructureConfig{
				FloatingPoolName: "ext-net",
				Networks:         api.GardenerShootNetworks{Workers: "10.250.0.0/16"},
			},
			Workers: []api.GardenerShootWorker{{
				Name: "wg1",
				Machine: api.GardenerShootMachine{
					Image: api.GardenerShootMachineImage{Name: "gardenlinux", Version: "1592.1.0"},
					Type:  "b.2c4gb",
				},
				MaxSurge: 1,
				Maximum:  1,
				Minimum:  1,
				Volume:   api.GardenerShootVolume{Size: "50Gi"},
				Zones:    []string{"nova"},
			}},
		},
		LastOperation: &api.GardenerShootLastOperation{
			Type: "Create", State: api.GardenerShootLastOperationStateSucceeded, Progress: 100,
		},
	}
}

func (m *mockShootAPI) handler() http.Handler {
	const shoots = "/gardener/v2/public/shoots/Sto2/{project}"
	mux := http.NewServeMux()
	mux.HandleFunc("POST /gardener/v2/public/Sto2/{project}/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST "+shoots, func(w http.ResponseWriter, r *http.Request) {
		var body api.GardenerCreateShootShoot
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			apiError(w, 400, err.Error())
			return
		}
		m.mu.Lock()
		if m.shoots == nil {
			m.shoots = map[string]bool{}
		}
		m.shoots[body.Name] = true
		m.mu.Unlock()
		writeJSON(w, 201, testShoot(body.Name))
	})
	mux.HandleFunc("GET "+shoots+"/{shoot}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.shoots[r.PathValue("shoot")] {
			apiError(w, 404, "Not Found")
			return
		}
		shoot := testShoot(r.PathValue("shoot"))
		if m.failReconcile {
			shoot.LastOperation.State = api.GardenerShootLastOperationStateFailed
			shoot.LastOperation.Progress = 40
		}
		writeJSON(w, 200, shoot)
	})
	mux.HandleFunc("DELETE "+shoots+"/{shoot}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		delete(m.shoots, r.PathValue("shoot"))
		m.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /gardener/v3/public/shoots/Sto2/{project}/{shoot}/admin-kubeconfig", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, api.GardenerAdminKubeConfig{
			Kubeconfig: "apiVersion: v1\nkind: Config\n",
			ExpiresAt:  time.Now().Add(time.Hour),
		})
	})
	return mux
}

// TestShootIdentityLifecycle proves the id survives the framework's own
// checks: known after create, stable across the next plan, accepted back as
// the import ID (as is the bare name), and unknown again when a rename
// replaces the cluster.
func TestShootIdentityLifecycle(t *testing.T) {
	serveShootAPI(t, &mockShootAPI{})

	const addr = "cleura_gardener_shoot.test"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: identityTestConfig("idtest"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", identityTestProject+"/idtest"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot_kubeconfig.test", "id", identityTestProject+"/idtest"),
				),
			},
			{
				Config: identityTestConfig("idtest"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The id is what the docs tell users to reference the cluster by.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateId:     "idtest",
				ImportStateVerify: true,
			},
			{
				// Renaming replaces the cluster (and its kubeconfig), so the old
				// id must not be carried into the plan.
				Config: identityTestConfig("idtest2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("id")),
						plancheck.ExpectUnknownValue("cleura_gardener_shoot_kubeconfig.test", tfjsonpath.New("id")),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "id", identityTestProject+"/idtest2"),
					resource.TestCheckResourceAttr("cleura_gardener_shoot_kubeconfig.test", "id", identityTestProject+"/idtest2"),
				),
			},
		},
	})
}

// TestKubeconfigIDFilledForOlderState: a kubeconfig's Read never calls the API,
// so a kubeconfig created before id existed gets its id from Read alone.
func TestKubeconfigIDFilledForOlderState(t *testing.T) {
	ctx := context.Background()
	r := &shootKubeconfigResource{config: &ProviderConfig{ProjectID: identityTestProject}}
	var schemaResp fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &shootKubeconfigResourceModel{ShootName: types.StringValue("prod")}); diags.HasError() {
		t.Fatal(diags)
	}
	resp := fwresource.ReadResponse{State: state}
	r.Read(ctx, fwresource.ReadRequest{State: state}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}

	var got shootKubeconfigResourceModel
	resp.State.Get(ctx, &got)
	if want := identityTestProject + "/prod"; got.ID.ValueString() != want {
		t.Errorf("id = %q, want %q", got.ID.ValueString(), want)
	}
}

// TestShootImportFromOtherProject: an id naming another project imports the
// cluster from there and records that project, so later reads, updates and
// deletes stay pointed at it.
func TestShootImportFromOtherProject(t *testing.T) {
	const other = "0000aaaa0000aaaa0000aaaa0000aaaa"
	serveShootAPI(t, &mockShootAPI{shoots: map[string]bool{"idtest": true}})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:        identityTestConfig("idtest"),
			ResourceName:  "cleura_gardener_shoot.test",
			ImportState:   true,
			ImportStateId: other + "/idtest",
			ImportStateCheck: func(states []*terraform.InstanceState) error {
				if len(states) != 1 {
					return fmt.Errorf("imported %d instances, want 1", len(states))
				}
				attrs := states[0].Attributes
				if attrs["project_id"] != other || attrs["id"] != other+"/idtest" {
					return fmt.Errorf("project_id = %q, id = %q; want %q, %q",
						attrs["project_id"], attrs["id"], other, other+"/idtest")
				}
				return nil
			},
		}},
	})
}

// TestShootIdentityAfterFailedReconcile covers the path where Create stops
// after the state it persists before waiting: the reconcile failure must be
// what the user sees, not a complaint about the id.
func TestShootIdentityAfterFailedReconcile(t *testing.T) {
	serveShootAPI(t, &mockShootAPI{failReconcile: true})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      identityTestConfig("idtest"),
			ExpectError: regexp.MustCompile(`Shoot reconciliation failed`),
		}},
	})
}

func serveShootAPI(t *testing.T, m *mockShootAPI) {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", "user")
	t.Setenv("CLEURA_API_TOKEN", "token")
}

func identityTestConfig(name string) string {
	return strings.NewReplacer("PROJECT", identityTestProject, "NAME", name).Replace(`
provider "cleura" {
  cloud      = "public"
  region     = "Sto2"
  project_id = "PROJECT"
  use_cli    = false
}

resource "cleura_gardener_shoot" "test" {
  name               = "NAME"
  kubernetes_version = "1.35.6"

  shoot_provider = {
    infrastructure_config = {
      floating_pool_name = "ext-net"
    }
    load_balancer_provider = "amphora"
    workers = [{
      name = "wg1"
      machine = {
        image_name    = "gardenlinux"
        image_version = "1592.1.0"
        type          = "b.2c4gb"
      }
      volume_size = "50Gi"
    }]
  }
}

resource "cleura_gardener_shoot_kubeconfig" "test" {
  shoot_name         = cleura_gardener_shoot.test.name
  expiration_seconds = 3600
}`)
}
