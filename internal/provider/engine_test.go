package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A Chrome browser sends what 0.12.0 sent: no engine key,
// whether the engine is planned "chrome" or null (a state 0.12.0 wrote).
func TestEngineSpecChromeSendsNothing(t *testing.T) {
	ctx := context.Background()
	for _, v := range []types.String{types.StringValue("chrome"), types.StringNull()} {
		b := baseBrowser()
		b.Engine = v
		b.CPU = types.StringValue("1")
		if got, want := browserSpec(ctx, b, b, nil), map[string]any{"cpu": "1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("browser create %v: got %v want %v", v, got, want)
		}
		st := b
		if got, want := browserSpec(ctx, b, b, &st), map[string]any{"cpu": "1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("browser update %v: got %v want %v", v, got, want)
		}
	}
}

func TestEngineSpecCamoufox(t *testing.T) {
	ctx := context.Background()
	b := baseBrowser()
	b.Engine = types.StringValue("camoufox")
	if got := browserSpec(ctx, b, b, nil); got["engine"] != "camoufox" {
		t.Errorf("browser: %v", got)
	}
	st := b
	if got := browserSpec(ctx, b, b, &st); got["engine"] != "camoufox" {
		t.Errorf("browser update: %v", got)
	}
}

// Read maps an absent engine to chrome, whatever the state held.
func TestReadEngine(t *testing.T) {
	ctx := context.Background()
	b := baseBrowser()
	if got := readBrowser(ctx, b, map[string]any{}, false).Engine; !got.Equal(types.StringValue("chrome")) {
		t.Errorf("absent: %v", got)
	}
	if got := readBrowser(ctx, b, map[string]any{"engine": "camoufox"}, false).Engine; !got.Equal(types.StringValue("camoufox")) {
		t.Errorf("camoufox: %v", got)
	}
}

func TestEngineChanged(t *testing.T) {
	s, n, u := types.StringValue, types.StringNull(), types.StringUnknown()
	for _, c := range []struct {
		state, plan types.String
		want        bool
	}{
		{n, s("chrome"), false}, // a 0.12.0 state: no change
		{n, n, false},
		{s("chrome"), s("chrome"), false},
		{s("camoufox"), s("camoufox"), false},
		{s("chrome"), s("camoufox"), true},
		{s("camoufox"), s("chrome"), true},
		{n, s("camoufox"), false}, // a 0.12.0 state holds no known engine: the platform checks
		{n, u, false},
		{s("chrome"), u, true},
		{u, s("chrome"), false},
	} {
		if got := engineChanged(c.state, c.plan); got != c.want {
			t.Errorf("%v -> %v: got %v want %v", c.state, c.plan, got, c.want)
		}
	}
}

// The plan modifier: no replace on a create, nor from a null state to chrome.
func TestEngineReplaceModifier(t *testing.T) {
	ctx := context.Background()
	sch := schema.Schema{Attributes: map[string]schema.Attribute{"engine": schema.StringAttribute{Optional: true, Computed: true}}}
	typ := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"engine": tftypes.String}}
	obj := func(v any) tftypes.Value {
		return tftypes.NewValue(typ, map[string]tftypes.Value{"engine": tftypes.NewValue(tftypes.String, v)})
	}
	run := func(state tftypes.Value, sv, cv, pv types.String) planmodifier.StringResponse {
		req := planmodifier.StringRequest{
			State:       tfsdk.State{Schema: sch, Raw: state},
			Plan:        tfsdk.Plan{Schema: sch, Raw: obj(pv.ValueString())},
			StateValue:  sv,
			ConfigValue: cv,
			PlanValue:   pv,
		}
		resp := planmodifier.StringResponse{PlanValue: pv}
		engineReplace{}.PlanModifyString(ctx, req, &resp)
		return resp
	}
	n, s := types.StringNull(), types.StringValue
	if r := run(tftypes.NewValue(typ, nil), n, s("camoufox"), s("camoufox")); r.RequiresReplace || !r.PlanValue.Equal(s("camoufox")) {
		t.Error("create must not replace")
	}
	u := types.StringUnknown()
	if r := run(tftypes.NewValue(typ, nil), n, n, u); r.RequiresReplace || !r.PlanValue.Equal(s("chrome")) {
		t.Errorf("create with engine left out must plan chrome: %v", r.PlanValue)
	}
	// a 0.12.0 state, engine left out, nothing else changes: stays null
	if r := run(obj(nil), n, n, n); r.RequiresReplace || !r.PlanValue.IsNull() {
		t.Errorf("null state, no engine configured: %v %v", r.RequiresReplace, r.PlanValue)
	}
	// ... and when something else changes (the framework planned it unknown): chrome, no replace
	if r := run(obj(nil), n, n, u); r.RequiresReplace || !r.PlanValue.Equal(s("chrome")) {
		t.Errorf("null state, other change: %v %v", r.RequiresReplace, r.PlanValue)
	}
	if r := run(obj("chrome"), s("chrome"), n, u); r.RequiresReplace || !r.PlanValue.Equal(s("chrome")) {
		t.Errorf("chrome state, other change: %v %v", r.RequiresReplace, r.PlanValue)
	}
	if r := run(obj(nil), n, s("chrome"), s("chrome")); r.RequiresReplace {
		t.Error("null state -> chrome must not replace")
	}
	// a null state holds no known engine: camoufox over it is an update the
	// platform keeps (a Camoufox browser) or refuses (a Chrome one), never a
	// replace that would wipe the profile
	if r := run(obj(nil), n, s("camoufox"), s("camoufox")); r.RequiresReplace || !r.PlanValue.Equal(s("camoufox")) {
		t.Errorf("null state -> camoufox must not replace: %v %v", r.RequiresReplace, r.PlanValue)
	}
	if r := run(obj("chrome"), s("chrome"), s("camoufox"), s("camoufox")); !r.RequiresReplace {
		t.Error("chrome -> camoufox must replace")
	}
	if r := run(obj("camoufox"), s("camoufox"), n, s("camoufox")); !r.RequiresReplace || !r.PlanValue.Equal(s("chrome")) {
		t.Error("camoufox, engine left out (chrome) must replace")
	}
}

func TestEngineSchema(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewBrowserResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	a, ok := resp.Schema.Attributes["engine"].(schema.StringAttribute)
	if !ok || !a.Optional || !a.Computed || len(a.PlanModifiers) != 1 {
		t.Errorf("engine attribute: %#v", resp.Schema.Attributes["engine"])
	}
}

// A Browser API holds browsers of either engine and has no engine of its own:
// an engine argument on livellm_browser_api fails at plan as an unexpected
// argument, and no body it sends carries one.
func TestBrowserAPIHasNoEngine(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewBrowserAPIResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if a, ok := resp.Schema.Attributes["engine"]; ok {
		t.Errorf("livellm_browser_api has an engine attribute: %#v", a)
	}
	if _, ok := reflect.TypeOf(browserAPIModel{}).FieldByName("Engine"); ok {
		t.Error("browserAPIModel has an Engine field")
	}
	all := resp.Schema.Attributes["all_browsers"].(schema.BoolAttribute).Description
	if !strings.Contains(all, "every browser in the workspace") || strings.Contains(all, "of its engine") {
		t.Errorf("all_browsers description: %q", all)
	}
	m := baseBrowserAPI()
	m.AllBrowsers = types.BoolValue(true)
	m.RemoteBrowser = remoteList(t, [3]string{"office", "wss://office.example.com/devtools/browser/x", ""})
	if errs := browserAPIConfigErrors(ctx, m); len(errs) != 0 {
		t.Errorf("all browsers + remote: %v", errs)
	}
	if got := browserAPISpec(ctx, m, nil); got["engine"] != nil {
		t.Errorf("engine sent: %v", got)
	}
	m.Browsers = stringSet("agent-1")
	errs := browserAPIConfigErrors(ctx, m)
	if len(errs) != 1 || !strings.Contains(errs[0][1], "every browser in the workspace;") {
		t.Errorf("conflict text: %v", errs)
	}
}

// The engine read back after an apply: a Camoufox engine the platform didn't
// keep is an error and the state holds what it made; anything else is the plan.
func TestEngineKept(t *testing.T) {
	s, n := types.StringValue, types.StringNull()
	for _, c := range []struct {
		planned types.String
		raw     any
		want    types.String
		err     bool
	}{
		{s("camoufox"), "camoufox", s("camoufox"), false},
		{s("camoufox"), nil, s("chrome"), true}, // a platform that drops the field
		{s("camoufox"), "chrome", s("chrome"), true},
		{s("chrome"), nil, s("chrome"), false},
		{n, nil, n, false},
		{s("chrome"), "camoufox", s("chrome"), false}, // not sent: the plan stays
	} {
		got, msg := engineKept(c.planned, c.raw, "fox")
		if !got.Equal(c.want) || (msg != "") != c.err {
			t.Errorf("%v / %v: got %v %q", c.planned, c.raw, got, msg)
		}
	}
	if _, msg := engineKept(s("camoufox"), nil, "fox"); !strings.Contains(msg, `"fox" a Chrome browser`) {
		t.Errorf("message: %q", msg)
	}
}

// A platform that doesn't know engines (it drops the field and makes Chrome):
// the apply of a Camoufox browser is an error and the state holds chrome, not
// camoufox (which would plan a replace on every run).
func TestEngineNotKeptAfterApply(t *testing.T) {
	engine := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := ""
		if engine != "" {
			e = `,"engine":"` + engine + `"`
		}
		switch r.URL.Path {
		case "/v1/workspace":
			_, _ = w.Write([]byte(`{"spec":{"workloads":[` +
				`{"id":"scraper","type":"browser","browser":{"cpu":"2"` + e + `}}]}}`))
		case "/v1/status":
			_, _ = w.Write([]byte(`{"workloads":[{"id":"scraper","type":"browser","phase":"Running","ready":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	data := &providerData{Client: client.New(srv.URL, "llc_test")}
	ctx := context.Background()
	for _, c := range []struct {
		engine string
		want   string
		err    bool
	}{{"", "chrome", true}, {"camoufox", "camoufox", false}} {
		engine = c.engine
		var d diag.Diagnostics
		b := baseBrowser()
		b.Engine = types.StringValue("camoufox")
		got := (&browserResource{data: data}).applied(ctx, b, &d)
		if got.Engine.ValueString() != c.want || d.HasError() != c.err {
			t.Errorf("browser, platform holds %q: engine %v, diags %v", c.engine, got.Engine, d)
		}
	}
}
