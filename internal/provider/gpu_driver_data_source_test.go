package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// readGPUDriver reads data.livellm_gpu_driver for vm "box" from a fake
// platform answering GET /v1/workloads/box/gpu-driver with status and body.
func readGPUDriver(t *testing.T, status int, body string) (datasource.ReadResponse, gpuDriverModel) {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/workloads/box/gpu-driver" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	d := NewGPUDriverDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{
		ProviderData: &providerData{Client: client.New(srv.URL, "llc_test")},
	}, &datasource.ConfigureResponse{})
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	ty := sr.Schema.Type().TerraformType(ctx)
	cfg := tftypes.NewValue(ty, map[string]tftypes.Value{
		"vm":     tftypes.NewValue(tftypes.String, "box"),
		"state":  tftypes.NewValue(tftypes.String, nil),
		"detail": tftypes.NewValue(tftypes.String, nil),
		"log":    tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
	})
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(ty, nil)}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg}}, &resp)
	var m gpuDriverModel
	if !resp.Diagnostics.HasError() {
		resp.State.Get(ctx, &m)
	}
	return resp, m
}

func TestGPUDriverRead(t *testing.T) {
	_, m := readGPUDriver(t, 200, `{"state":"working","detail":"620.70.06","log":["a","b"]}`)
	var logs []string
	m.Log.ElementsAs(context.Background(), &logs, false)
	if m.State != types.StringValue("working") || m.Detail != types.StringValue("620.70.06") || len(logs) != 2 {
		t.Fatalf("read %+v", m)
	}
	_, m = readGPUDriver(t, 200, `{"state":"none"}`)
	if m.State.ValueString() != "none" || m.Detail.ValueString() != "" || m.Log.IsNull() {
		t.Fatalf("none: %+v", m)
	}
}

// A stopped or unreachable machine is a state, never a failed plan; an API
// without the route says which version it needs.
func TestGPUDriverRefusals(t *testing.T) {
	_, m := readGPUDriver(t, 409, `{"error":"box is stopped","code":"machine_stopped"}`)
	if m.State.ValueString() != "machine-stopped" || m.Detail.ValueString() != "box is stopped" {
		t.Fatalf("409: %+v", m)
	}
	_, m = readGPUDriver(t, 504, `{"error":"box doesn't answer","code":"machine_unreachable"}`)
	if m.State.ValueString() != "machine-unreachable" {
		t.Fatalf("504: %+v", m)
	}
	resp, _ := readGPUDriver(t, 404, "404 page not found\n")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "0.101.9") {
		t.Fatalf("route missing: %v", resp.Diagnostics)
	}
	resp, _ = readGPUDriver(t, 422, `{"error":"box's system has no automatic NVIDIA driver.","code":"gpu_driver_unsupported"}`)
	if !resp.Diagnostics.HasError() {
		t.Fatal("422 should fail")
	}
}
