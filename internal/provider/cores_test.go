package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// The three settings are optional and computed and plan with keepCoreChoice,
// never a Default (which would send false on every save that leaves them
// out); what the running machine got is computed only.
func TestCoreAttributes(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema: %v", d)
	}
	for _, a := range []string{"dedicated_cores", "mixed_speeds"} {
		v, ok := resp.Schema.Attributes[a].(schema.BoolAttribute)
		if !ok || !v.Optional || !v.Computed || v.Default != nil || len(v.PlanModifiers) != 1 {
			t.Errorf("%s: %+v", a, resp.Schema.Attributes[a])
			continue
		}
		if _, ok := v.PlanModifiers[0].(keepCoreChoice); !ok {
			t.Errorf("%s: plan modifier %T", a, v.PlanModifiers[0])
		}
	}
	speed, ok := resp.Schema.Attributes["core_speed"].(schema.Int64Attribute)
	if !ok || !speed.Optional || !speed.Computed || speed.Default != nil || len(speed.PlanModifiers) != 1 {
		t.Fatalf("core_speed: %+v", resp.Schema.Attributes["core_speed"])
	}
	if _, ok := speed.PlanModifiers[0].(keepCoreChoice); !ok {
		t.Errorf("core_speed: plan modifier %T", speed.PlanModifiers[0])
	}
	for _, a := range []string{"running_core_speed", "running_mixed_speeds"} {
		if v := resp.Schema.Attributes[a]; v == nil || !v.IsComputed() || v.IsOptional() || v.IsRequired() {
			t.Errorf("%s: %+v, want computed only", a, v)
		}
	}
	for _, a := range resp.Schema.Attributes {
		if strings.Contains(strings.ToLower(a.GetDescription()), "vcpu") {
			t.Errorf("the product says cores: %q", a.GetDescription())
		}
	}
}

func TestKeepCoreChoicePlans(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"x": tftypes.Bool}}
	state := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{"x": tftypes.NewValue(tftypes.Bool, true)})}
	noState := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	run := func(st tfsdk.State, cfg, stateVal types.Bool) types.Bool {
		req := planmodifier.BoolRequest{State: st, ConfigValue: cfg, StateValue: stateVal, PlanValue: types.BoolUnknown()}
		resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		keepCoreChoice{}.PlanModifyBool(ctx, req, resp)
		return resp.PlanValue
	}
	// A new machine is shared unless asked: known at plan.
	if got := run(noState, types.BoolNull(), types.BoolNull()); !got.Equal(types.BoolValue(false)) {
		t.Errorf("create, left out: %v", got)
	}
	// Left out later: what the machine has (the console's choice included).
	if got := run(state, types.BoolNull(), types.BoolValue(true)); !got.Equal(types.BoolValue(true)) {
		t.Errorf("update, left out: %v", got)
	}
	if got := run(state, types.BoolNull(), types.BoolNull()); !got.IsNull() {
		t.Errorf("update, a machine read before the setting existed: %v", got)
	}
	if got := run(state, types.BoolValue(false), types.BoolValue(true)); !got.IsUnknown() {
		t.Errorf("configured: plan touched: %v", got)
	}

	irun := func(st tfsdk.State, cfg, stateVal types.Int64) types.Int64 {
		req := planmodifier.Int64Request{State: st, ConfigValue: cfg, StateValue: stateVal, PlanValue: types.Int64Unknown()}
		resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
		keepCoreChoice{}.PlanModifyInt64(ctx, req, resp)
		return resp.PlanValue
	}
	if got := irun(noState, types.Int64Null(), types.Int64Null()); !got.Equal(types.Int64Value(0)) {
		t.Errorf("int64 create, left out: %v", got)
	}
	if got := irun(state, types.Int64Null(), types.Int64Value(5700)); !got.Equal(types.Int64Value(5700)) {
		t.Errorf("int64 update, left out: %v", got)
	}
	if got := irun(state, types.Int64Value(4400), types.Int64Value(5700)); !got.IsUnknown() {
		t.Errorf("int64 configured: plan touched: %v", got)
	}
}

// What the platform doesn't hold reads as false and 0, so a configured
// false or 0 never shows as a change; the pin is the platform's and is
// never read or sent.
func TestReadAndSendVMCores(t *testing.T) {
	var m vmResourceModel
	readVMCores(&m, map[string]any{})
	if !m.DedicatedCores.Equal(types.BoolValue(false)) || !m.MixedSpeeds.Equal(types.BoolValue(false)) ||
		!m.CoreSpeed.Equal(types.Int64Value(0)) {
		t.Errorf("read none: %v %v %v", m.DedicatedCores, m.MixedSpeeds, m.CoreSpeed)
	}
	readVMCores(&m, map[string]any{"dedicatedCores": true, "coreSpeed": float64(5700),
		"pin": map[string]any{"host": "selangor", "cpus": "0-15", "speedMHz": float64(5700)}})
	if !m.DedicatedCores.Equal(types.BoolValue(true)) || !m.MixedSpeeds.Equal(types.BoolValue(false)) ||
		!m.CoreSpeed.Equal(types.Int64Value(5700)) {
		t.Errorf("read: %v %v %v", m.DedicatedCores, m.MixedSpeeds, m.CoreSpeed)
	}
	readVMCores(&m, map[string]any{"dedicatedCores": true, "mixedSpeeds": true})
	if !m.MixedSpeeds.Equal(types.BoolValue(true)) || !m.CoreSpeed.Equal(types.Int64Value(0)) {
		t.Errorf("read mixed: %v %v", m.MixedSpeeds, m.CoreSpeed)
	}

	// A write sends each known setting, false and 0 included (that clears).
	ctx := context.Background()
	m = vmResourceModel{SSHKeys: types.ListNull(types.StringType), DedicatedCores: types.BoolValue(true),
		MixedSpeeds: types.BoolValue(false), CoreSpeed: types.Int64Value(0)}
	spec := vmSpec(ctx, m, vmWrite{})
	if spec["dedicatedCores"] != true || spec["mixedSpeeds"] != false || spec["coreSpeed"] != int64(0) {
		t.Errorf("sent %v %v %v", spec["dedicatedCores"], spec["mixedSpeeds"], spec["coreSpeed"])
	}
	if _, sent := spec["pin"]; sent {
		t.Error("the pin is the platform's")
	}
	m.DedicatedCores, m.MixedSpeeds, m.CoreSpeed = types.BoolUnknown(), types.BoolUnknown(), types.Int64Unknown()
	spec = vmSpec(ctx, m, vmWrite{})
	for _, k := range []string{"dedicatedCores", "mixedSpeeds", "coreSpeed"} {
		if _, sent := spec[k]; sent {
			t.Errorf("unknown %s sent as %v", k, spec[k])
		}
	}

	// A write that left them unknown takes what the platform holds.
	settleVMCores(&m, map[string]any{"dedicatedCores": true, "coreSpeed": float64(4400)})
	if !m.DedicatedCores.Equal(types.BoolValue(true)) || !m.MixedSpeeds.Equal(types.BoolValue(false)) ||
		!m.CoreSpeed.Equal(types.Int64Value(4400)) {
		t.Errorf("settle: %v %v %v", m.DedicatedCores, m.MixedSpeeds, m.CoreSpeed)
	}
	planned := vmResourceModel{DedicatedCores: types.BoolValue(true), MixedSpeeds: types.BoolUnknown(), CoreSpeed: types.Int64Value(5700)}
	settleVMCores(&planned, nil)
	if !planned.DedicatedCores.Equal(types.BoolValue(true)) || !planned.MixedSpeeds.Equal(types.BoolValue(false)) ||
		!planned.CoreSpeed.Equal(types.Int64Value(5700)) {
		t.Errorf("settle keeps what was planned: %v %v %v", planned.DedicatedCores, planned.MixedSpeeds, planned.CoreSpeed)
	}
}

// vmPlanValue is a livellm_vm object with these attributes set and the rest
// null.
func vmPlanValue(t *testing.T, set map[string]tftypes.Value) (resource.SchemaResponse, tftypes.Value) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["name"] = tftypes.NewValue(tftypes.String, "box")
	vals["username"] = tftypes.NewValue(tftypes.String, "dev")
	vals["password_wo_version"] = tftypes.NewValue(tftypes.Number, 1)
	for k, v := range set {
		vals[k] = v
	}
	return sr, tftypes.NewValue(typ, vals)
}

// ModifyPlan, on real plans: core_speed and mixed_speeds plan cleared on
// shared cores (the platform drops them) and are refused there when set;
// while dedicated_cores is unknown, so are the ones left out; on dedicated
// cores they stay as planned.
func TestVMModifyPlanCores(t *testing.T) {
	ctx := context.Background()
	b := func(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }
	n := func(v int) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }
	unknownBool := tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue)
	type vals = map[string]tftypes.Value
	dedicated := vals{"dedicated_cores": b(true), "mixed_speeds": b(true), "core_speed": n(5700)}
	for _, c := range []struct {
		name                string
		config, plan, state vals // state nil: a create
		refusedOn           string
		wantMixed           any // bool, or nil for unknown
		wantSpeed           any // int, or nil for unknown
	}{
		{name: "create, left out", plan: vals{"dedicated_cores": b(false), "mixed_speeds": b(false), "core_speed": n(0)},
			wantMixed: false, wantSpeed: 0},
		{name: "create, dedicated at a speed", config: vals{"dedicated_cores": b(true), "core_speed": n(5700)},
			plan: vals{"dedicated_cores": b(true), "mixed_speeds": b(false), "core_speed": n(5700)}, wantMixed: false, wantSpeed: 5700},
		{name: "kept dedicated, left out", plan: dedicated, state: dedicated, wantMixed: true, wantSpeed: 5700},
		{name: "turned shared: the rest clears", config: vals{"dedicated_cores": b(false)},
			plan: vals{"dedicated_cores": b(false), "mixed_speeds": b(true), "core_speed": n(5700)}, state: dedicated,
			wantMixed: false, wantSpeed: 0},
		{name: "a speed on shared cores", config: vals{"dedicated_cores": b(false), "core_speed": n(5700)},
			plan: vals{"dedicated_cores": b(false), "mixed_speeds": b(false), "core_speed": n(5700)}, refusedOn: "core_speed"},
		{name: "mixed speeds on cores kept shared", config: vals{"mixed_speeds": b(true)},
			plan:  vals{"dedicated_cores": b(false), "mixed_speeds": b(true), "core_speed": n(0)},
			state: vals{"dedicated_cores": b(false), "mixed_speeds": b(false), "core_speed": n(0)}, refusedOn: "mixed_speeds"},
		{name: "a speed of 0 on shared cores", config: vals{"dedicated_cores": b(false), "core_speed": n(0)},
			plan: vals{"dedicated_cores": b(false), "mixed_speeds": b(false), "core_speed": n(0)}, wantMixed: false, wantSpeed: 0},
		{name: "dedicated not known yet", config: vals{"dedicated_cores": unknownBool},
			plan: vals{"dedicated_cores": unknownBool, "mixed_speeds": b(false), "core_speed": n(0)}, state: vals{
				"dedicated_cores": b(false), "mixed_speeds": b(false), "core_speed": n(0)}, wantMixed: nil, wantSpeed: nil},
	} {
		sr, cfg := vmPlanValue(t, c.config)
		_, plan := vmPlanValue(t, c.plan)
		state := tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
		if c.state != nil {
			_, state = vmPlanValue(t, c.state)
		}
		req := resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg},
			Plan:   tfsdk.Plan{Schema: sr.Schema, Raw: plan},
			State:  tfsdk.State{Schema: sr.Schema, Raw: state},
		}
		resp := &resource.ModifyPlanResponse{Plan: req.Plan}
		(&vmResource{}).ModifyPlan(ctx, req, resp)
		if c.refusedOn != "" {
			errs := resp.Diagnostics.Errors()
			if len(errs) != 1 {
				t.Errorf("%s: errors %v, want one", c.name, resp.Diagnostics)
				continue
			}
			if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root(c.refusedOn)) {
				t.Errorf("%s: error not on %s: %v", c.name, c.refusedOn, errs[0])
			}
			continue
		}
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: %v", c.name, resp.Diagnostics)
			continue
		}
		var mixed types.Bool
		var speed types.Int64
		resp.Plan.GetAttribute(ctx, path.Root("mixed_speeds"), &mixed)
		resp.Plan.GetAttribute(ctx, path.Root("core_speed"), &speed)
		if want, ok := c.wantMixed.(bool); (ok && !mixed.Equal(types.BoolValue(want))) || (!ok && !mixed.IsUnknown()) {
			t.Errorf("%s: mixed_speeds %v, want %v", c.name, mixed, c.wantMixed)
		}
		if want, ok := c.wantSpeed.(int); (ok && !speed.Equal(types.Int64Value(int64(want)))) || (!ok && !speed.IsUnknown()) {
			t.Errorf("%s: core_speed %v, want %v", c.name, speed, c.wantSpeed)
		}
	}
}

// terraform validate already refuses a speed or mixed speeds written next
// to dedicated_cores = false.
func TestSharedCoreErrors(t *testing.T) {
	f, tr := types.BoolValue(false), types.BoolValue(true)
	if errs := sharedCoreErrors(f, tr, types.Int64Value(5700)); len(errs) != 2 ||
		errs[0][0] != "core_speed" || errs[1][0] != "mixed_speeds" {
		t.Errorf("shared with both: %v", errs)
	}
	for name, c := range map[string]struct {
		dedicated, mixed types.Bool
		speed            types.Int64
	}{
		"dedicated":          {tr, tr, types.Int64Value(5700)},
		"dedicated unknown":  {types.BoolUnknown(), tr, types.Int64Value(5700)},
		"dedicated left out": {types.BoolNull(), tr, types.Int64Value(5700)},
		"shared, cleared":    {f, f, types.Int64Value(0)},
		"shared, left out":   {f, types.BoolNull(), types.Int64Null()},
		"shared, not known":  {f, types.BoolUnknown(), types.Int64Unknown()},
	} {
		if errs := sharedCoreErrors(c.dedicated, c.mixed, c.speed); len(errs) != 0 {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// ValidateConfig itself (terraform validate) refuses a speed or mixed speeds
// written next to dedicated_cores = false, which the platform drops on shared
// cores so that every plan would show them again; core_speed = 0 and
// mixed_speeds = false are what it holds there and pass. With
// dedicated_cores left out, the machine's own decides at plan
// (TestVMModifyPlanCores).
func TestVMValidateConfigCores(t *testing.T) {
	ctx := context.Background()
	b := func(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }
	n := func(v int) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }
	unknownNum := tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	for _, c := range []struct {
		name      string
		config    map[string]tftypes.Value
		refusedOn []string
	}{
		{"shared at a speed", map[string]tftypes.Value{"dedicated_cores": b(false), "core_speed": n(5700)}, []string{"core_speed"}},
		{"shared on mixed speeds", map[string]tftypes.Value{"dedicated_cores": b(false), "mixed_speeds": b(true)}, []string{"mixed_speeds"}},
		{"shared with both", map[string]tftypes.Value{"dedicated_cores": b(false), "core_speed": n(4400), "mixed_speeds": b(true)},
			[]string{"core_speed", "mixed_speeds"}},
		{"shared, speed 0", map[string]tftypes.Value{"dedicated_cores": b(false), "core_speed": n(0), "mixed_speeds": b(false)}, nil},
		{"shared, speed not known yet", map[string]tftypes.Value{"dedicated_cores": b(false), "core_speed": unknownNum}, nil},
		{"dedicated at a speed", map[string]tftypes.Value{"dedicated_cores": b(true), "core_speed": n(5700), "mixed_speeds": b(true)}, nil},
		{"dedicated left out", map[string]tftypes.Value{"core_speed": n(5700), "mixed_speeds": b(true)}, nil},
	} {
		sr, cfg := vmPlanValue(t, c.config)
		resp := &resource.ValidateConfigResponse{}
		(&vmResource{}).ValidateConfig(ctx, resource.ValidateConfigRequest{Config: tfsdk.Config{Schema: sr.Schema, Raw: cfg}}, resp)
		errs := resp.Diagnostics.Errors()
		if len(errs) != len(c.refusedOn) {
			t.Errorf("%s: %v, want errors on %v", c.name, resp.Diagnostics, c.refusedOn)
			continue
		}
		for i, e := range errs {
			wp, ok := e.(diag.DiagnosticWithPath)
			if !ok || !wp.Path().Equal(path.Root(c.refusedOn[i])) {
				t.Errorf("%s: error %d not on %s: %v", c.name, i, c.refusedOn[i], e)
			}
			if !strings.Contains(e.Summary(), "is for dedicated cores") || !strings.Contains(e.Detail(), "dedicated_cores = true") {
				t.Errorf("%s: %q / %q", c.name, e.Summary(), e.Detail())
			}
		}
	}
}

// The four refusals keep the platform's (the owner's) words and say what to
// write: the console's "Use N cores" is cpus = N, "Continue with mixed
// speeds" is mixed_speeds = true.
func TestCoresRefusals(t *testing.T) {
	for _, c := range []struct {
		status        int
		body          string
		summary       string
		want, notWant []string
	}{
		{409, `{"error":"Only 6 cores at 5.7 GHz are available. Continue with mixed core speeds?","code":"core_speed_short","workload":"ws","cores":8,"fit":6,"speedMHz":5700}`,
			"Too few cores at that speed",
			[]string{"Cannot create VM: Only 6 cores at 5.7 GHz are available. Continue with mixed core speeds?",
				"Use 6 cores: set cpus = 6.", "Continue with mixed speeds: set mixed_speeds = true"}, nil},
		{409, `{"error":"Mixed core speeds: this machine's cores run at different clock speeds.","code":"core_speeds_mixed","workload":"ws","cores":20,"fit":16}`,
			"Mixed core speeds",
			[]string{"Mixed core speeds: this machine's cores run at different clock speeds.", "Use 16 cores: set cpus = 16.",
				"mixed_speeds = true"}, nil},
		{409, `{"error":"Only 0 cores at 5.7 GHz are available. Continue with mixed core speeds?","code":"core_speed_short","fit":0,"speedMHz":5700}`,
			"Too few cores at that speed", []string{"mixed_speeds = true"}, []string{"Use 0 cores", "cpus ="}},
		{422, `{"error":"No server this machine may run on has cores at 3 GHz.","code":"core_speed_unknown","workload":"ws","cores":4,"fit":0,"speedMHz":3000}`,
			"No cores at that speed",
			[]string{"No server this machine may run on has cores at 3 GHz.", "core_speed", "0 for any cores of one speed"}, []string{"cpus ="}},
		{409, `{"error":"This server is full: it has no room for this machine.","code":"server_full","workload":"ws"}`,
			"No room on the servers", []string{"This server is full: it has no room for this machine.", "fewer cores"}, nil},
		{409, `{"error":"No server has room for this machine.","code":"server_full","workload":"ws"}`,
			"No room on the servers", []string{"No server has room for this machine."}, nil},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", &client.APIError{Status: c.status, Body: c.body})
		if len(diags) != 1 || diags[0].Summary() != c.summary {
			t.Errorf("%s: %v", c.body, diags)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(diags[0].Detail(), w) {
				t.Errorf("%s: detail %q lacks %q", c.summary, diags[0].Detail(), w)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(diags[0].Detail(), w) {
				t.Errorf("%s: detail %q has %q", c.summary, diags[0].Detail(), w)
			}
		}
		if strings.Contains(strings.ToLower(diags[0].Detail()), "vcpu") {
			t.Errorf("the product says cores: %q", diags[0].Detail())
		}
	}
	// Every kind is held to the servers' room, not only machines: the
	// platform's words name the kind, the provider's never say machine.
	for _, c := range []struct{ summary, body string }{
		{"Cannot create app", `{"error":"No server has room for this app.","code":"server_full","workload":"web"}`},
		{"Cannot create database", `{"error":"This server is full: it has no room for this database.","code":"server_full","workload":"db"}`},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, c.summary, &client.APIError{Status: 409, Body: c.body})
		if len(diags) != 1 || diags[0].Summary() != "No room on the servers" {
			t.Errorf("%s: %v", c.body, diags)
			continue
		}
		var b struct{ Error string }
		_ = json.Unmarshal([]byte(c.body), &b)
		if d := diags[0].Detail(); !strings.HasPrefix(d, c.summary+": "+b.Error) ||
			strings.Contains(strings.TrimPrefix(d, c.summary+": "+b.Error), "machine") {
			t.Errorf("%s: detail %q", c.summary, d)
		}
	}
	// Another conflict, or a code on another status, is shown as it was.
	for _, e := range []*client.APIError{
		{Status: 409, Body: `{"error":"a name taken","code":"name_taken"}`},
		{Status: 400, Body: `{"error":"bad","code":"server_full"}`},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", e)
		if len(diags) != 1 || diags[0].Summary() != "Cannot create VM" {
			t.Errorf("%v: %v", e, diags)
		}
	}
}

// The status's cores: the clock of one speed, or mixed; nothing when the
// platform doesn't say.
func TestRunningCores(t *testing.T) {
	for _, c := range []struct {
		st    *client.WorkloadStatus
		speed types.Int64
		mixed types.Bool
	}{
		{nil, types.Int64Null(), types.BoolNull()},
		{&client.WorkloadStatus{}, types.Int64Null(), types.BoolNull()},
		{&client.WorkloadStatus{Cores: &client.CoreStatus{SpeedMHz: 5700}}, types.Int64Value(5700), types.BoolValue(false)},
		{&client.WorkloadStatus{Cores: &client.CoreStatus{Mixed: true, Message: "Mixed core speeds: …"}}, types.Int64Null(), types.BoolValue(true)},
	} {
		speed, mixed := runningCores(c.st)
		if !speed.Equal(c.speed) || !mixed.Equal(c.mixed) {
			t.Errorf("%+v: %v %v, want %v %v", c.st, speed, mixed, c.speed, c.mixed)
		}
	}
	m, d := vmModelFrom(context.Background(), client.WorkloadStatus{ID: "box", Type: "vm-ubuntu", Ready: true,
		Cores: &client.CoreStatus{SpeedMHz: 4400}})
	if d.HasError() || !m.RunningCoreSpeed.Equal(types.Int64Value(4400)) || !m.RunningMixed.Equal(types.BoolValue(false)) {
		t.Errorf("data source: %v %v %v", m.RunningCoreSpeed, m.RunningMixed, d)
	}
}

// coresPlatform answers the workspace and status reads with one machine,
// its settings and its status row as given.
func coresPlatform(t *testing.T, w client.Workload, status string) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspace":
			_ = json.NewEncoder(rw).Encode(map[string]any{"spec": map[string]any{"workloads": []client.Workload{w}}})
		case "/v1/status":
			_, _ = rw.Write([]byte(`{"workloads":[` + status + `]}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "llc_test")
}

// A refresh (and an import) reads the machine's cores and the clock its
// running cores got.
func TestVMReadCores(t *testing.T) {
	ctx := context.Background()
	w := client.Workload{ID: "box", Type: "vm-ubuntu", VM: map[string]any{"cpus": float64(8), "dedicatedCores": true,
		"coreSpeed": float64(5700), "pin": map[string]any{"host": "selangor", "cpus": "0-15", "speedMHz": float64(5700)}}}
	r := NewVMResource()
	r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{Client: coresPlatform(t, w,
			`{"id":"box","type":"vm-ubuntu","phase":"Running","ready":true,"cores":{"speedMHz":5700}}`)},
	}, &resource.ConfigureResponse{})
	var sr resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sr)
	st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
	st.SetAttribute(ctx, path.Root("name"), "box")
	resp := resource.ReadResponse{State: st}
	r.Read(ctx, resource.ReadRequest{State: st}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var m vmResourceModel
	resp.State.Get(ctx, &m)
	if !m.DedicatedCores.Equal(types.BoolValue(true)) || !m.CoreSpeed.Equal(types.Int64Value(5700)) ||
		!m.MixedSpeeds.Equal(types.BoolValue(false)) {
		t.Errorf("settings: %v %v %v", m.DedicatedCores, m.CoreSpeed, m.MixedSpeeds)
	}
	if !m.RunningCoreSpeed.Equal(types.Int64Value(5700)) || !m.RunningMixed.Equal(types.BoolValue(false)) {
		t.Errorf("running: %v %v", m.RunningCoreSpeed, m.RunningMixed)
	}
}

// After a write, a platform that didn't keep the cores (one older than this
// provider ignores them) is warned about; one that kept them, and false or 0
// sent to an older one, are not.
func TestWarnCoresNotKept(t *testing.T) {
	ctx := context.Background()
	asked := vmResourceModel{Name: types.StringValue("box"), SSHKeys: types.ListNull(types.StringType),
		CPUs: types.Int64Value(8), MemoryGi: types.Int64Value(16), DiskGi: types.Int64Value(80),
		DedicatedCores: types.BoolValue(true), MixedSpeeds: types.BoolValue(false), CoreSpeed: types.Int64Value(5700)}
	for _, c := range []struct {
		name  string
		vm    map[string]any
		plan  vmResourceModel
		warns bool
	}{
		{"an older platform", map[string]any{"cpus": float64(8)}, asked, true},
		{"kept", map[string]any{"cpus": float64(8), "dedicatedCores": true, "coreSpeed": float64(5700)}, asked, false},
		{"shared, on an older platform", map[string]any{"cpus": float64(8)}, func() vmResourceModel {
			m := asked
			m.DedicatedCores, m.CoreSpeed = types.BoolValue(false), types.Int64Value(0)
			return m
		}(), false},
	} {
		m := c.plan
		cl := coresPlatform(t, client.Workload{ID: "box", Type: "vm-ubuntu", VM: c.vm}, `{"id":"box","type":"vm-ubuntu","ready":true}`)
		var diags diag.Diagnostics
		refreshVMSpec(ctx, cl, &m, &diags)
		if got := len(diags.Warnings()) == 1 && diags.Warnings()[0].Summary() == "Cores not kept as written"; got != c.warns {
			t.Errorf("%s: %v, want warned %v", c.name, diags, c.warns)
		}
		if c.warns && !strings.Contains(diags.Warnings()[0].Detail(), "dedicated_cores, core_speed") {
			t.Errorf("%s: names %q", c.name, diags.Warnings()[0].Detail())
		}
	}
}

// planCoreChoice runs a core setting's own plan modifiers as the framework
// does when the configuration leaves it out: planned unknown (it is
// computed), against the prior state given (null on a create).
func planCoreChoice(t *testing.T, sch schema.Schema, cfg tfsdk.Config, st tfsdk.State, pl tfsdk.Plan, a string) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	p := path.Root(a)
	switch at := sch.Attributes[a].(type) {
	case schema.BoolAttribute:
		sv := types.BoolNull()
		if !st.Raw.IsNull() {
			st.GetAttribute(ctx, p, &sv)
		}
		req := planmodifier.BoolRequest{Path: p, Config: cfg, ConfigValue: types.BoolNull(), State: st, StateValue: sv,
			Plan: pl, PlanValue: types.BoolUnknown()}
		resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
		for _, m := range at.PlanModifiers {
			m.PlanModifyBool(ctx, req, resp)
			req.PlanValue = resp.PlanValue
		}
		v, _ := resp.PlanValue.ToTerraformValue(ctx)
		return v
	case schema.Int64Attribute:
		sv := types.Int64Null()
		if !st.Raw.IsNull() {
			st.GetAttribute(ctx, p, &sv)
		}
		req := planmodifier.Int64Request{Path: p, Config: cfg, ConfigValue: types.Int64Null(), State: st, StateValue: sv,
			Plan: pl, PlanValue: types.Int64Unknown()}
		resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
		for _, m := range at.PlanModifiers {
			m.PlanModifyInt64(ctx, req, resp)
			req.PlanValue = resp.PlanValue
		}
		v, _ := resp.PlanValue.ToTerraformValue(ctx)
		return v
	}
	t.Fatalf("%s: %T", a, sch.Attributes[a])
	return tftypes.Value{}
}

// platformKeepsCores is what the platform stores of a machine's core
// settings (tenant-api spec.CarryCoreChoice): one a save leaves out is the
// stored machine's, then only true and a clock, and only with dedicated
// cores.
func platformKeepsCores(was, sent map[string]any) map[string]any {
	held := map[string]any{}
	for _, k := range []string{"dedicatedCores", "mixedSpeeds", "coreSpeed"} {
		v, ok := sent[k]
		if !ok {
			v = was[k]
		}
		switch x := v.(type) {
		case bool:
			if x {
				held[k] = true
			}
		case int64:
			if x > 0 {
				held[k] = float64(x)
			}
		case float64:
			if x > 0 {
				held[k] = x
			}
		}
	}
	if held["dedicatedCores"] != true {
		return map[string]any{}
	}
	return held
}

// A replacement is planned twice: as an update of the old machine, where a
// change such as os asks for it, then by Terraform as a create with a null
// prior state, whose plan is the one applied. A machine whose configuration
// leaves the core settings out (dedicated at 5.7 GHz from the console) is
// replaced as any new machine is made, shared: the create half plans false,
// 0 and false, the create sends all three (the platform would keep the old
// machine's for a setting left out), and the shared machine the platform
// stores reads back as planned, so the next plan is empty.
func TestVMReplaceCores(t *testing.T) {
	ctx := context.Background()
	b := func(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }
	n := func(v int) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	unknownBool := tftypes.NewValue(tftypes.Bool, tftypes.UnknownValue)
	unknownNum := tftypes.NewValue(tftypes.Number, tftypes.UnknownValue)
	type vals = map[string]tftypes.Value
	cores := []string{"dedicated_cores", "core_speed", "mixed_speeds"}
	held := vals{"os": s("ubuntu"), "dedicated_cores": b(true), "core_speed": n(5700), "mixed_speeds": b(false)}
	sr, oldRaw := vmPlanValue(t, held)
	sch := sr.Schema
	_, cfgRaw := vmPlanValue(t, vals{"os": s("debian")})
	_, proposedRaw := vmPlanValue(t, vals{"os": s("debian"), "dedicated_cores": unknownBool, "core_speed": unknownNum,
		"mixed_speeds": unknownBool})
	cfg := tfsdk.Config{Schema: sch, Raw: cfgRaw}
	old := tfsdk.State{Schema: sch, Raw: oldRaw}
	none := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(ctx), nil)}
	proposed := tfsdk.Plan{Schema: sch, Raw: proposedRaw}

	// The update half: os replaces the machine; the settings left out keep
	// the old machine's (that plan is not the one applied).
	if !replacing(ctx, modifyPlanReq(ctx, sch, held, vals{"os": s("debian"), "dedicated_cores": b(true),
		"core_speed": n(5700), "mixed_speeds": b(false)})) {
		t.Fatal("a change of os doesn't replace the machine")
	}
	for _, a := range cores {
		if got := planCoreChoice(t, sch, cfg, old, proposed, a); !got.Equal(held[a]) {
			t.Errorf("update half: %s planned %v, want %v", a, got, held[a])
		}
	}

	// The create half, the plan applied: the platform's default, known now.
	planned := vals{"os": s("debian")}
	for _, a := range cores {
		planned[a] = planCoreChoice(t, sch, cfg, none, proposed, a)
	}
	_, planRaw := vmPlanValue(t, planned)
	req := resource.ModifyPlanRequest{Config: cfg, Plan: tfsdk.Plan{Schema: sch, Raw: planRaw}, State: none}
	resp := &resource.ModifyPlanResponse{Plan: req.Plan}
	(&vmResource{}).ModifyPlan(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("create half: %v", resp.Diagnostics)
	}
	var m vmResourceModel
	if d := resp.Plan.Get(ctx, &m); d.HasError() {
		t.Fatalf("plan: %v", d)
	}
	if !m.DedicatedCores.Equal(types.BoolValue(false)) || !m.CoreSpeed.Equal(types.Int64Value(0)) ||
		!m.MixedSpeeds.Equal(types.BoolValue(false)) {
		t.Fatalf("create half planned %v %v %v, want false 0 false", m.DedicatedCores, m.CoreSpeed, m.MixedSpeeds)
	}

	// The create sends all three, so nothing of the old machine is kept even
	// where the platform still holds it, and what it stores reads as planned.
	sent := vmSpec(ctx, m, vmWrite{create: true})
	if sent["dedicatedCores"] != false || sent["coreSpeed"] != int64(0) || sent["mixedSpeeds"] != false {
		t.Errorf("sent %v %v %v", sent["dedicatedCores"], sent["coreSpeed"], sent["mixedSpeeds"])
	}
	oldVM := map[string]any{"dedicatedCores": true, "coreSpeed": float64(5700)}
	for name, was := range map[string]map[string]any{"old machine gone": nil, "old machine still held": oldVM} {
		var read vmResourceModel
		readVMCores(&read, platformKeepsCores(was, sent))
		if !read.DedicatedCores.Equal(m.DedicatedCores) || !read.CoreSpeed.Equal(m.CoreSpeed) ||
			!read.MixedSpeeds.Equal(m.MixedSpeeds) {
			t.Errorf("%s: reads %v %v %v, planned %v %v %v", name, read.DedicatedCores, read.CoreSpeed, read.MixedSpeeds,
				m.DedicatedCores, m.CoreSpeed, m.MixedSpeeds)
		}
	}
	// (Left out of the create, as an unknown size is, the old machine's
	// would be kept: that is why the create half plans them known.)
	if got := platformKeepsCores(oldVM, map[string]any{}); got["dedicatedCores"] != true {
		t.Errorf("the platform's carry, as modelled: %v", got)
	}
}
