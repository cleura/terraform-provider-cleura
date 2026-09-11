package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	api "github.com/cleura/cleura-client-go/api"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func strp(s string) *string { return &s }

func domain(id, name, areaName string, regionTags ...string) api.CommonOpenStackDomain {
	var regions []api.CommonOpenStackRegion
	for i, tag := range regionTags {
		regions = append(regions, api.CommonOpenStackRegion{Id: i + 1, Name: tag, Tag: tag})
	}
	return api.CommonOpenStackDomain{Id: id, Name: strp(name), Status: "active", Area: api.CommonOpenStackDomainArea{Id: 1, Name: areaName, Tag: strings.ToLower(areaName), Regions: regions}}
}

func TestDomainForRegion(t *testing.T) {
	sto := domain("dom-sto", "CCP_Domain_1", "Stockholm", "Sto2", "Sto1")
	fra := domain("dom-fra", "CCP_Domain_2", "Frankfurt", "Fra1")

	tests := []struct {
		name    string
		domains []api.CommonOpenStackDomain
		region  string
		want    string
		wantErr string
	}{
		{"region identifies the domain", []api.CommonOpenStackDomain{sto, fra}, "Fra1", "dom-fra", ""},
		{"region tag match is case-insensitive", []api.CommonOpenStackDomain{sto, fra}, "sto2", "dom-sto", ""},
		{"sole domain wins even when it does not list the region", []api.CommonOpenStackDomain{sto}, "Kna1", "dom-sto", ""},
		{"no domains", nil, "Sto2", "", "no OpenStack domains"},
		{"several domains, none serving the region", []api.CommonOpenStackDomain{sto, fra}, "Kna1", "", "set domain_id explicitly"},
		{"several domains serving the region", []api.CommonOpenStackDomain{sto, domain("dom-sto-b", "CCP_Domain_3", "Stockholm", "Sto2")}, "Sto2", "", "2 OpenStack domains serve region"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := domainForRegion(tc.domains, tc.region)
			if tc.wantErr == "" {
				if err != nil || got != tc.want {
					t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tc.want)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got (%q, %v), want error containing %q", got, err, tc.wantErr)
			}
			// Diagnostics must name the choices so the user can pick one.
			if len(tc.domains) > 1 && !strings.Contains(err.Error(), "dom-") {
				t.Errorf("error should list the candidate domain IDs: %v", err)
			}
		})
	}
}

func TestFindProjectByName(t *testing.T) {
	projects := []api.OpenStackIdentityProject{
		{Id: "p1", Name: "shared", DomainId: "dom-a", Enabled: true},
		{Id: "p2", Name: "shared", DomainId: "dom-b", Enabled: true},
		{Id: "p3", Name: "only", DomainId: "dom-a", Enabled: true},
	}
	if p, err := findProjectByName(projects, "dom-b", "shared"); err != nil || p.Id != "p2" {
		t.Errorf("domain-scoped lookup: got (%v, %v), want p2", p, err)
	}
	if p, err := findProjectByName(projects, "", "only"); err != nil || p.Id != "p3" {
		t.Errorf("unscoped unique lookup: got (%v, %v), want p3", p, err)
	}
	if _, err := findProjectByName(projects, "", "shared"); err == nil || !strings.Contains(err.Error(), "p1, p2") {
		t.Errorf("ambiguous lookup should list the candidate IDs, got %v", err)
	}
	if _, err := findProjectByName(projects, "dom-a", "missing"); err == nil || !strings.Contains(err.Error(), `"missing"`) {
		t.Errorf("missing project should be a clear error, got %v", err)
	}
}

func TestListProjectsDedupesAcrossRegions(t *testing.T) {
	// The API repeats a project under every region it is available in.
	body := `[{"region":{"id":1,"name":"Stockholm 2","tag":"Sto2","network_provider":"x","object_storage_enabled":true},"projects":[{"id":"p1","name":"a","domain_id":"d","enabled":true}]},
	          {"region":{"id":2,"name":"Karlskrona","tag":"Kna1","network_provider":"x","object_storage_enabled":true},"projects":[{"id":"p1","name":"a","domain_id":"d","enabled":true},{"id":"p2","name":"b","domain_id":"d","enabled":false}]}]`
	cfg := newTestConfig(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	projects, err := listProjects(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].Id != "p1" || projects[1].Id != "p2" {
		t.Fatalf("want [p1 p2], got %+v", projects)
	}
}

func TestDecodeJSONErrors(t *testing.T) {
	mk := func(status int, body string) *http.Response {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
	}
	// The API's error envelope is surfaced as its message plus the status.
	err := decodeJSON(mk(403, `{"error":{"code":403,"message":"Invalid token"}}`), nil)
	if err == nil || err.Error() != "Invalid token (HTTP 403)" {
		t.Errorf("envelope: got %v", err)
	}
	// 404 is distinguishable.
	err = decodeJSON(mk(404, `{"error":{"code":404,"message":"No such project"}}`), nil)
	if !isNotFound(err) || !strings.Contains(err.Error(), "No such project") {
		t.Errorf("404: got %v", err)
	}
	// Non-JSON bodies fall back to the raw text; empty bodies to the status alone.
	if err := decodeJSON(mk(502, "<html>bad gateway</html>"), nil); err == nil || !strings.Contains(err.Error(), "HTTP 502: <html>") {
		t.Errorf("raw body: got %v", err)
	}
	if err := decodeJSON(mk(500, ""), nil); err == nil || err.Error() != "HTTP 500" {
		t.Errorf("empty body: got %v", err)
	}
	// A 2xx with a body decodes; a 2xx without one (204) is fine with nil out.
	var out struct {
		Id string `json:"id"`
	}
	if err := decodeJSON(mk(201, `{"id":"abc"}`), &out); err != nil || out.Id != "abc" {
		t.Errorf("decode: got %v %+v", err, out)
	}
	if err := decodeJSON(mk(204, ""), nil); err != nil {
		t.Errorf("204: got %v", err)
	}
}

func TestOptionalStringValue(t *testing.T) {
	if v := optionalStringValue(nil); !v.IsNull() {
		t.Errorf("nil should be null, got %v", v)
	}
	if v := optionalStringValue(strp("")); !v.IsNull() {
		t.Errorf("empty should be null, got %v", v)
	}
	if v := optionalStringValue(strp("x")); v.ValueString() != "x" {
		t.Errorf("got %v", v)
	}
}

// The OpenStack schemas use write-only, defaults, and Optional+Computed
// combinations the framework validates at runtime; catch mistakes here rather
// than at the first terraform plan.
func TestOpenStackSchemasAreValid(t *testing.T) {
	ctx := context.Background()
	for _, r := range []resource.Resource{NewOpenStackProjectResource(), NewOpenStackUserResource(), NewOpenStackRoleAssignmentResource()} {
		var resp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%T: %v", r, diags)
		}
	}
	for _, d := range []datasource.DataSource{NewOpenStackProjectDataSource(), NewOpenStackUserDataSource()} {
		var resp datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%T: %v", d, diags)
		}
	}
}

func TestRoleIDsByName(t *testing.T) {
	byName := map[string]string{"member": "id-member", "swiftoperator": "id-swift"}
	ids, err := roleIDsByName(byName, []string{"swiftoperator", "member"})
	if err != nil || fmt.Sprint(ids) != "[id-swift id-member]" {
		t.Errorf("got (%v, %v)", ids, err)
	}
	_, err = roleIDsByName(byName, []string{"member", "admin"})
	if err == nil || !strings.Contains(err.Error(), `"admin"`) || !strings.Contains(err.Error(), "member, swiftoperator") {
		t.Errorf("unknown role should name it and list the available roles, got %v", err)
	}
}

func TestSetDiff(t *testing.T) {
	added, removed := setDiff([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	if fmt.Sprint(added) != "[a]" || fmt.Sprint(removed) != "[d]" {
		t.Errorf("got added=%v removed=%v", added, removed)
	}
	added, removed = setDiff([]string{"a"}, []string{"a"})
	if len(added) != 0 || len(removed) != 0 {
		t.Errorf("identical sets: got added=%v removed=%v", added, removed)
	}
}
