package provider

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

const (
	secretUser = "user-5f1c"
	secretPass = "pass-9a7e"
	secretCIP  = "https://changeip.example.com/rot?key=k-3b2d"
)

func baseBrowser() browserModel {
	return browserModel{
		Name:        types.StringValue("scraper"),
		CPU:         types.StringNull(),
		Memory:      types.StringNull(),
		Locale:      types.StringNull(),
		Timezone:    types.StringNull(),
		Languages:   types.ListNull(types.StringType),
		Geolocation: types.ObjectNull(geoAttrTypes),
		Proxy:       types.ObjectNull(proxyAttrTypes),
	}
}

// up is an upstream as the configuration writes it; login and cip set the
// write-only values.
func up(name, server string, login, cip bool) upstreamModel {
	u := upstreamModel{
		Name: types.StringValue(name), Server: types.StringValue(server),
		UsernameWO: types.StringNull(), PasswordWO: types.StringNull(), ChangeIPURLWO: types.StringNull(),
		ChangeIPMethod: types.StringNull(), MinChangeIPSeconds: types.Int64Null(),
		HasAuth: types.BoolNull(), HasChangeIP: types.BoolNull(),
	}
	if login {
		u.UsernameWO, u.PasswordWO = types.StringValue(secretUser), types.StringValue(secretPass)
	}
	if cip {
		u.ChangeIPURLWO = types.StringValue(secretCIP)
	}
	return u
}

func proxyOf(authVersion types.Int64, rotation types.Object, ups ...upstreamModel) types.Object {
	vals := make([]attr.Value, 0, len(ups))
	for _, u := range ups {
		vals = append(vals, upstreamObject(u))
	}
	list := types.ListValueMust(upstreamObjType, vals)
	if len(ups) == 0 {
		list = types.ListNull(upstreamObjType)
	}
	if rotation.IsNull() && len(rotation.AttributeTypes(context.Background())) == 0 {
		rotation = types.ObjectNull(rotationAttrTypes)
	}
	return proxyObject(proxyModel{Upstream: list, Rotation: rotation, AuthVersion: authVersion, CheckURL: types.StringNull()})
}

// stored is what an apply leaves in state for the configuration m: the plan
// with has_* filled and no write-only value.
func stored(t *testing.T, m browserModel, prev *browserModel) browserModel {
	t.Helper()
	p, _ := planBrowser(context.Background(), m, m, prev)
	p.Proxy = withoutSecrets(context.Background(), p.Proxy)
	return p
}

func specJSON(t *testing.T, spec map[string]any) string {
	t.Helper()
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBrowserSchema(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewBrowserResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
		t.Fatal(d)
	}
}

// A browser without the newer settings sends exactly what 0.11.0 sent, on
// create and on update: nothing an older API would refuse.
func TestBrowserSpecUnchangedWithoutNewSettings(t *testing.T) {
	ctx := context.Background()
	m := baseBrowser()
	m.CPU = types.StringValue("1")
	want := map[string]any{"cpu": "1"}
	if got := browserSpec(ctx, m, m, nil); !reflect.DeepEqual(got, want) {
		t.Errorf("create: %v", got)
	}
	st := stored(t, m, nil)
	if got := browserSpec(ctx, m, m, &st); !reflect.DeepEqual(got, want) {
		t.Errorf("update: %v", got)
	}
}

func TestBrowserSpecLocale(t *testing.T) {
	ctx := context.Background()
	m := baseBrowser()
	m.Locale, m.Timezone = types.StringValue("ru-RU"), types.StringValue("Europe/Moscow")
	m.Geolocation = types.ObjectValueMust(geoAttrTypes, map[string]attr.Value{
		"mode": types.StringValue("fixed"), "latitude": types.Float64Value(55.75), "longitude": types.Float64Value(37.62),
		"accuracy": types.Int64Null(),
	})
	got := browserSpec(ctx, m, m, nil)
	want := map[string]any{
		"locale": "ru-RU", "timezone": "Europe/Moscow", "languages": []string{},
		"geolocation": map[string]any{"mode": "fixed", "latitude": 55.75, "longitude": 37.62},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("create:\n got %v\nwant %v", got, want)
	}

	// Same locale on an update: languages are left out (kept).
	st := m
	st.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("ru-RU"), types.StringValue("ru")})
	if got := browserSpec(ctx, m, m, &st); got["languages"] != nil {
		t.Errorf("same locale sent languages %v", got["languages"])
	}
	// A new locale: worked out again.
	m2 := m
	m2.Locale = types.StringValue("de-DE")
	if got := browserSpec(ctx, m2, m2, &st); !reflect.DeepEqual(got["languages"], []string{}) {
		t.Errorf("new locale: languages %v", got["languages"])
	}
	// Languages from the configuration go as written.
	m3 := m
	m3.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("ru-RU"), types.StringValue("en")})
	if got := browserSpec(ctx, m3, m3, &st); !reflect.DeepEqual(got["languages"], []string{"ru-RU", "en"}) {
		t.Errorf("configured languages: %v", got["languages"])
	}

	// Everything removed: the clear values, and only because state had them.
	cleared := baseBrowser()
	got = browserSpec(ctx, cleared, cleared, &st)
	want = map[string]any{"locale": "", "timezone": "", "languages": []string{}, "geolocation": map[string]any{"mode": "prompt"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleared:\n got %v\nwant %v", got, want)
	}
}

func TestProxySpecSendsSecretsOnlyWhenNeeded(t *testing.T) {
	ctx := context.Background()
	v1 := types.Int64Value(1)
	cfg := baseBrowser()
	cfg.Proxy = proxyOf(v1, types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", false, false),
		up("b", "socks5://proxy-b.example.com:1080", true, true))

	sent := func(spec map[string]any) bool {
		s := specJSON(t, spec)
		return strings.Contains(s, secretUser) || strings.Contains(s, secretPass) || strings.Contains(s, "k-3b2d")
	}
	upstream := func(spec map[string]any, i int) map[string]any {
		return spec["proxy"].(map[string]any)["upstreams"].([]map[string]any)[i]
	}

	// Create: sent.
	spec := browserSpec(ctx, cfg, cfg, nil)
	b := upstream(spec, 1)
	if b["username"] != secretUser || b["password"] != secretPass || b["changeIpUrl"] != secretCIP {
		t.Errorf("create didn't send b's values: %v", b)
	}
	if a := upstream(spec, 0); a["hasAuth"] != false || a["hasChangeIp"] != false || a["username"] != nil {
		t.Errorf("a has no login: %v", a)
	}

	st := stored(t, cfg, nil)
	// An update that changes cpu only: kept, not sent.
	upd := cfg
	upd.CPU = types.StringValue("2")
	spec = browserSpec(ctx, upd, upd, &st)
	if sent(spec) {
		t.Errorf("cpu-only update sent a secret: %s", specJSON(t, spec))
	}
	if b := upstream(spec, 1); b["hasAuth"] != true || b["hasChangeIp"] != true {
		t.Errorf("cpu-only update: b should keep its values: %v", b)
	}

	// auth_version bumped: sent again.
	bump := cfg
	bump.Proxy = proxyOf(types.Int64Value(2), types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", false, false),
		up("b", "socks5://proxy-b.example.com:1080", true, true))
	if spec = browserSpec(ctx, bump, bump, &st); !sent(spec) {
		t.Errorf("auth_version bump didn't send: %s", specJSON(t, spec))
	}

	// b moved to another host: sent (the platform won't carry a login there).
	moved := cfg
	moved.Proxy = proxyOf(v1, types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", false, false),
		up("b", "socks5://proxy-c.example.com:1080", true, true))
	if spec = browserSpec(ctx, moved, moved, &st); upstream(spec, 1)["username"] != secretUser {
		t.Errorf("moved server didn't send the login: %v", upstream(spec, 1))
	}

	// a gains a login it never had: sent for a only.
	gain := cfg
	gain.Proxy = proxyOf(v1, types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", true, false),
		up("b", "socks5://proxy-b.example.com:1080", true, true))
	spec = browserSpec(ctx, gain, gain, &st)
	if upstream(spec, 0)["username"] != secretUser || upstream(spec, 1)["username"] != nil {
		t.Errorf("gained login: a %v b %v", upstream(spec, 0), upstream(spec, 1))
	}

	// A new upstream name: its values are sent.
	added := cfg
	added.Proxy = proxyOf(v1, types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", false, false),
		up("b", "socks5://proxy-b.example.com:1080", true, true),
		up("c", "http://proxy-c.example.com:8080", true, false))
	spec = browserSpec(ctx, added, added, &st)
	if upstream(spec, 2)["username"] != secretUser || upstream(spec, 1)["username"] != nil {
		t.Errorf("new upstream: b %v c %v", upstream(spec, 1), upstream(spec, 2))
	}

	// b's login left out of the configuration: removed.
	drop := cfg
	drop.Proxy = proxyOf(v1, types.ObjectNull(rotationAttrTypes),
		up("a", "http://proxy-a.example.com:3128", false, false),
		up("b", "socks5://proxy-b.example.com:1080", false, true))
	spec = browserSpec(ctx, drop, drop, &st)
	if upstream(spec, 1)["hasAuth"] != false || upstream(spec, 1)["hasChangeIp"] != true {
		t.Errorf("dropped login: %v", upstream(spec, 1))
	}

	// The proxy block removed: remove.
	none := baseBrowser()
	if spec = browserSpec(ctx, none, none, &st); !reflect.DeepEqual(spec["proxy"], map[string]any{"remove": true}) {
		t.Errorf("removed block: %v", spec["proxy"])
	}
	// No upstream at all: direct, an empty list.
	direct := baseBrowser()
	direct.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes))
	if spec = browserSpec(ctx, direct, direct, &st); !reflect.DeepEqual(spec["proxy"], map[string]any{"upstreams": []map[string]any{}}) {
		t.Errorf("direct: %v", spec["proxy"])
	}
}

func TestPlanBrowserProxyFlags(t *testing.T) {
	ctx := context.Background()
	cfg := baseBrowser()
	cfg.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes),
		up("a", "http://a.example.com:3128", false, false),
		up("b", "http://b.example.com:3128", true, true))
	plan, removed := planBrowser(ctx, cfg, cfg, nil)
	if len(removed) != 0 {
		t.Errorf("create removes %v", removed)
	}
	ups := plan.proxy(ctx).upstreams(ctx)
	if ups[0].HasAuth.ValueBool() || ups[0].HasChangeIP.ValueBool() || !ups[1].HasAuth.ValueBool() || !ups[1].HasChangeIP.ValueBool() {
		t.Errorf("flags: %+v", ups)
	}
	for _, u := range ups {
		if !u.UsernameWO.IsNull() || !u.PasswordWO.IsNull() || !u.ChangeIPURLWO.IsNull() {
			t.Errorf("write-only value in the plan: %+v", u)
		}
	}
	// Changing password_wo alone plans no change: the plan equals the state.
	st := stored(t, cfg, nil)
	again, _ := planBrowser(ctx, cfg, cfg, &st)
	if !again.Proxy.Equal(st.Proxy) {
		t.Errorf("an unchanged configuration plans a change:\n plan  %v\n state %v", again.Proxy, st.Proxy)
	}

	// A login stored but not in the configuration: planned false, with a warning.
	cfg2 := baseBrowser()
	cfg2.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes),
		up("a", "http://a.example.com:3128", false, false),
		up("b", "http://b.example.com:3128", false, false))
	plan2, removed := planBrowser(ctx, cfg2, cfg2, &st)
	if len(removed) != 2 || plan2.proxy(ctx).upstreams(ctx)[1].HasAuth.ValueBool() {
		t.Errorf("removed %v, plan %+v", removed, plan2.proxy(ctx).upstreams(ctx)[1])
	}
}

func TestPlanBrowserLanguages(t *testing.T) {
	ctx := context.Background()
	ruLangs := types.ListValueMust(types.StringType, []attr.Value{
		types.StringValue("ru-RU"), types.StringValue("ru"), types.StringValue("en-US"), types.StringValue("en")})
	st := baseBrowser()
	st.Locale, st.Languages = types.StringValue("ru-RU"), ruLangs

	m := baseBrowser()
	m.Locale = types.StringValue("ru-RU")
	if p, _ := planBrowser(ctx, m, m, &st); !p.Languages.Equal(ruLangs) {
		t.Errorf("same locale: %v", p.Languages)
	}
	m.Locale = types.StringValue("de-DE")
	if p, _ := planBrowser(ctx, m, m, &st); !p.Languages.IsUnknown() {
		t.Errorf("new locale: %v", p.Languages)
	}
	if p, _ := planBrowser(ctx, m, m, nil); !p.Languages.IsUnknown() {
		t.Errorf("create with locale: %v", p.Languages)
	}
	none := baseBrowser()
	if p, _ := planBrowser(ctx, none, none, &st); !p.Languages.IsNull() {
		t.Errorf("no locale: %v", p.Languages)
	}
	given := m
	given.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("de-DE")})
	if p, _ := planBrowser(ctx, given, given, &st); !p.Languages.Equal(given.Languages) {
		t.Errorf("configured: %v", p.Languages)
	}
}

// What the platform answers, defaults filled in, reads back as the state the
// apply left: a second plan is clean.
func TestReadBrowserRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := baseBrowser()
	cfg.Locale, cfg.Timezone = types.StringValue("ru-RU"), types.StringValue("Europe/Moscow")
	cfg.Geolocation = types.ObjectValueMust(geoAttrTypes, map[string]attr.Value{
		"mode": types.StringValue("fixed"), "latitude": types.Float64Value(55.75), "longitude": types.Float64Value(37.62),
		"accuracy": types.Int64Null(),
	})
	cfg.Proxy = proxyOf(types.Int64Value(3), types.ObjectValueMust(rotationAttrTypes, map[string]attr.Value{
		"mode": types.StringValue("session"), "every_minutes": types.Int64Null(), "order": types.StringNull(),
	}), up("a", "http://a.example.com:3128", false, false), up("b", "socks5://b.example.com:1080", true, true))
	st := stored(t, cfg, nil)
	st.Languages = types.ListValueMust(types.StringType, []attr.Value{
		types.StringValue("ru-RU"), types.StringValue("ru"), types.StringValue("en-US"), types.StringValue("en")})
	st.ProfilesReady = types.BoolValue(true)

	var api map[string]any
	json.Unmarshal([]byte(`{
		"locale": "ru-RU", "timezone": "Europe/Moscow", "languages": ["ru-RU","ru","en-US","en"],
		"geolocation": {"mode": "fixed", "latitude": 55.75, "longitude": 37.62, "accuracy": 100},
		"profilesReady": true,
		"proxy": {
			"upstreams": [
				{"name": "a", "server": "http://a.example.com:3128", "hasAuth": false, "hasChangeIp": false, "changeIpMethod": "GET", "minChangeIpSeconds": 60},
				{"name": "b", "server": "socks5://b.example.com:1080", "hasAuth": true, "hasChangeIp": true, "changeIpMethod": "GET", "minChangeIpSeconds": 60}
			],
			"rotation": {"mode": "session", "order": "sequential"}
		}
	}`), &api)
	got := readBrowser(ctx, st, api, false)
	for name, pair := range map[string][2]attr.Value{
		"locale": {got.Locale, st.Locale}, "timezone": {got.Timezone, st.Timezone}, "languages": {got.Languages, st.Languages},
		"geolocation": {got.Geolocation, st.Geolocation}, "proxy": {got.Proxy, st.Proxy}, "profiles_ready": {got.ProfilesReady, st.ProfilesReady},
	} {
		if !pair[0].Equal(pair[1]) {
			t.Errorf("%s reads back as\n %v\nwant\n %v", name, pair[0], pair[1])
		}
	}

	// A console change shows: b's login gone, a new method on a.
	api = nil
	json.Unmarshal([]byte(`{"proxy": {"upstreams": [
		{"name": "a", "server": "http://a.example.com:3128", "changeIpMethod": "POST"},
		{"name": "b", "server": "socks5://b.example.com:1080", "hasAuth": false, "hasChangeIp": true}]}}`), &api)
	got = readBrowser(ctx, st, api, false)
	ups := got.proxy(ctx).upstreams(ctx)
	if ups[0].ChangeIPMethod.ValueString() != "POST" || ups[1].HasAuth.ValueBool() || !got.Locale.IsNull() {
		t.Errorf("console change not read: %+v locale %v", ups, got.Locale)
	}
	if got.proxy(ctx).AuthVersion.ValueInt64() != 3 {
		t.Errorf("auth_version is the configuration's: %v", got.proxy(ctx).AuthVersion)
	}

	// Import: nothing in state, the platform holds a proxy with defaults only.
	api = nil
	json.Unmarshal([]byte(`{"proxy": {"upstreams": [], "rotation": {"mode": "off", "order": "sequential"}}}`), &api)
	imp := readBrowser(ctx, baseBrowser(), api, true)
	p := imp.proxy(ctx)
	if p == nil || !p.Upstream.IsNull() || !p.Rotation.IsNull() || !p.CheckURL.IsNull() {
		t.Errorf("import: %v", imp.Proxy)
	}
	if imp = readBrowser(ctx, baseBrowser(), map[string]any{}, true); !imp.Proxy.IsNull() || !imp.Geolocation.IsNull() {
		t.Errorf("nothing held: %v %v", imp.Proxy, imp.Geolocation)
	}
}

func TestWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	o := proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("b", "http://b.example.com:3128", true, true))
	got := withoutSecrets(ctx, o)
	if s := got.String(); strings.Contains(s, secretPass) || strings.Contains(s, secretUser) || strings.Contains(s, "k-3b2d") {
		t.Errorf("a secret survived: %s", s)
	}
}

func TestBrowserConfigErrors(t *testing.T) {
	ctx := context.Background()
	geo := func(mode string, lat, lon bool) types.Object {
		v := map[string]attr.Value{"mode": types.StringValue(mode), "latitude": types.Float64Null(), "longitude": types.Float64Null(), "accuracy": types.Int64Null()}
		if mode == "" {
			v["mode"] = types.StringNull()
		}
		if lat {
			v["latitude"] = types.Float64Value(1)
		}
		if lon {
			v["longitude"] = types.Float64Value(1)
		}
		return types.ObjectValueMust(geoAttrTypes, v)
	}
	rot := func(mode string, every int64) types.Object {
		e := types.Int64Null()
		if every > 0 {
			e = types.Int64Value(every)
		}
		return types.ObjectValueMust(rotationAttrTypes, map[string]attr.Value{"mode": types.StringValue(mode), "every_minutes": e, "order": types.StringNull()})
	}
	halfLogin := up("a", "http://a.example.com:3128", false, false)
	halfLogin.UsernameWO = types.StringValue("u")
	cases := []struct {
		name string
		edit func(*browserModel)
		want string
	}{
		{"plain", func(m *browserModel) {}, ""},
		{"locale and its languages", func(m *browserModel) {
			m.Locale = types.StringValue("ru-RU")
			m.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("ru-RU"), types.StringValue("en")})
		}, ""},
		{"languages not led by locale", func(m *browserModel) {
			m.Locale = types.StringValue("ru-RU")
			m.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("en")})
		}, "languages must start with locale"},
		{"geo without mode", func(m *browserModel) { m.Geolocation = geo("", false, false) }, "needs a mode"},
		{"geo fixed without place", func(m *browserModel) { m.Geolocation = geo("fixed", true, false) }, "needs a place"},
		{"geo off with place", func(m *browserModel) { m.Geolocation = geo("off", true, true) }, "takes no place"},
		{"geo fixed", func(m *browserModel) { m.Geolocation = geo("fixed", true, true) }, ""},
		{"server with login", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("a", "http://u:p@a.example.com:3128", false, false))
		}, "Bad upstream server"},
		{"server without port", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("a", "socks5://a.example.com", false, false))
		}, "Bad upstream server"},
		{"ftp server", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("a", "ftp://a.example.com:21", false, false))
		}, "Bad upstream server"},
		{"bad name", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("Res_1", "http://a.example.com:3128", false, false))
		}, "Bad upstream name"},
		{"twice", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes),
				up("a", "http://a.example.com:3128", false, false), up("a", "http://b.example.com:3128", false, false))
		}, "used twice"},
		{"half a login", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), halfLogin)
		}, "Incomplete upstream login"},
		{"interval without minutes", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), rot("interval", 0), up("a", "http://a.example.com:3128", false, false))
		}, "needs every_minutes"},
		{"minutes without interval", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Null(), rot("session", 5), up("a", "http://a.example.com:3128", false, false))
		}, "goes with interval"},
		{"change_ip_method without change_ip_url_wo", func(m *browserModel) {
			u := up("a", "http://a.example.com:3128", false, false)
			u.ChangeIPMethod = types.StringValue("POST")
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), u)
		}, "needs change_ip_url_wo"},
		{"min_change_ip_seconds without change_ip_url_wo", func(m *browserModel) {
			u := up("a", "http://a.example.com:3128", true, false)
			u.MinChangeIPSeconds = types.Int64Value(30)
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), u)
		}, "needs change_ip_url_wo"},
		{"change_ip_method with an address not known yet", func(m *browserModel) {
			u := up("a", "http://a.example.com:3128", false, false)
			u.ChangeIPMethod, u.ChangeIPURLWO = types.StringValue("POST"), types.StringUnknown()
			m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), u)
		}, ""},
		{"older locale code", func(m *browserModel) { m.Locale = types.StringValue("iw-IL") }, "Older language code"},
		{"older language code", func(m *browserModel) {
			m.Locale = types.StringValue("he-IL")
			m.Languages = types.ListValueMust(types.StringType, []attr.Value{types.StringValue("he-IL"), types.StringValue("iw")})
		}, "Older language code"},
		{"full proxy", func(m *browserModel) {
			m.Proxy = proxyOf(types.Int64Value(1), rot("interval", 5),
				up("a", "https://a.example.com:443", true, true), up("b", "socks5://[2001:db8::1]:1080", false, false))
		}, ""},
	}
	for _, c := range cases {
		m := baseBrowser()
		c.edit(&m)
		errs := browserConfigErrors(ctx, m)
		switch {
		case c.want == "" && len(errs) > 0:
			t.Errorf("%s: unexpected %v", c.name, errs)
		case c.want != "" && (len(errs) == 0 || !strings.Contains(errs[0][0], c.want)):
			t.Errorf("%s: got %v, want %q", c.name, errs, c.want)
		}
	}
	// A login written into the server never reaches the diagnostic.
	leak := baseBrowser()
	leak.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes), up("a", "socks5://user:S3cret@a.example.com:1080", false, false))
	if errs := browserConfigErrors(ctx, leak); len(errs) != 1 || strings.Contains(errs[0][1], "S3cret") || strings.Contains(errs[0][1], "user:") {
		t.Errorf("server with a login: %v", errs)
	}
	for _, ok := range []string{"en-US", "pt-BR", "zh-TW", "zh-Hant-TW", "es-419", "nb-NO", "he-IL", "id-ID", "fil"} {
		if _, older := canonicalTag(ok); older {
			t.Errorf("%q called an older spelling", ok)
		}
	}
	for old, want := range map[string]string{"iw-IL": "he-IL", "in-ID": "id-ID", "tl": "fil", "iw": "he"} {
		if got, older := canonicalTag(old); !older || got != want {
			t.Errorf("%q: %q %v, want %q", old, got, older, want)
		}
	}
	for _, ok := range []string{"ru-RU", "en-US", "es-419", "kk-KZ"} {
		if !localeRe.MatchString(ok) {
			t.Errorf("locale %q refused", ok)
		}
	}
	for _, bad := range []string{"ru-ru", "RU", "", "ru_RU"} {
		if localeRe.MatchString(bad) {
			t.Errorf("locale %q accepted", bad)
		}
	}
	for _, ok := range []string{"UTC", "Europe/Moscow", "America/Argentina/Buenos_Aires", "Etc/GMT+3"} {
		if !timezoneRe.MatchString(ok) {
			t.Errorf("time zone %q refused", ok)
		}
	}
	for _, bad := range []string{"Local", "", "Mars Base"} {
		if timezoneRe.MatchString(bad) {
			t.Errorf("time zone %q accepted", bad)
		}
	}
}

// A key without the proxies permission gets the platform's own words.
func TestProxyRefusalDiagnostic(t *testing.T) {
	msg := "This API key can't change browser proxies. A person can give it the proxies permission on the Keys page."
	body, _ := json.Marshal(map[string]string{"error": msg})
	var diags diag.Diagnostics
	apiDiag(&diags, "Cannot update browser", &client.APIError{Status: 403, Body: string(body)})
	if len(diags) != 1 || !strings.Contains(diags[0].Summary(), "proxies") || !strings.Contains(diags[0].Detail(), msg) {
		t.Errorf("diagnostic: %v", diags)
	}
	// Any other refusal is passed on as it was.
	diags = nil
	apiDiag(&diags, "Cannot update browser", &client.APIError{Status: 403, Body: `{"error":"forbidden"}`})
	if len(diags) != 1 || diags[0].Summary() != "Cannot update browser" {
		t.Errorf("other 403: %v", diags)
	}
	diags = nil
	apiDiag(&diags, "x", errors.New("boom"))
	if len(diags) != 1 || diags[0].Summary() != "x" {
		t.Errorf("plain error: %v", diags)
	}
}

// A configuration without the newer settings leaves them to the console:
// a locale, a time zone, a geolocation and a proxy a person set there are not
// read into state, plan nothing, and an update sends only what 0.11.0 sent,
// so a key without the proxies permission can still change cpu.
func TestConsoleSettingsKeptWithoutConfiguration(t *testing.T) {
	ctx := context.Background()
	m := baseBrowser()
	m.CPU = types.StringValue("1")
	st := stored(t, m, nil)

	var api map[string]any
	json.Unmarshal([]byte(`{
		"cpu": "1", "locale": "ru-RU", "timezone": "Europe/Moscow", "languages": ["ru-RU","ru","en-US","en"],
		"geolocation": {"mode": "off"}, "profilesReady": true,
		"proxy": {"upstreams": [{"name": "a", "server": "http://a.example.com:3128", "hasAuth": true, "hasChangeIp": false}],
			"rotation": {"mode": "off", "order": "sequential"}}
	}`), &api)
	read := readBrowser(ctx, st, api, false)
	for name, v := range map[string]attr.Value{"locale": read.Locale, "timezone": read.Timezone,
		"languages": read.Languages, "geolocation": read.Geolocation, "proxy": read.Proxy} {
		if !v.IsNull() {
			t.Errorf("%s read into state: %v", name, v)
		}
	}
	plan, removed := planBrowser(ctx, m, m, &read)
	if len(removed) != 0 {
		t.Errorf("removes %v", removed)
	}
	upd := plan
	upd.CPU = types.StringValue("2")
	if got := browserSpec(ctx, upd, upd, &read); !reflect.DeepEqual(got, map[string]any{"cpu": "2"}) {
		t.Errorf("cpu-only update sent %s", specJSON(t, got))
	}

	// An import reads them all, so the plan shows what the configuration lacks.
	imp := readBrowser(ctx, baseBrowser(), api, true)
	if imp.Locale.ValueString() != "ru-RU" || imp.Proxy.IsNull() || imp.Geolocation.IsNull() || imp.Languages.IsNull() {
		t.Errorf("import: %+v", imp)
	}
	// A managed setting the console cleared reads back empty: the plan puts it back.
	managed := st
	managed.Locale = types.StringValue("ru-RU")
	if got := readBrowser(ctx, managed, map[string]any{}, false); !got.Locale.IsNull() {
		t.Errorf("cleared locale reads %v", got.Locale)
	}
}

// lifecycle { ignore_changes = [proxy] }: Terraform hands the provider the
// state's proxy as the configuration (read-only flags set, write-only values
// empty), or no proxy at all. Either way the stored logins stay, the plan is
// clean, and an update sends no proxy.
func TestIgnoredProxyKeepsStoredValues(t *testing.T) {
	ctx := context.Background()
	m := baseBrowser()
	m.CPU = types.StringValue("1")
	m.Proxy = proxyOf(types.Int64Null(), types.ObjectNull(rotationAttrTypes),
		up("a", "http://a.example.com:3128", true, true), up("b", "http://b.example.com:3128", false, false))
	st := stored(t, m, nil)

	copied := st // the configuration Terraform builds under ignore_changes
	copied.CPU = types.StringValue("2")
	plan, removed := planBrowser(ctx, copied, copied, &st)
	if len(removed) != 0 {
		t.Errorf("removes %v", removed)
	}
	if !plan.Proxy.Equal(st.Proxy) {
		t.Errorf("plan changes the proxy:\n plan  %v\n state %v", plan.Proxy, st.Proxy)
	}
	if got := browserSpec(ctx, plan, copied, &st); got["proxy"] != nil {
		t.Errorf("update sent the proxy: %s", specJSON(t, got))
	}

	noCfg := baseBrowser() // the other shape: no proxy in the configuration
	noCfg.CPU = types.StringValue("2")
	planned := copied
	plan, removed = planBrowser(ctx, planned, noCfg, &st)
	if len(removed) != 0 || !plan.Proxy.Equal(st.Proxy) {
		t.Errorf("no proxy in the configuration: removed %v plan %v", removed, plan.Proxy)
	}
	if got := browserSpec(ctx, plan, noCfg, &st); got["proxy"] != nil {
		t.Errorf("update sent the proxy: %s", specJSON(t, got))
	}

	// Only the upstreams ignored, the rotation written: the flags are kept and sent.
	partial := copied
	partial.Proxy = proxyOf(types.Int64Null(), types.ObjectValueMust(rotationAttrTypes, map[string]attr.Value{
		"mode": types.StringValue("interval"), "every_minutes": types.Int64Value(5), "order": types.StringNull(),
	}), st.proxy(ctx).upstreams(ctx)...)
	plan, removed = planBrowser(ctx, partial, partial, &st)
	if len(removed) != 0 {
		t.Errorf("partial removes %v", removed)
	}
	got := browserSpec(ctx, plan, partial, &st)
	ups := got["proxy"].(map[string]any)["upstreams"].([]map[string]any)
	if ups[0]["hasAuth"] != true || ups[0]["hasChangeIp"] != true || ups[1]["hasAuth"] != false || ups[0]["username"] != nil {
		t.Errorf("partial: %s", specJSON(t, got))
	}
}
