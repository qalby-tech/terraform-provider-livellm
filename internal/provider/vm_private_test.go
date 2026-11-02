package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// private_network and private_address are optional and computed, private_mac
// computed; all three plan with keepOrPlatform, never a Default or a
// replacement.
func TestPrivateAddressAttributes(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatalf("schema: %v", d)
	}
	pn, ok := resp.Schema.Attributes["private_network"].(schema.BoolAttribute)
	if !ok || !pn.Optional || !pn.Computed || pn.Default != nil || len(pn.PlanModifiers) != 1 {
		t.Fatalf("private_network: %+v", resp.Schema.Attributes["private_network"])
	}
	if _, ok := pn.PlanModifiers[0].(keepOrPlatform); !ok {
		t.Errorf("private_network: plan modifier %T", pn.PlanModifiers[0])
	}
	for _, name := range []string{"private_address", "private_mac"} {
		a, ok := resp.Schema.Attributes[name].(schema.StringAttribute)
		if !ok || !a.Computed || a.Default != nil || len(a.PlanModifiers) != 1 {
			t.Fatalf("%s: %+v", name, resp.Schema.Attributes[name])
		}
		if _, ok := a.PlanModifiers[0].(keepOrPlatform); !ok {
			t.Errorf("%s: plan modifier %T", name, a.PlanModifiers[0])
		}
		if a.Optional != (name == "private_address") {
			t.Errorf("%s: optional %v", name, a.Optional)
		}
	}
	for _, d := range []string{resp.Schema.Attributes["private_network"].GetDescription(),
		resp.Schema.Attributes["private_address"].GetDescription()} {
		for _, word := range []string{"OVN", "operator", "chart", "Multus", " CR"} {
			if strings.Contains(d, word) {
				t.Errorf("description names %q: %s", word, d)
			}
		}
	}
}

// Left out, a new machine's is the platform's (unknown); an existing one's is
// what state holds, null included (a machine without a private address plans
// clean); a configured value stays.
func TestKeepOrPlatform(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"x": tftypes.String}}
	state := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{"x": tftypes.NewValue(tftypes.String, nil)})}
	noState := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	srun := func(st tfsdk.State, cfg, sv types.String) types.String {
		req := planmodifier.StringRequest{State: st, ConfigValue: cfg, StateValue: sv, PlanValue: types.StringUnknown()}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		keepOrPlatform{}.PlanModifyString(ctx, req, resp)
		return resp.PlanValue
	}
	if v := srun(noState, types.StringNull(), types.StringNull()); !v.IsUnknown() {
		t.Errorf("create, left out: %v", v)
	}
	if v := srun(state, types.StringNull(), types.StringNull()); !v.IsNull() {
		t.Errorf("update, none held: %v", v)
	}
	if v := srun(state, types.StringNull(), types.StringValue("172.16.0.12")); !v.Equal(types.StringValue("172.16.0.12")) {
		t.Errorf("update, held: %v", v)
	}
	req := planmodifier.StringRequest{State: state, ConfigValue: types.StringValue("172.16.0.20"),
		StateValue: types.StringValue("172.16.0.12"), PlanValue: types.StringValue("172.16.0.20")}
	resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
	keepOrPlatform{}.PlanModifyString(ctx, req, resp)
	if !resp.PlanValue.Equal(types.StringValue("172.16.0.20")) {
		t.Errorf("configured: %v", resp.PlanValue)
	}
	breq := planmodifier.BoolRequest{State: state, ConfigValue: types.BoolNull(), StateValue: types.BoolValue(false), PlanValue: types.BoolUnknown()}
	bresp := &planmodifier.BoolResponse{PlanValue: breq.PlanValue}
	keepOrPlatform{}.PlanModifyBool(ctx, breq, bresp)
	if !bresp.PlanValue.Equal(types.BoolValue(false)) {
		t.Errorf("bool, update: %v", bresp.PlanValue)
	}
	breq.State, breq.StateValue = noState, types.BoolNull()
	bresp = &planmodifier.BoolResponse{PlanValue: types.BoolUnknown()}
	keepOrPlatform{}.PlanModifyBool(ctx, breq, bresp)
	if !bresp.PlanValue.IsUnknown() {
		t.Errorf("bool, create: %v", bresp.PlanValue)
	}
}

func TestPrivateConfigErrors(t *testing.T) {
	m := func(network types.Bool, addr types.String) vmResourceModel {
		return vmResourceModel{PrivateNetwork: network, PrivateAddress: addr}
	}
	for _, c := range []struct {
		name string
		m    vmResourceModel
		want string // the summary, "" for none
	}{
		{"left out", m(types.BoolNull(), types.StringNull()), ""},
		{"an address", m(types.BoolNull(), types.StringValue("172.16.0.12")), ""},
		{"an address, network on", m(types.BoolValue(true), types.StringValue("172.16.0.12")), ""},
		{"network off", m(types.BoolValue(false), types.StringNull()), ""},
		{"not known yet", m(types.BoolValue(false), types.StringUnknown()), ""},
		{"an address, network off", m(types.BoolValue(false), types.StringValue("172.16.0.12")), "A private address needs private_network"},
		{"a range", m(types.BoolNull(), types.StringValue("172.16.0.0/16")), "Not a private address"},
		{"IPv6", m(types.BoolNull(), types.StringValue("fd00::12")), "Not a private address"},
		{"leading zeros", m(types.BoolNull(), types.StringValue("172.16.0.012")), "Not a private address"},
	} {
		errs := privateConfigErrors(c.m)
		switch {
		case c.want == "" && len(errs) > 0:
			t.Errorf("%s: %v", c.name, errs)
		case c.want != "" && (len(errs) != 1 || errs[0][1] != c.want || errs[0][0] != "private_address"):
			t.Errorf("%s: %v, want %q", c.name, errs, c.want)
		}
	}
}

func TestPrivateChange(t *testing.T) {
	m := func(network types.Bool, addr types.String) vmResourceModel {
		return vmResourceModel{Name: types.StringValue("box"), PrivateNetwork: network, PrivateAddress: addr}
	}
	b, s := types.BoolValue, types.StringValue
	has, none := m(b(true), s("172.16.0.12")), m(b(false), types.StringNull())
	for _, c := range []struct {
		name        string
		plan, state vmResourceModel
		on, detail  string // "" for kept
	}{
		{"kept", has, has, "", ""},
		{"kept, none", none, none, "", ""},
		{"not known yet", m(types.BoolUnknown(), types.StringUnknown()), has, "", ""},
		{"from an older provider", has, m(types.BoolNull(), types.StringNull()), "private_address", "leave private_address out"},
		{"network off", m(b(false), s("172.16.0.12")), has, "private_network", "keep private_network = true"},
		{"network on", m(b(true), types.StringNull()), none, "private_network", "made without a private address"},
		{"another address", m(b(true), s("172.16.0.20")), has, "private_address", `keep private_address = "172.16.0.12"`},
		{"an address added", m(b(false), s("172.16.0.20")), none, "private_address", "leave private_address out"},
	} {
		e := privateChange(c.plan, c.state)
		if c.on == "" {
			if e != nil {
				t.Errorf("%s: %v", c.name, e)
			}
			continue
		}
		if e == nil || e[0] != c.on || !strings.Contains(e[2], c.detail) || !strings.Contains(e[2], "taint") {
			t.Errorf("%s: %v, want on %s with %q", c.name, e, c.on, c.detail)
		}
	}
}

// ModifyPlan, on real plans: another private network or address on an
// existing machine is refused on its attribute, never planned as a
// replacement; one left out keeps the machine's; a machine replaced anyway (a
// new name) takes the new ones.
func TestVMModifyPlanPrivateAddress(t *testing.T) {
	ctx := context.Background()
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	b := func(v bool) tftypes.Value { return tftypes.NewValue(tftypes.Bool, v) }
	type vals = map[string]tftypes.Value
	held := vals{"private_network": b(true), "private_address": s("172.16.0.12"), "private_mac": s("02:4c:4c:3a:91:0c")}
	with := func(v vals, extra vals) vals {
		out := vals{}
		for k, x := range v {
			out[k] = x
		}
		for k, x := range extra {
			out[k] = x
		}
		return out
	}
	for _, c := range []struct {
		name                string
		config, plan, state vals
		refusedOn           string
	}{
		{name: "left out", plan: held, state: held},
		{name: "kept", config: vals{"private_address": s("172.16.0.12")}, plan: held, state: held},
		{name: "another address", config: vals{"private_address": s("172.16.0.20")},
			plan: with(held, vals{"private_address": s("172.16.0.20")}), state: held, refusedOn: "private_address"},
		{name: "network off", config: vals{"private_network": b(false)},
			plan: with(held, vals{"private_network": b(false)}), state: held, refusedOn: "private_network"},
		{name: "replaced for its name", config: vals{"name": s("box2"), "private_address": s("172.16.0.20")},
			plan: with(held, vals{"name": s("box2"), "private_address": s("172.16.0.20")}), state: held},
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
		if c.refusedOn == "" {
			if len(errs) > 0 {
				t.Errorf("%s: %v", c.name, errs)
			}
			continue
		}
		if len(errs) != 1 {
			t.Errorf("%s: errors %v, want one", c.name, resp.Diagnostics)
			continue
		}
		if wp, ok := errs[0].(diag.DiagnosticWithPath); !ok || !wp.Path().Equal(path.Root(c.refusedOn)) {
			t.Errorf("%s: not on %s: %v", c.name, c.refusedOn, errs[0])
		}
	}
}

// The create asks for what the configuration sets; left out nothing is sent;
// a later save (and the save that stops a machine born stopped) sends
// neither, nor ever the MAC or the platform's record.
func TestVMSpecPrivateAddress(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{Name: types.StringValue("box"), Username: types.StringValue("dev"),
		PrivateNetwork: types.BoolValue(true), PrivateAddress: types.StringValue("172.16.0.12"),
		PrivateMac: types.StringValue("02:4c:4c:3a:91:0c")}
	spec := vmSpec(ctx, m, vmWrite{create: true, password: "S3cure-Pass!"})
	if spec["privateNetwork"] != true || spec["privateAddress"] != "172.16.0.12" {
		t.Errorf("create: %v %v", spec["privateNetwork"], spec["privateAddress"])
	}
	off := m
	off.PrivateNetwork, off.PrivateAddress = types.BoolValue(false), types.StringNull()
	if spec := vmSpec(ctx, off, vmWrite{create: true}); spec["privateNetwork"] != false || spec["privateAddress"] != nil {
		t.Errorf("create, off: %v %v", spec["privateNetwork"], spec["privateAddress"])
	}
	left := m
	left.PrivateNetwork, left.PrivateAddress, left.PrivateMac = types.BoolUnknown(), types.StringUnknown(), types.StringUnknown()
	if spec := vmSpec(ctx, left, vmWrite{create: true}); spec["privateNetwork"] != nil || spec["privateAddress"] != nil {
		t.Errorf("create, left out: %v %v", spec["privateNetwork"], spec["privateAddress"])
	}
	for _, wr := range []vmWrite{{}, {password: "S3cure-Pass!"}} {
		spec := vmSpec(ctx, m, wr)
		for _, k := range []string{"privateNetwork", "privateAddress", "privateMac", "network"} {
			if _, sent := spec[k]; sent {
				t.Errorf("save: %s sent: %v", k, spec[k])
			}
		}
	}
	if _, sent := spec["privateMac"]; sent {
		t.Error("create: privateMac sent")
	}
}

func TestReadVMPrivate(t *testing.T) {
	has := map[string]any{"privateNetwork": true, "privateAddress": "172.16.0.12", "privateMac": "02:4c:4c:3a:91:0c",
		"network": map[string]any{"address": "172.16.0.12", "mac": "02:4c:4c:3a:91:0c", "uplinkMac": "02:4c:4c:c7:20:5e"}}
	p := readVMPrivate(has)
	if !p.said || !p.network.Equal(types.BoolValue(true)) || !p.address.Equal(types.StringValue("172.16.0.12")) ||
		!p.mac.Equal(types.StringValue("02:4c:4c:3a:91:0c")) {
		t.Errorf("has: %+v", p)
	}
	p = readVMPrivate(map[string]any{"privateNetwork": false})
	if !p.said || !p.network.Equal(types.BoolValue(false)) || !p.address.IsNull() || !p.mac.IsNull() {
		t.Errorf("none: %+v", p)
	}
	for _, sp := range []map[string]any{nil, {"cpus": float64(2)}} {
		p = readVMPrivate(sp)
		if p.said || !p.network.Equal(types.BoolValue(false)) || !p.address.IsNull() {
			t.Errorf("not said (%v): %+v", sp, p)
		}
	}
}

// A refresh takes the platform's answer, and keeps state when it says
// nothing (an older platform).
func TestRefreshVMPrivate(t *testing.T) {
	m := vmResourceModel{PrivateNetwork: types.BoolValue(true), PrivateAddress: types.StringValue("172.16.0.12"),
		PrivateMac: types.StringValue("02:4c:4c:3a:91:0c")}
	kept := m
	refreshVMPrivate(&kept, map[string]any{"cpus": float64(2)})
	if !kept.PrivateNetwork.Equal(m.PrivateNetwork) || !kept.PrivateAddress.Equal(m.PrivateAddress) || !kept.PrivateMac.Equal(m.PrivateMac) {
		t.Errorf("not said: %v %v %v", kept.PrivateNetwork, kept.PrivateAddress, kept.PrivateMac)
	}
	refreshVMPrivate(&m, map[string]any{"privateNetwork": false})
	if !m.PrivateNetwork.Equal(types.BoolValue(false)) || !m.PrivateAddress.IsNull() || !m.PrivateMac.IsNull() {
		t.Errorf("none: %+v", m)
	}
}

// After a create: unknowns read back; values the create sent and the
// platform didn't keep warn and stay as planned; kept ones say nothing.
func TestSettleVMPrivate(t *testing.T) {
	left := vmResourceModel{Name: types.StringValue("box"), PrivateNetwork: types.BoolUnknown(),
		PrivateAddress: types.StringUnknown(), PrivateMac: types.StringUnknown()}
	var diags diag.Diagnostics
	m := left
	settleVMPrivate(&m, map[string]any{"privateNetwork": true, "privateAddress": "172.16.0.10", "privateMac": "02:4c:4c:3a:91:0c"}, &diags)
	if !m.PrivateNetwork.Equal(types.BoolValue(true)) || !m.PrivateAddress.Equal(types.StringValue("172.16.0.10")) ||
		!m.PrivateMac.Equal(types.StringValue("02:4c:4c:3a:91:0c")) || diags.HasError() || diags.WarningsCount() > 0 {
		t.Errorf("read back: %+v %v", m, diags)
	}
	m = left
	settleVMPrivate(&m, nil, &diags)
	if !m.PrivateNetwork.Equal(types.BoolValue(false)) || !m.PrivateAddress.IsNull() || !m.PrivateMac.IsNull() || diags.WarningsCount() > 0 {
		t.Errorf("unread: %+v %v", m, diags)
	}
	asked := vmResourceModel{Name: types.StringValue("box"), PrivateNetwork: types.BoolValue(true),
		PrivateAddress: types.StringValue("172.16.0.12"), PrivateMac: types.StringUnknown()}
	m = asked
	settleVMPrivate(&m, map[string]any{"cpus": float64(2)}, &diags)
	if !m.PrivateNetwork.Equal(asked.PrivateNetwork) || !m.PrivateAddress.Equal(asked.PrivateAddress) || !m.PrivateMac.IsNull() {
		t.Errorf("older platform: %+v", m)
	}
	if diags.WarningsCount() != 2 || !strings.Contains(diags.Warnings()[0].Summary(), "not kept as written") {
		t.Errorf("older platform, warnings: %v", diags)
	}
}

// The platform's refusals read in its words, with what to do next, by code.
func TestPrivateRefusals(t *testing.T) {
	for code, status := range map[string]int{
		"private_network_not_offered": 422, "private_address_invalid": 422, "private_address_outside": 422,
		"private_address_reserved": 422, "private_address_taken": 409, "private_address_releasing": 409,
		"private_address_fixed": 422, "private_range_full": 409,
	} {
		r, ok := privateRefusals[code]
		if !ok {
			t.Errorf("%s: no refusal", code)
			continue
		}
		body, _ := json.Marshal(map[string]any{"error": "workload box: the platform's words", "code": code})
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", &client.APIError{Status: status, Body: string(body)})
		errs := diags.Errors()
		if len(errs) != 1 || errs[0].Summary() != r.summary ||
			!strings.Contains(errs[0].Detail(), "Cannot create VM: workload box: the platform's words\n\n"+r.next) {
			t.Errorf("%s: %v", code, errs)
		}
	}
}

// A refresh (and an import) reads the machine's private address.
func TestVMReadPrivateAddress(t *testing.T) {
	ctx := context.Background()
	w := client.Workload{ID: "box", Type: "vm-ubuntu", VM: map[string]any{"cpus": float64(2), "privateNetwork": true,
		"privateAddress": "172.16.0.12", "privateMac": "02:4c:4c:3a:91:0c"}}
	r := NewVMResource()
	r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
		ProviderData: &providerData{Client: coresPlatform(t, w, `{"id":"box","type":"vm-ubuntu","phase":"Running","ready":true}`)},
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
	if !m.PrivateNetwork.Equal(types.BoolValue(true)) || !m.PrivateAddress.Equal(types.StringValue("172.16.0.12")) ||
		!m.PrivateMac.Equal(types.StringValue("02:4c:4c:3a:91:0c")) {
		t.Errorf("read: %v %v %v", m.PrivateNetwork, m.PrivateAddress, m.PrivateMac)
	}
}

// The livellm_vm and livellm_vms data sources read the private address from
// the workspace's settings, through their own Read.
func TestVMDataSourcesPrivateAddress(t *testing.T) {
	ctx := context.Background()
	w := client.Workload{ID: "box", Type: "vm-ubuntu", VM: map[string]any{"privateNetwork": true,
		"privateAddress": "172.16.0.12", "privateMac": "02:4c:4c:3a:91:0c"}}
	c := coresPlatform(t, w, `{"id":"box","type":"vm-ubuntu","phase":"Running","ready":true}`)

	one := NewVMDataSource()
	one.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{ProviderData: &providerData{Client: c}},
		&datasource.ConfigureResponse{})
	var sr datasource.SchemaResponse
	one.Schema(ctx, datasource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name, at := range typ.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["id"] = tftypes.NewValue(tftypes.String, "box")
	cfg := tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, vals)}
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(typ, nil)}}
	one.Read(ctx, datasource.ReadRequest{Config: cfg}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("livellm_vm: %v", resp.Diagnostics)
	}
	var m vmModel
	resp.State.Get(ctx, &m)
	if !m.PrivateNetwork.Equal(types.BoolValue(true)) || !m.PrivateAddress.Equal(types.StringValue("172.16.0.12")) ||
		!m.PrivateMac.Equal(types.StringValue("02:4c:4c:3a:91:0c")) {
		t.Errorf("livellm_vm: %v %v %v", m.PrivateNetwork, m.PrivateAddress, m.PrivateMac)
	}

	all := NewVMsDataSource()
	all.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{ProviderData: &providerData{Client: c}},
		&datasource.ConfigureResponse{})
	var asr datasource.SchemaResponse
	all.Schema(ctx, datasource.SchemaRequest{}, &asr)
	aresp := datasource.ReadResponse{State: tfsdk.State{Schema: asr.Schema, Raw: tftypes.NewValue(asr.Schema.Type().TerraformType(ctx), nil)}}
	all.Read(ctx, datasource.ReadRequest{}, &aresp)
	if aresp.Diagnostics.HasError() {
		t.Fatalf("livellm_vms: %v", aresp.Diagnostics)
	}
	var list vmsModel
	aresp.State.Get(ctx, &list)
	var rows []vmModel
	list.VMs.ElementsAs(ctx, &rows, false)
	if len(rows) != 1 || !rows[0].PrivateAddress.Equal(types.StringValue("172.16.0.12")) {
		t.Errorf("livellm_vms: %+v", rows)
	}

	// A machine the settings don't list (an older platform's read) has none.
	m, d := vmModelFrom(ctx, client.WorkloadStatus{ID: "box", Type: "vm-ubuntu"}, nil)
	if d.HasError() || !m.PrivateNetwork.Equal(types.BoolValue(false)) || !m.PrivateAddress.IsNull() || !m.PrivateMac.IsNull() {
		t.Errorf("unlisted: %v %v %v", m.PrivateNetwork, m.PrivateAddress, m.PrivateMac)
	}
}

// The livellm_workspace data source reads the private network, and null where
// the platform offers none (null or left out).
func TestWorkspaceDataSourcePrivateNetwork(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name, body      string
		range_, gateway string // "" for null
	}{
		{"offered", `{"name":"acme","plan":"free","owner":"u1","privateNetwork":{"range":"172.16.0.0/16","gateway":"172.16.0.1"}}`,
			"172.16.0.0/16", "172.16.0.1"},
		{"none", `{"name":"acme","plan":"free","owner":"u1","privateNetwork":null}`, "", ""},
		{"an older platform", `{"name":"acme","plan":"free","owner":"u1"}`, "", ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = rw.Write([]byte(c.body))
		}))
		d := NewWorkspaceDataSource()
		d.(datasource.DataSourceWithConfigure).Configure(ctx,
			datasource.ConfigureRequest{ProviderData: &providerData{Client: client.New(srv.URL, "llc_test")}}, &datasource.ConfigureResponse{})
		var sr datasource.SchemaResponse
		d.Schema(ctx, datasource.SchemaRequest{}, &sr)
		resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
		d.Read(ctx, datasource.ReadRequest{}, &resp)
		srv.Close()
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", c.name, resp.Diagnostics)
		}
		var m workspaceModel
		resp.State.Get(ctx, &m)
		if c.range_ == "" {
			if !m.PrivateNetwork.IsNull() {
				t.Errorf("%s: %v", c.name, m.PrivateNetwork)
			}
			continue
		}
		attrs := m.PrivateNetwork.Attributes()
		if !attrs["range"].Equal(types.StringValue(c.range_)) || !attrs["gateway"].Equal(types.StringValue(c.gateway)) {
			t.Errorf("%s: %v", c.name, m.PrivateNetwork)
		}
	}
}
