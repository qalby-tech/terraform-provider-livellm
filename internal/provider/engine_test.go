package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A Chrome browser or Browser API sends what 0.12.0 sent: no engine key,
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
		a := baseBrowserAPI()
		a.Engine = v
		a.Browsers = stringSet("agent-1")
		got := browserAPISpec(ctx, a, nil)
		if _, ok := got["engine"]; ok {
			t.Errorf("browser api %v: engine sent: %v", v, got)
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
	a := baseBrowserAPI()
	a.Engine = types.StringValue("camoufox")
	a.AllBrowsers = types.BoolValue(true)
	if got := browserAPISpec(ctx, a, nil); got["engine"] != "camoufox" {
		t.Errorf("browser api: %v", got)
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
	a := baseBrowserAPI()
	if got := readBrowserAPI(a, map[string]any{"autodiscover": true}).Engine; !got.Equal(types.StringValue("chrome")) {
		t.Errorf("api absent: %v", got)
	}
	if got := readBrowserAPI(a, map[string]any{"engine": "camoufox"}).Engine; !got.Equal(types.StringValue("camoufox")) {
		t.Errorf("api camoufox: %v", got)
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
	for _, r := range []resource.Resource{NewBrowserResource(), NewBrowserAPIResource()} {
		var resp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &resp)
		a, ok := resp.Schema.Attributes["engine"].(schema.StringAttribute)
		if !ok || !a.Optional || !a.Computed || len(a.PlanModifiers) != 1 {
			t.Errorf("engine attribute: %#v", resp.Schema.Attributes["engine"])
		}
	}
}

// A Camoufox Browser API with remote browsers is refused at plan; a Chrome
// one, or a Camoufox one with workspace browsers, is not.
func TestBrowserAPICamoufoxRemote(t *testing.T) {
	ctx := context.Background()
	m := baseBrowserAPI()
	m.Engine = types.StringValue("camoufox")
	m.RemoteBrowser = remoteList(t, [3]string{"office", "wss://office.example.com/devtools/browser/x", ""})
	errs := browserAPIConfigErrors(ctx, m)
	if len(errs) != 1 || !strings.Contains(errs[0][0], "Chrome Browser API") {
		t.Errorf("camoufox + remote: %v", errs)
	}
	m.Engine = types.StringValue("chrome")
	if errs := browserAPIConfigErrors(ctx, m); len(errs) != 0 {
		t.Errorf("chrome + remote: %v", errs)
	}
	m.Engine = types.StringNull()
	if errs := browserAPIConfigErrors(ctx, m); len(errs) != 0 {
		t.Errorf("default + remote: %v", errs)
	}
	m = baseBrowserAPI()
	m.Engine = types.StringValue("camoufox")
	m.Browsers = stringSet("fox-1")
	if errs := browserAPIConfigErrors(ctx, m); len(errs) != 0 {
		t.Errorf("camoufox + browsers: %v", errs)
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
		got, msg := engineKept(c.planned, c.raw, "browser", "fox")
		if !got.Equal(c.want) || (msg != "") != c.err {
			t.Errorf("%v / %v: got %v %q", c.planned, c.raw, got, msg)
		}
	}
	if _, msg := engineKept(s("camoufox"), nil, "Browser API", "pool"); !strings.Contains(msg, `"pool" a Chrome Browser API`) {
		t.Errorf("message: %q", msg)
	}
}

// A platform that doesn't know engines (it drops the field and makes Chrome):
// the apply of a Camoufox browser or Browser API is an error and the state
// holds chrome, not camoufox (which would plan a replace on every run).
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
				`{"id":"scraper","type":"browser","browser":{"cpu":"2"` + e + `}},` +
				`{"id":"scrapers","type":"controller","controller":{"autodiscover":true` + e + `}}]}}`))
		case "/v1/status":
			_, _ = w.Write([]byte(`{"workloads":[{"id":"scraper","type":"browser","phase":"Running","ready":true},` +
				`{"id":"scrapers","type":"controller","phase":"Running","ready":true}]}`))
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
		d = nil
		a := baseBrowserAPI()
		a.Engine = types.StringValue("camoufox")
		(&browserAPIResource{data: data}).waitAndRefresh(ctx, &a, 5*time.Second, "not ready", &d)
		if a.Engine.ValueString() != c.want || d.HasError() != c.err {
			t.Errorf("browser api, platform holds %q: engine %v, diags %v", c.engine, a.Engine, d)
		}
	}
}
