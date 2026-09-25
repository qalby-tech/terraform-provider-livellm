package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func remoteList(t *testing.T, rbs ...[3]string) types.List {
	t.Helper()
	vals := make([]attr.Value, 0, len(rbs))
	for _, rb := range rbs {
		auth := types.StringNull()
		if rb[2] != "" {
			auth = types.StringValue(rb[2])
		}
		vals = append(vals, types.ObjectValueMust(remoteBrowserAttrTypes, map[string]attr.Value{
			"id": types.StringValue(rb[0]), "ws_url": types.StringValue(rb[1]), "auth_wo": auth,
			"has_auth": types.BoolNull(),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: remoteBrowserAttrTypes}, vals)
}

func stringSet(names ...string) types.Set {
	vals := make([]attr.Value, 0, len(names))
	for _, n := range names {
		vals = append(vals, types.StringValue(n))
	}
	return types.SetValueMust(types.StringType, vals)
}

func baseBrowserAPI() browserAPIModel {
	return browserAPIModel{
		Name:          types.StringValue("scrapers"),
		Browsers:      types.SetNull(types.StringType),
		AllBrowsers:   types.BoolValue(false),
		RemoteBrowser: types.ListNull(types.ObjectType{AttrTypes: remoteBrowserAttrTypes}),
		CPU:           types.StringNull(),
		Memory:        types.StringNull(),
	}
}

// The schema is one the framework accepts: write-only inside a list block.
func TestBrowserAPISchema(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewBrowserAPIResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatal(d)
	}
}

func TestBrowserAPISpec(t *testing.T) {
	ctx := context.Background()

	// Named browsers: autodiscover false is sent, not left out.
	m := baseBrowserAPI()
	m.Browsers = stringSet("agent-2", "agent-1")
	m.CPU = types.StringValue("500m")
	got := browserAPISpec(ctx, m, nil)
	want := map[string]any{
		"autodiscover":     false,
		"browsers":         []string{"agent-1", "agent-2"},
		"externalBrowsers": []map[string]any{},
		"cpu":              "500m",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("named browsers:\n got %v\nwant %v", got, want)
	}

	// Every browser: no browsers list at all.
	m = baseBrowserAPI()
	m.AllBrowsers = types.BoolValue(true)
	got = browserAPISpec(ctx, m, nil)
	if got["autodiscover"] != true || got["browsers"] != nil {
		t.Errorf("all browsers: %v", got)
	}

	// Remote browsers: the header comes from the configuration only.
	m = baseBrowserAPI()
	m.RemoteBrowser = remoteList(t, [3]string{"office", "wss://office.example.com/devtools/browser/x", ""},
		[3]string{"lab", "ws://lab.example.com:9222/devtools/browser/y", ""})
	got = browserAPISpec(ctx, m, map[string]string{"office": "Bearer abc"})
	ext := got["externalBrowsers"].([]map[string]any)
	if len(ext) != 2 || ext[0]["authHeader"] != "Bearer abc" || ext[1]["authHeader"] != nil || ext[1]["hasAuth"] != false || ext[0]["hasAuth"] != nil || ext[1]["wsUrl"] != "ws://lab.example.com:9222/devtools/browser/y" {
		t.Errorf("remotes: %v", ext)
	}
	if !reflect.DeepEqual(got["browsers"], []string{}) {
		t.Errorf("remotes only: browsers should be an empty list, got %v", got["browsers"])
	}
}

func TestRemoteAuth(t *testing.T) {
	m := baseBrowserAPI()
	m.RemoteBrowser = remoteList(t, [3]string{"office", "wss://o", "X-Token: 1"}, [3]string{"lab", "wss://l", ""})
	got := remoteAuth(context.Background(), m)
	if !reflect.DeepEqual(got, map[string]string{"office": "X-Token: 1"}) {
		t.Errorf("remoteAuth = %v", got)
	}
}

func TestBrowserAPIConfigErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		edit func(*browserAPIModel)
		want string // substring of the first error summary; "" = valid
	}{
		{"named browsers", func(m *browserAPIModel) { m.Browsers = stringSet("a") }, ""},
		{"every browser", func(m *browserAPIModel) { m.AllBrowsers = types.BoolValue(true) }, ""},
		{"remote only", func(m *browserAPIModel) {
			m.RemoteBrowser = remoteList(t, [3]string{"office", "wss://office.example.com", ""})
		}, ""},
		{"names not known yet", func(m *browserAPIModel) { m.Browsers = types.SetUnknown(types.StringType) }, ""},
		{"nothing", func(m *browserAPIModel) {}, "needs browsers"},
		{"empty list", func(m *browserAPIModel) { m.Browsers = stringSet() }, "needs browsers"},
		{"both", func(m *browserAPIModel) {
			m.AllBrowsers = types.BoolValue(true)
			m.Browsers = stringSet("a")
		}, "Conflicting"},
		{"remote named like a browser", func(m *browserAPIModel) {
			m.Browsers = stringSet("agent-1")
			m.RemoteBrowser = remoteList(t, [3]string{"agent-1", "wss://x", ""})
		}, "named like"},
		{"remote twice", func(m *browserAPIModel) {
			m.RemoteBrowser = remoteList(t, [3]string{"x", "wss://a", ""}, [3]string{"x", "wss://b", ""})
		}, "twice"},
		{"remote without address", func(m *browserAPIModel) {
			m.RemoteBrowser = remoteList(t, [3]string{"x", "", ""})
		}, "Incomplete"},
		{"remote http address", func(m *browserAPIModel) {
			m.RemoteBrowser = remoteList(t, [3]string{"x", "https://a", ""})
		}, "ws_url"},
		{"remote bad id", func(m *browserAPIModel) {
			m.RemoteBrowser = remoteList(t, [3]string{"Office", "wss://a", ""})
		}, "Bad remote_browser id"},
	}
	for _, c := range cases {
		m := baseBrowserAPI()
		c.edit(&m)
		errs := browserAPIConfigErrors(ctx, m)
		switch {
		case c.want == "" && len(errs) > 0:
			t.Errorf("%s: unexpected %v", c.name, errs)
		case c.want != "" && (len(errs) == 0 || !strings.Contains(errs[0][0], c.want)):
			t.Errorf("%s: got %v, want %q", c.name, errs, c.want)
		}
	}
}

func TestReadBrowserAPI(t *testing.T) {
	// An import: only the name is known. The header is never read back.
	prev := baseBrowserAPI()
	prev.AllBrowsers = types.BoolNull()
	sp := map[string]any{
		"browsers":         []any{"agent-2"},
		"externalBrowsers": []any{map[string]any{"id": "office", "wsUrl": "wss://o", "hasAuth": true}},
		"memory":           "1Gi",
	}
	m := readBrowserAPI(prev, sp)
	if m.AllBrowsers.ValueBool() || !m.Browsers.Equal(stringSet("agent-2")) || m.Memory.ValueString() != "1Gi" || !m.CPU.IsNull() {
		t.Errorf("read: %+v", m)
	}
	var rbs []remoteBrowserModel
	m.RemoteBrowser.ElementsAs(context.Background(), &rbs, false)
	if len(rbs) != 1 || rbs[0].ID.ValueString() != "office" || rbs[0].WsURL.ValueString() != "wss://o" || !rbs[0].AuthWO.IsNull() || !rbs[0].HasAuth.ValueBool() {
		t.Errorf("remotes read back: %+v", rbs)
	}

	// Every browser: browsers stays unset when the configuration had none.
	m = readBrowserAPI(baseBrowserAPI(), map[string]any{"autodiscover": true})
	if !m.AllBrowsers.ValueBool() || !m.Browsers.IsNull() || !m.RemoteBrowser.IsNull() {
		t.Errorf("every browser: %+v", m)
	}

	// A browser taken out in the console shows as drift.
	prev = baseBrowserAPI()
	prev.Browsers = stringSet("agent-1", "agent-2")
	m = readBrowserAPI(prev, map[string]any{"browsers": []any{"agent-1"}})
	if !m.Browsers.Equal(stringSet("agent-1")) {
		t.Errorf("drift: %v", m.Browsers)
	}
}

var _ resource.ResourceWithModifyPlan = &browserAPIResource{}

func withHasAuth(t *testing.T, l types.List, has ...bool) types.List {
	t.Helper()
	var rbs []remoteBrowserModel
	l.ElementsAs(context.Background(), &rbs, false)
	vals := make([]attr.Value, 0, len(rbs))
	for i, rb := range rbs {
		vals = append(vals, types.ObjectValueMust(remoteBrowserAttrTypes, map[string]attr.Value{
			"id": rb.ID, "ws_url": rb.WsURL, "auth_wo": rb.AuthWO, "has_auth": types.BoolValue(has[i]),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: remoteBrowserAttrTypes}, vals)
}

// has_auth is planned from the configuration, so a header stored without an
// auth_wo in the configuration is a visible change (with a warning), not a
// silent removal on some later update.
func TestPlanRemoteAuth(t *testing.T) {
	ctx := context.Background()
	cfg := baseBrowserAPI()
	cfg.RemoteBrowser = remoteList(t, [3]string{"office", "wss://o", "Bearer abc"}, [3]string{"lab", "wss://l", ""})
	plan := baseBrowserAPI()
	plan.RemoteBrowser = remoteList(t, [3]string{"office", "wss://o", ""}, [3]string{"lab", "wss://l", ""})

	has := func(m browserAPIModel) []bool {
		var rbs []remoteBrowserModel
		m.RemoteBrowser.ElementsAs(ctx, &rbs, false)
		out := []bool{}
		for _, rb := range rbs {
			if !rb.AuthWO.IsNull() {
				t.Errorf("auth_wo left in the plan for %s", rb.ID.ValueString())
			}
			out = append(out, rb.HasAuth.ValueBool())
		}
		return out
	}

	// A create: nothing stored yet, nothing removed.
	got, removed := planRemoteAuth(ctx, plan, cfg, baseBrowserAPIState())
	if !reflect.DeepEqual(has(got), []bool{true, false}) || len(removed) != 0 {
		t.Errorf("create: has %v removed %v", has(got), removed)
	}

	// lab has a header stored (the console, or an import) and none in the
	// configuration: the plan says it goes, and names it.
	state := baseBrowserAPI()
	state.RemoteBrowser = withHasAuth(t, plan.RemoteBrowser, true, true)
	got, removed = planRemoteAuth(ctx, plan, cfg, state)
	if !reflect.DeepEqual(has(got), []bool{true, false}) || !reflect.DeepEqual(removed, []string{"lab"}) {
		t.Errorf("stored without auth_wo: has %v removed %v", has(got), removed)
	}
	if got.RemoteBrowser.Equal(state.RemoteBrowser) {
		t.Error("the removal should show as a change")
	}

	// office's header was removed in the console: auth_wo plans it back.
	state.RemoteBrowser = withHasAuth(t, plan.RemoteBrowser, false, false)
	got, removed = planRemoteAuth(ctx, plan, cfg, state)
	if got.RemoteBrowser.Equal(state.RemoteBrowser) || len(removed) != 0 {
		t.Errorf("header gone in the console: has %v removed %v", has(got), removed)
	}

	// Matching: no change.
	state.RemoteBrowser = withHasAuth(t, plan.RemoteBrowser, true, false)
	got, _ = planRemoteAuth(ctx, plan, cfg, state)
	if !got.RemoteBrowser.Equal(state.RemoteBrowser) {
		t.Errorf("in step: %v vs %v", got.RemoteBrowser, state.RemoteBrowser)
	}
}
