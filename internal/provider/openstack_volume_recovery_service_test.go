package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/google/uuid"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const (
	recoveryTestProject = "0f3c1e2a4b5d6e7f8091a2b3c4d5e6f7"
	recoveryTestVolume  = "5c9a1f0e-2b3d-4e5f-8a6b-7c8d9e0f1a2b"
	recoveryAddr        = "cleura_openstack_volume_recovery_service.test"

	recoveryActive   = api.OpenStackBlockStorageVolumeDisasterRecoveryServiceStatusActive
	recoveryStopping = api.OpenStackBlockStorageVolumeDisasterRecoveryServiceStatusStopping
)

type mockRecoveryVolume struct {
	service   *api.OpenStackBlockStorageDisasterRecoveryService
	stopPolls int // reads left before a "stopping" record disappears
}

// mockVolumeAPI serves volumes the way the live API handles the Recovery
// service (verified on 2026-10-06): a PATCH with an object switches it on or
// changes it in place, a PATCH with null makes it "stopping", and the record
// disappears after a few reads.
type mockVolumeAPI struct {
	mu        sync.Mutex
	volumes   map[string]*mockRecoveryVolume
	stopAfter int                          // reads a "stopping" record survives
	patches   []map[string]json.RawMessage // every PATCH body, in order
	targets   []string                     // "<region>/<project>" of every request
	nextID    int
}

func newMockVolumeAPI(stopAfter int) *mockVolumeAPI {
	return &mockVolumeAPI{
		volumes:   map[string]*mockRecoveryVolume{recoveryTestVolume: {}},
		stopAfter: stopAfter,
		nextID:    100,
	}
}

func (m *mockVolumeAPI) handler() http.Handler {
	const prefix = "/openstack/block-storage/v2/volumes/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
		if !strings.HasPrefix(r.URL.Path, prefix) || len(parts) != 3 {
			apiError(w, http.StatusNotFound, "no route for "+r.URL.Path)
			return
		}
		m.targets = append(m.targets, parts[0]+"/"+parts[1])
		vol, ok := m.volumes[parts[2]]
		if !ok {
			apiError(w, http.StatusNotFound, "Volume not found")
			return
		}

		switch r.Method {
		case http.MethodGet:
			if s := vol.service; s != nil && s.Status == recoveryStopping {
				if vol.stopPolls <= 0 {
					vol.service = nil
				} else {
					vol.stopPolls--
				}
			}
		case http.MethodPatch:
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				apiError(w, http.StatusBadRequest, err.Error())
				return
			}
			m.patches = append(m.patches, body)
			raw, present := body["recover_service"]
			switch {
			case !present:
			case string(raw) == "null":
				if vol.service != nil && vol.service.Status == recoveryActive {
					vol.service.Status = recoveryStopping
					vol.stopPolls = m.stopAfter
				}
			default:
				var req api.CommonRecoverServiceRequest
				if err := json.Unmarshal(raw, &req); err != nil {
					apiError(w, http.StatusBadRequest, err.Error())
					return
				}
				if req.RetentionDays != 10 && req.RetentionDays != 30 {
					apiError(w, http.StatusConflict, "Invalid values for RetentionDays. Must be one of [10, 30]")
					return
				}
				if vol.service == nil || vol.service.Status != recoveryActive {
					m.nextID++
					vol.service = &api.OpenStackBlockStorageDisasterRecoveryService{
						Id:      m.nextID,
						Status:  recoveryActive,
						Started: time.Date(2026, 10, 6, 13, 0, m.nextID%60, 0, time.UTC),
					}
				}
				vol.service.RetentionDays, vol.service.Immutable = req.RetentionDays, req.Immutable
			}
		default:
			apiError(w, http.StatusMethodNotAllowed, r.Method+" is not served by the mock")
			return
		}
		writeJSON(w, http.StatusOK, mockVolumeBody(parts[2], vol))
	})
}

func mockVolumeBody(id string, vol *mockRecoveryVolume) api.OpenStackBlockStorageVolume {
	name := "data"
	v := api.OpenStackBlockStorageVolume{
		Id:          uuid.MustParse(id),
		Name:        &name,
		Size:        1,
		Status:      api.CommonOpenStackVolumeStatus("available"),
		VolumeType:  "cbs",
		Bootable:    "false",
		CreatedAt:   time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC),
		Metadata:    map[string]interface{}{},
		Attachments: []api.OpenStackBlockStorageVolumeAttachment{},
	}
	if vol.service != nil {
		s := *vol.service
		v.DisasterRecoverService = &s
	}
	return v
}

func (m *mockVolumeAPI) service() *api.OpenStackBlockStorageDisasterRecoveryService {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.volumes[recoveryTestVolume].service; s != nil {
		c := *s
		return &c
	}
	return nil
}

func (m *mockVolumeAPI) setService(s *api.OpenStackBlockStorageDisasterRecoveryService, stopPolls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volumes[recoveryTestVolume].service = s
	m.volumes[recoveryTestVolume].stopPolls = stopPolls
}

func (m *mockVolumeAPI) deleteVolume() {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.volumes, recoveryTestVolume)
}

func (m *mockVolumeAPI) patchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.patches)
}

func (m *mockVolumeAPI) lastPatch() map[string]json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.patches) == 0 {
		return nil
	}
	return m.patches[len(m.patches)-1]
}

func (m *mockVolumeAPI) lastTarget() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.targets) == 0 {
		return ""
	}
	return m.targets[len(m.targets)-1]
}

func serveVolumeAPI(t *testing.T, m *mockVolumeAPI) {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	t.Setenv("CLEURA_API_URL", srv.URL)
	t.Setenv("CLEURA_API_USERNAME", "user")
	t.Setenv("CLEURA_API_TOKEN", "token")
}

// withFastRecoveryPolling makes Delete's wait poll every millisecond and give
// up after timeout.
func withFastRecoveryPolling(t *testing.T, timeout time.Duration) {
	t.Helper()
	interval, stop := recoveryServicePollInterval, recoveryServiceStopTimeout
	recoveryServicePollInterval, recoveryServiceStopTimeout = time.Millisecond, timeout
	t.Cleanup(func() { recoveryServicePollInterval, recoveryServiceStopTimeout = interval, stop })
}

func recoveryConfig(volumeID, attrs string) string {
	return fmt.Sprintf(`
provider "cleura" {
  cloud      = "public"
  region     = "Kna1"
  project_id = %q
  use_cli    = false
}

resource "cleura_openstack_volume_recovery_service" "test" {
  volume_id = %q
%s
}
`, recoveryTestProject, volumeID, attrs)
}

// onlyRecoverService checks that a PATCH body carries recover_service and
// nothing else: the volume's other fields belong to whatever manages the
// volume.
func onlyRecoverService(body map[string]json.RawMessage, want string) error {
	if len(body) != 1 {
		keys := make([]string, 0, len(body))
		for k := range body {
			keys = append(keys, k)
		}
		return fmt.Errorf("PATCH body has keys %v, want only recover_service", keys)
	}
	var got, wanted any
	if err := json.Unmarshal(body["recover_service"], &got); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(want), &wanted); err != nil {
		return err
	}
	if !reflect.DeepEqual(got, wanted) {
		return fmt.Errorf("PATCH sent recover_service=%s, want %s", body["recover_service"], want)
	}
	return nil
}

func TestVolumeRecoveryServiceLifecycle(t *testing.T) {
	withFastRecoveryPolling(t, 5*time.Second)
	mock := newMockVolumeAPI(3)
	serveVolumeAPI(t, mock)
	var firstRecord int

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			// Destroy switched the service off and waited until its record was gone.
			if err := onlyRecoverService(mock.lastPatch(), "null"); err != nil {
				return err
			}
			if s := mock.service(); s != nil {
				return fmt.Errorf("destroy returned while the Recovery service was still %s", s.Status)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: recoveryConfig(recoveryTestVolume, "  retention_days = 10"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("id"), knownvalue.StringExact(recoveryTestVolume)),
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("region"), knownvalue.StringExact("Kna1")),
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("project_id"), knownvalue.StringExact(recoveryTestProject)),
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("retention_days"), knownvalue.Int64Exact(10)),
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("immutable"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("started_at"), knownvalue.NotNull()),
				},
				Check: func(*terraform.State) error {
					firstRecord = mock.service().Id
					return onlyRecoverService(mock.lastPatch(), `{"retention_days":10,"immutable":false}`)
				},
			},
			{
				Config:           recoveryConfig(recoveryTestVolume, "  retention_days = 10"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}},
			},
			{
				// Changed in place: the same record, so its recovery points are kept.
				Config: recoveryConfig(recoveryTestVolume, "  retention_days = 30"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(recoveryAddr, plancheck.ResourceActionUpdate),
				}},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("retention_days"), knownvalue.Int64Exact(30)),
				},
				Check: func(*terraform.State) error {
					if got := mock.service().Id; got != firstRecord {
						return fmt.Errorf("record %d was replaced by %d; want an in-place change", firstRecord, got)
					}
					return onlyRecoverService(mock.lastPatch(), `{"retention_days":30,"immutable":false}`)
				},
			},
			{
				ResourceName:      recoveryAddr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     recoveryTestVolume,
			},
			{
				ResourceName:      recoveryAddr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateId:     "Kna1/" + recoveryTestProject + "/" + recoveryTestVolume,
			},
		},
	})
}

func TestVolumeRecoveryServiceAdoptsAndTargets(t *testing.T) {
	withFastRecoveryPolling(t, 5*time.Second)
	mock := newMockVolumeAPI(0)
	// Switched on before Terraform managed it, e.g. by the customer's own tooling.
	mock.setService(&api.OpenStackBlockStorageDisasterRecoveryService{
		Id: 7, Status: recoveryActive, RetentionDays: 30,
		Started: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
	}, 0)
	serveVolumeAPI(t, mock)
	const otherProject = "0123456789abcdef0123456789abcdef"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: recoveryConfig(recoveryTestVolume, "  retention_days = 10\n  region = \"Fra1\"\n  project_id = \""+otherProject+"\""),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("region"), knownvalue.StringExact("Fra1")),
				statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("project_id"), knownvalue.StringExact(otherProject)),
				statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("started_at"), knownvalue.StringExact("2026-09-01T08:00:00Z")),
			},
			Check: func(*terraform.State) error {
				if s := mock.service(); s.Id != 7 || s.RetentionDays != 10 {
					return fmt.Errorf("got record %d with %d days; want record 7 adopted and changed to 10 days", s.Id, s.RetentionDays)
				}
				if got, want := mock.lastTarget(), "Fra1/"+otherProject; got != want {
					return fmt.Errorf("last request went to %s, want %s", got, want)
				}
				return nil
			},
		}},
	})
}

func TestVolumeRecoveryServiceDrift(t *testing.T) {
	withFastRecoveryPolling(t, 5*time.Second)
	mock := newMockVolumeAPI(0)
	serveVolumeAPI(t, mock)
	config := recoveryConfig(recoveryTestVolume, "  retention_days = 10")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// Switched off outside Terraform: planned to be switched on again.
				PreConfig:          func() { mock.setService(nil, 0) },
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// A record that is still stopping counts as off too.
				PreConfig: func() {
					mock.setService(&api.OpenStackBlockStorageDisasterRecoveryService{
						Id: 9, Status: recoveryStopping, RetentionDays: 10,
					}, 1000)
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(recoveryAddr, tfjsonpath.New("retention_days"), knownvalue.Int64Exact(10)),
				},
			},
			{
				// The volume was deleted outside Terraform.
				PreConfig:   mock.deleteVolume,
				Config:      config,
				ExpectError: regexp.MustCompile(`(?s)Volume\s+` + recoveryTestVolume + `\s+was\s+not\s+found\s+in\s+project`),
			},
		},
	})
}

func TestVolumeRecoveryServicePlanValidation(t *testing.T) {
	serveVolumeAPI(t, newMockVolumeAPI(0))
	for _, tc := range []struct {
		name, volumeID, attrs, want string
	}{
		{"retention other than 10 or 30", recoveryTestVolume, "  retention_days = 7", `(?s)value\s+must\s+be\s+one\s+of`},
		{"volume ID that is not a UUID", "vol-1", "  retention_days = 10", `(?s)must\s+be\s+a\s+volume\s+ID`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      recoveryConfig(tc.volumeID, tc.attrs),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.want),
				}},
			})
		})
	}
}

// deleteWithState calls Delete directly, so the test controls what the API
// reports at that moment rather than what a refresh just before it saw.
func deleteWithState(t *testing.T, mock *mockVolumeAPI, immutable bool) fwresource.DeleteResponse {
	t.Helper()
	cfg := newTestConfig(t, mock.handler())
	cfg.Region, cfg.ProjectID = "Kna1", recoveryTestProject
	r := &volumeRecoveryServiceResource{config: cfg}
	ctx := context.Background()

	var schemaResp fwresource.SchemaResponse
	r.Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)
	state := tfsdk.State{Schema: schemaResp.Schema}
	if diags := state.Set(ctx, &volumeRecoveryServiceModel{
		ID:            types.StringValue(recoveryTestVolume),
		VolumeID:      types.StringValue(recoveryTestVolume),
		Region:        types.StringValue("Kna1"),
		ProjectID:     types.StringValue(recoveryTestProject),
		RetentionDays: types.Int64Value(10),
		Immutable:     types.BoolValue(immutable),
		StartedAt:     types.StringValue("2026-10-06T13:55:07Z"),
	}); diags.HasError() {
		t.Fatal(diags)
	}
	resp := fwresource.DeleteResponse{State: state}
	r.Delete(ctx, fwresource.DeleteRequest{State: state}, &resp)
	return resp
}

func TestVolumeRecoveryServiceDeleteWhileStopping(t *testing.T) {
	withFastRecoveryPolling(t, 5*time.Second)
	mock := newMockVolumeAPI(0)
	// Switched off outside Terraform and still stopping when destroy runs.
	mock.setService(&api.OpenStackBlockStorageDisasterRecoveryService{Id: 9, Status: recoveryStopping, RetentionDays: 10}, 3)

	resp := deleteWithState(t, mock, false)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if n := mock.patchCount(); n != 0 {
		t.Errorf("sent %d PATCH requests for a service that was already stopping, want none", n)
	}
	if s := mock.service(); s != nil {
		t.Errorf("Delete returned while the Recovery service was still %s", s.Status)
	}
}

func TestVolumeRecoveryServiceDeleteVolumeGone(t *testing.T) {
	mock := newMockVolumeAPI(0)
	mock.deleteVolume()

	resp := deleteWithState(t, mock, false)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if n := mock.patchCount(); n != 0 {
		t.Errorf("sent %d PATCH requests for a volume that is gone, want none", n)
	}
}

func TestVolumeRecoveryServiceStopTimeout(t *testing.T) {
	withFastRecoveryPolling(t, 30*time.Millisecond)
	mock := newMockVolumeAPI(1 << 30) // never finishes stopping
	mock.setService(&api.OpenStackBlockStorageDisasterRecoveryService{Id: 9, Status: recoveryActive, RetentionDays: 10}, 0)

	resp := deleteWithState(t, mock, false)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Delete succeeded although the Recovery service never stopped")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	for _, want := range []string{"still stopping after", "Run the command again"} {
		if !strings.Contains(detail, want) {
			t.Errorf("error %q does not mention %q", detail, want)
		}
	}
}

func TestParseVolumeRecoveryServiceImportID(t *testing.T) {
	for _, tc := range []struct {
		name, id, region, project, volume string
		wantErr                           bool
	}{
		{name: "volume only", id: recoveryTestVolume, volume: recoveryTestVolume},
		{name: "project and volume", id: "p1/" + recoveryTestVolume, project: "p1", volume: recoveryTestVolume},
		{name: "region, project and volume", id: "Kna1/p1/" + recoveryTestVolume, region: "Kna1", project: "p1", volume: recoveryTestVolume},
		{name: "surrounding space", id: " Kna1 / p1 / " + recoveryTestVolume + " ", region: "Kna1", project: "p1", volume: recoveryTestVolume},
		{name: "not a volume ID", id: "p1/vol-1", wantErr: true},
		{name: "empty segment", id: "/" + recoveryTestVolume, wantErr: true},
		{name: "too many segments", id: "a/b/c/" + recoveryTestVolume, wantErr: true},
		{name: "empty", id: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			region, project, volume, err := parseVolumeRecoveryServiceImportID(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.id)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if region != tc.region || project != tc.project || volume != tc.volume {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", region, project, volume, tc.region, tc.project, tc.volume)
			}
		})
	}
}
