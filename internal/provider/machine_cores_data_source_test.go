package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// readMachineCores reads data.livellm_machine_cores from a fake platform
// that answers GET /v1/machine-cores with status and body (for the test key
// only).
func readMachineCores(t *testing.T, status int, body string) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/machine-cores" || r.Header.Get("x-api-key") != "llc_test" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(rw, "unexpected", http.StatusTeapot)
			return
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	d := NewMachineCoresDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{
		ProviderData: &providerData{Client: client.New(srv.URL, "llc_test")},
	}, &datasource.ConfigureResponse{})
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	d.Read(ctx, datasource.ReadRequest{}, &resp)
	return resp
}

// The data source is the provider's, named livellm_machine_cores, takes no
// argument (the key pins the workspace) and says cores, never vCPU.
func TestMachineCoresSchema(t *testing.T) {
	ctx := context.Background()
	found := false
	for _, f := range (&livellmProvider{}).DataSources(ctx) {
		var md datasource.MetadataResponse
		f().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "livellm"}, &md)
		found = found || md.TypeName == "livellm_machine_cores"
	}
	if !found {
		t.Fatal("livellm_machine_cores is not among the provider's data sources")
	}
	var sr datasource.SchemaResponse
	NewMachineCoresDataSource().Schema(ctx, datasource.SchemaRequest{}, &sr)
	if d := sr.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema: %v", d)
	}
	descs := []string{sr.Schema.Description}
	for name, a := range sr.Schema.Attributes {
		if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s: want computed only", name)
		}
		descs = append(descs, a.GetDescription())
	}
	speeds, ok := sr.Schema.Attributes["speeds"].(schema.ListNestedAttribute)
	if !ok {
		t.Fatalf("speeds: %T", sr.Schema.Attributes["speeds"])
	}
	for name, a := range speeds.NestedObject.Attributes {
		if !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("speeds.%s: want computed only", name)
		}
		descs = append(descs, a.GetDescription())
	}
	for _, d := range descs {
		if strings.Contains(strings.ToLower(d), "vcpu") {
			t.Errorf("the product says cores: %q", d)
		}
	}
}

// The platform's answer reads into the three attributes: each speed fastest
// first with its fit, and an empty list (never null) where the servers run
// at one speed, whether the answer lists none or leaves speeds out.
func TestMachineCoresRead(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name, body string
		show       bool
		speeds     []coreSpeedModel
		fit        int64
	}{
		{"two speeds", `{"showSpeed":true,"speeds":[{"speedMHz":5700,"fit":6},{"speedMHz":4400,"fit":16}],"dedicatedFit":16}`,
			true, []coreSpeedModel{
				{Speed: types.Int64Value(5700), Fit: types.Int64Value(6)},
				{Speed: types.Int64Value(4400), Fit: types.Int64Value(16)},
			}, 16},
		{"a speed with no room", `{"showSpeed":true,"speeds":[{"speedMHz":5700,"fit":0},{"speedMHz":4400,"fit":8}],"dedicatedFit":8}`,
			true, []coreSpeedModel{
				{Speed: types.Int64Value(5700), Fit: types.Int64Value(0)},
				{Speed: types.Int64Value(4400), Fit: types.Int64Value(8)},
			}, 8},
		{"one speed", `{"showSpeed":false,"speeds":[],"dedicatedFit":12}`, false, []coreSpeedModel{}, 12},
		{"speeds left out", `{"showSpeed":false,"dedicatedFit":0}`, false, []coreSpeedModel{}, 0},
	}
	for _, c := range cases {
		resp := readMachineCores(t, http.StatusOK, c.body)
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: read: %v", c.name, resp.Diagnostics)
			continue
		}
		var m machineCoresModel
		if d := resp.State.Get(ctx, &m); d.HasError() {
			t.Fatalf("%s: state: %v", c.name, d)
		}
		if !m.ShowSpeed.Equal(types.BoolValue(c.show)) || !m.DedicatedFit.Equal(types.Int64Value(c.fit)) {
			t.Errorf("%s: show_speed %v, dedicated_fit %v", c.name, m.ShowSpeed, m.DedicatedFit)
		}
		if m.Speeds.IsNull() || m.Speeds.IsUnknown() {
			t.Errorf("%s: speeds %v, want a list", c.name, m.Speeds)
			continue
		}
		speeds := []coreSpeedModel{}
		m.Speeds.ElementsAs(ctx, &speeds, false)
		if !reflect.DeepEqual(speeds, c.speeds) {
			t.Errorf("%s: speeds %v, want %v", c.name, speeds, c.speeds)
		}
	}
}

// A platform without dedicated cores has no such route: the error says what
// it needs, not a bare 404. Any other failure keeps the platform's answer.
func TestMachineCoresReadRefused(t *testing.T) {
	resp := readMachineCores(t, http.StatusNotFound, "404 page not found")
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 404 should be an error")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, "needs a LiveLLM API with dedicated cores") || strings.Contains(d, "404") {
		t.Errorf("404: %q", d)
	}
	resp = readMachineCores(t, http.StatusServiceUnavailable, `{"error":"servers can't be read right now: timeout"}`)
	if !resp.Diagnostics.HasError() {
		t.Fatal("a 503 should be an error")
	}
	if d := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(d, "servers can't be read right now") {
		t.Errorf("503: %q", d)
	}
}
