package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// devices and time_zone are optional and computed and plan with
// keepWhenLeftOut, never a Default: left out, the machine keeps what it has.
func TestDevicesAndTimeZoneAttributes(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema: %v", d)
	}
	dev, ok := resp.Schema.Attributes["devices"].(schema.ListAttribute)
	if !ok || !dev.Optional || !dev.Computed || dev.Default != nil || len(dev.PlanModifiers) != 1 {
		t.Fatalf("devices: %+v", resp.Schema.Attributes["devices"])
	}
	if _, ok := dev.PlanModifiers[0].(keepWhenLeftOut); !ok {
		t.Errorf("devices: plan modifier %T", dev.PlanModifiers[0])
	}
	tz, ok := resp.Schema.Attributes["time_zone"].(schema.StringAttribute)
	if !ok || !tz.Optional || !tz.Computed || tz.Default != nil || len(tz.PlanModifiers) != 1 {
		t.Fatalf("time_zone: %+v", resp.Schema.Attributes["time_zone"])
	}
	if _, ok := tz.PlanModifiers[0].(keepWhenLeftOut); !ok {
		t.Errorf("time_zone: plan modifier %T", tz.PlanModifiers[0])
	}
}

func TestKeepWhenLeftOut(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"x": tftypes.String}}
	state := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{"x": tftypes.NewValue(tftypes.String, "a")})}
	noState := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	srun := func(st tfsdk.State, cfg, sv types.String) types.String {
		req := planmodifier.StringRequest{State: st, ConfigValue: cfg, StateValue: sv, PlanValue: types.StringUnknown()}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		keepWhenLeftOut{}.PlanModifyString(ctx, req, resp)
		return resp.PlanValue
	}
	if got := srun(noState, types.StringNull(), types.StringNull()); !got.IsNull() {
		t.Errorf("create, left out: %v", got)
	}
	if got := srun(state, types.StringNull(), types.StringValue("Europe/Moscow")); got.ValueString() != "Europe/Moscow" {
		t.Errorf("update, left out: %v", got)
	}
	if got := srun(state, types.StringValue("UTC"), types.StringValue("Europe/Moscow")); !got.IsUnknown() {
		t.Errorf("configured: plan touched: %v", got)
	}
	lrun := func(st tfsdk.State, cfg, sv types.List) types.List {
		req := planmodifier.ListRequest{State: st, ConfigValue: cfg, StateValue: sv, PlanValue: types.ListUnknown(types.StringType)}
		resp := &planmodifier.ListResponse{PlanValue: req.PlanValue}
		keepWhenLeftOut{}.PlanModifyList(ctx, req, resp)
		return resp.PlanValue
	}
	if got := lrun(noState, types.ListNull(types.StringType), types.ListNull(types.StringType)); got.IsNull() || got.IsUnknown() || len(got.Elements()) != 0 {
		t.Errorf("list create, left out: %v (want [])", got)
	}
	held := stringList([]string{"rtx5090"})
	if got := lrun(state, types.ListNull(types.StringType), held); !got.Equal(held) {
		t.Errorf("list update, left out: %v", got)
	}
}

// Devices are sent only when the configuration writes them; [] removes them.
func TestVMDevicesSpec(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  types.List
		want any // nil: not sent
	}{
		{"left out", types.ListNull(types.StringType), nil},
		{"not known", types.ListUnknown(types.StringType), nil},
		{"removed", stringList(nil), []string{}},
		{"written", stringList([]string{"rtx5090", "station-usb"}), []string{"rtx5090", "station-usb"}},
	} {
		spec := map[string]any{}
		vmDevicesSpec(c.cfg, spec)
		got, sent := spec["devices"]
		if c.want == nil {
			if sent {
				t.Errorf("%s: sent %v", c.name, got)
			}
			continue
		}
		if b, _ := json.Marshal(got); string(b) != mustJSON(t, c.want) {
			t.Errorf("%s: sent %s", c.name, b)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadVMDevicesAndTimeZone(t *testing.T) {
	if got := readVMDevices(map[string]any{}); got.IsNull() || len(got.Elements()) != 0 {
		t.Errorf("none: %v", got)
	}
	sp := map[string]any{"devices": []any{"rtx5090"}, "hostDevices": []any{"nvidia.com/RTX5090"}, "timeZone": "Europe/Moscow"}
	if got := listStrings(readVMDevices(sp)); len(got) != 1 || got[0] != "rtx5090" {
		t.Errorf("devices: %v", got)
	}
	if got := readVMTimeZone(sp); got.ValueString() != "Europe/Moscow" {
		t.Errorf("time zone: %v", got)
	}
	if got := readVMTimeZone(map[string]any{}); !got.IsNull() {
		t.Errorf("no time zone: %v", got)
	}
}

func TestVMDeviceErrors(t *testing.T) {
	if errs := vmDeviceErrors(stringList([]string{"a", "b"})); len(errs) != 0 {
		t.Errorf("fine: %v", errs)
	}
	if errs := vmDeviceErrors(stringList([]string{"a", "a"})); len(errs) != 1 || !strings.Contains(errs[0][1], "twice") {
		t.Errorf("twice: %v", errs)
	}
	nine := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	if errs := vmDeviceErrors(stringList(nine)); len(errs) != 1 || !strings.Contains(errs[0][1], "at most 8") {
		t.Errorf("nine: %v", errs)
	}
}

// Every write carries the time zone state holds, so the platform keeps it
// (it doesn't over a save that leaves it out).
func TestVMSpecCarriesTimeZone(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{Name: types.StringValue("box"), OS: types.StringValue("ubuntu"), TimeZone: types.StringValue("Europe/Moscow")}
	if got := vmSpec(ctx, m, vmWrite{})["timeZone"]; got != "Europe/Moscow" {
		t.Errorf("timeZone %v", got)
	}
	m.TimeZone = types.StringNull()
	if _, sent := vmSpec(ctx, m, vmWrite{})["timeZone"]; sent {
		t.Error("a machine with no zone sent one")
	}
}

// What the platform decides about a machine is never in a write: the machine
// type it first ran on, its disk and network card kind, the devices resolved
// into what is passed through, where its cores are, whether it is installed.
func TestVMSpecSendsNothingThePlatformOwns(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{
		Name: types.StringValue("win"), OS: types.StringValue("windows"), WindowsEdition: types.StringValue("desktop"),
		CPUs: types.Int64Value(4), MemoryGi: types.Int64Value(8), DiskGi: types.Int64Value(64),
		DedicatedCores: types.BoolValue(true), CoreSpeed: types.Int64Value(5700), MixedSpeeds: types.BoolValue(false),
		TimeZone: types.StringValue("Europe/Moscow"), Username: types.StringValue("dev"),
	}
	wr := vmWrite{password: "S3cure-Pass!", devices: stringList([]string{"rtx5090"}), portGroups: portGroupList(nil)}
	spec := vmSpec(ctx, m, wr)
	for _, k := range []string{"machineType", "virtio", "virtioMove", "virtioMoveAt", "installed", "hostDevices", "hideHypervisor", "pin"} {
		if _, sent := spec[k]; sent {
			t.Errorf("%s sent: %v", k, spec[k])
		}
	}
	if spec["devices"] == nil || spec["portGroups"] == nil {
		t.Errorf("devices %v, portGroups %v", spec["devices"], spec["portGroups"])
	}
}

func TestTimeZoneChange(t *testing.T) {
	m := func(tz types.String) vmResourceModel {
		return vmResourceModel{Name: types.StringValue("box"), TimeZone: tz}
	}
	z := types.StringValue
	if e := timeZoneChange(m(z("Europe/Moscow")), m(z("Europe/Moscow"))); e != nil {
		t.Errorf("kept: %v", e)
	}
	if e := timeZoneChange(m(types.StringUnknown()), m(z("Europe/Moscow"))); e != nil {
		t.Errorf("not known yet: %v", e)
	}
	if e := timeZoneChange(m(types.StringNull()), m(types.StringNull())); e != nil {
		t.Errorf("none: %v", e)
	}
	e := timeZoneChange(m(z("Asia/Tokyo")), m(z("Europe/Moscow")))
	if e == nil || !strings.Contains(e[1], `keep time_zone = "Europe/Moscow"`) || !strings.Contains(e[1], "on the machine itself") {
		t.Errorf("changed: %v", e)
	}
	e = timeZoneChange(m(z("UTC")), m(types.StringNull()))
	if e == nil || !strings.Contains(e[1], "leave time_zone out") {
		t.Errorf("added: %v", e)
	}
}

// ModifyPlan, on real plans: a new time zone on an existing machine is
// refused, never planned as a replacement; a machine replaced anyway (a new
// name) takes the new zone.
func TestVMModifyPlanTimeZone(t *testing.T) {
	ctx := context.Background()
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	type vals = map[string]tftypes.Value
	for _, c := range []struct {
		name                string
		config, plan, state vals
		refused             bool
	}{
		{name: "kept", config: vals{"time_zone": s("Europe/Moscow")}, plan: vals{"time_zone": s("Europe/Moscow")},
			state: vals{"time_zone": s("Europe/Moscow")}},
		{name: "changed", config: vals{"time_zone": s("Asia/Tokyo")}, plan: vals{"time_zone": s("Asia/Tokyo")},
			state: vals{"time_zone": s("Europe/Moscow")}, refused: true},
		{name: "replaced for its name", config: vals{"name": s("box2"), "time_zone": s("Asia/Tokyo")},
			plan: vals{"name": s("box2"), "time_zone": s("Asia/Tokyo")}, state: vals{"time_zone": s("Europe/Moscow")}},
	} {
		sr, cfg := vmPlanValue(t, c.config)
		_, plan := vmPlanValue(t, c.plan)
		_, state := vmPlanValue(t, c.state)
		req := resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg},
			Plan:   tfsdk.Plan{Schema: sr.Schema, Raw: plan},
			State:  tfsdk.State{Schema: sr.Schema, Raw: state},
		}
		resp := &resource.ModifyPlanResponse{Plan: req.Plan}
		(&vmResource{}).ModifyPlan(ctx, req, resp)
		errs := resp.Diagnostics.Errors()
		if !c.refused {
			if len(errs) > 0 {
				t.Errorf("%s: %v", c.name, errs)
			}
			continue
		}
		if len(errs) != 1 {
			t.Errorf("%s: errors %v, want one", c.name, resp.Diagnostics)
			continue
		}
		if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root("time_zone")) {
			t.Errorf("%s: not on time_zone: %v", c.name, errs[0])
		}
	}
}

// After a write: devices left unknown read back; devices the write sent and
// the platform didn't keep warn; devices kept say nothing.
func TestSettleVMDevicesAndTimeZone(t *testing.T) {
	m := vmResourceModel{Name: types.StringValue("box"), Devices: types.ListUnknown(types.StringType), TimeZone: types.StringUnknown()}
	var diags diag.Diagnostics
	settleVMDevices(&m, map[string]any{"devices": []any{"rtx5090"}}, false, &diags)
	settleVMTimeZone(&m, map[string]any{"timeZone": "UTC"}, &diags)
	if got := listStrings(m.Devices); len(got) != 1 || m.TimeZone.ValueString() != "UTC" || diags.WarningsCount() != 0 {
		t.Errorf("unknown: %v %v %v", got, m.TimeZone, diags)
	}
	m.Devices = stringList([]string{"rtx5090"})
	settleVMDevices(&m, map[string]any{}, true, &diags)
	if diags.WarningsCount() != 1 || !strings.Contains(diags.Warnings()[0].Detail(), "no devices, not rtx5090") {
		t.Errorf("dropped: %v", diags)
	}
	if got := listStrings(m.Devices); len(got) != 1 {
		t.Errorf("state must keep the plan: %v", got)
	}
	diags = nil
	settleVMDevices(&m, map[string]any{"devices": []any{"rtx5090"}}, true, &diags)
	settleVMDevices(&m, map[string]any{}, false, &diags) // not sent: the platform's own business
	m.TimeZone = types.StringValue("Europe/Moscow")
	settleVMTimeZone(&m, map[string]any{}, &diags)
	if diags.WarningsCount() != 1 || diags.Warnings()[0].Summary() != "Time zone not kept as written" {
		t.Errorf("kept / zone dropped: %v", diags)
	}
}

// readMachineDevices reads data.livellm_machine_devices from a fake platform
// that answers GET /v1/machine-devices with status and body.
func readMachineDevices(t *testing.T, status int, body string) datasource.ReadResponse {
	t.Helper()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/machine-devices" || r.Header.Get("x-api-key") != "llc_test" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(rw, "unexpected", http.StatusTeapot)
			return
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	d := NewMachineDevicesDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{
		ProviderData: &providerData{Client: client.New(srv.URL, "llc_test")},
	}, &datasource.ConfigureResponse{})
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	if diags := sr.Schema.ValidateImplementation(ctx); diags.HasError() {
		t.Fatalf("schema: %v", diags)
	}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	d.Read(ctx, datasource.ReadRequest{}, &resp)
	return resp
}

func TestMachineDevicesDataSource(t *testing.T) {
	ctx := context.Background()
	found := false
	for _, f := range (&livellmProvider{}).DataSources(ctx) {
		var md datasource.MetadataResponse
		f().Metadata(ctx, datasource.MetadataRequest{ProviderTypeName: "livellm"}, &md)
		found = found || md.TypeName == "livellm_machine_devices"
	}
	if !found {
		t.Fatal("livellm_machine_devices is not among the provider's data sources")
	}
	resp := readMachineDevices(t, 200, `{"devices":[{"id":"rtx5090","label":"NVIDIA RTX 5090","kind":"gpu","total":1,"free":0,"here":["workstation"]}]}`)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var m machineDevicesModel
	resp.State.Get(ctx, &m)
	if len(m.Devices.Elements()) != 1 {
		t.Fatalf("devices: %v", m.Devices)
	}
	got := m.Devices.Elements()[0].(types.Object).Attributes()
	want := map[string]attr.Value{"id": types.StringValue("rtx5090"), "label": types.StringValue("NVIDIA RTX 5090"),
		"kind": types.StringValue("gpu"), "total": types.Int64Value(1), "free": types.Int64Value(0),
		"machines": stringList([]string{"workstation"})}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("%s: %v, want %v", k, got[k], v)
		}
	}
	resp = readMachineDevices(t, 200, `{"devices":[]}`)
	resp.State.Get(ctx, &m)
	if m.Devices.IsNull() || len(m.Devices.Elements()) != 0 {
		t.Errorf("none: %v", m.Devices)
	}
	resp = readMachineDevices(t, http.StatusNotFound, "404 page not found")
	if !resp.Diagnostics.HasError() || !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), "needs a LiveLLM API with machine devices") {
		t.Errorf("404: %v", resp.Diagnostics)
	}
}

// A refresh keeps the zone state holds when the platform forgot it (a save
// that left it out); an import reads the platform's.
func TestRefreshVMTimeZone(t *testing.T) {
	msk := types.StringValue("Europe/Moscow")
	if got := refreshVMTimeZone(msk, map[string]any{}); !got.Equal(msk) {
		t.Errorf("forgotten: %v", got)
	}
	if got := refreshVMTimeZone(msk, map[string]any{"timeZone": "UTC"}); got.ValueString() != "UTC" {
		t.Errorf("changed elsewhere: %v", got)
	}
	if got := refreshVMTimeZone(types.StringNull(), map[string]any{"timeZone": "UTC"}); got.ValueString() != "UTC" {
		t.Errorf("import: %v", got)
	}
	if got := refreshVMTimeZone(types.StringNull(), map[string]any{}); !got.IsNull() {
		t.Errorf("none: %v", got)
	}
}
