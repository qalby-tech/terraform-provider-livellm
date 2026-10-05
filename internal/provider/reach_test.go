package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// reachResources is every resource that has reachable_from, with the
// workload type and kind block it is stored as.
func reachResources() []struct {
	name  string
	r     func() resource.Resource
	wtype string
	block func(w *client.Workload, sp map[string]any)
	vals  map[string]tftypes.Value
} {
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	n := func(v int64) tftypes.Value { return tftypes.NewValue(tftypes.Number, v) }
	return []struct {
		name  string
		r     func() resource.Resource
		wtype string
		block func(w *client.Workload, sp map[string]any)
		vals  map[string]tftypes.Value
	}{
		{"vm", NewVMResource, "vm-ubuntu", func(w *client.Workload, sp map[string]any) { w.VM = sp },
			map[string]tftypes.Value{"os": s("ubuntu"), "username": s("u"), "password_wo": s("password1"), "password_wo_version": n(1)}},
		{"container_app", NewContainerAppResource, "pod", func(w *client.Workload, sp map[string]any) { w.Pod = sp },
			map[string]tftypes.Value{"image": s("nginx")}},
		{"browser", NewBrowserResource, "browser", func(w *client.Workload, sp map[string]any) { w.Browser = sp }, nil},
		{"browser_api", NewBrowserAPIResource, "controller", func(w *client.Workload, sp map[string]any) { w.Controller = sp },
			map[string]tftypes.Value{"all_browsers": tftypes.NewValue(tftypes.Bool, true)}},
		{"desktop_app", NewDesktopAppResource, "desktop", func(w *client.Workload, sp map[string]any) { w.Desktop = sp }, nil},
	}
}

// Every resource has reachable_from: optional, computed, a list of names,
// kept from state when the configuration leaves it out, and checked at plan.
func TestReachOnEveryResource(t *testing.T) {
	ctx := context.Background()
	for _, c := range reachResources() {
		var sr resource.SchemaResponse
		c.r().Schema(ctx, resource.SchemaRequest{}, &sr)
		a, ok := sr.Schema.Attributes["reachable_from"].(schema.ListAttribute)
		if !ok {
			t.Errorf("%s: no reachable_from list", c.name)
			continue
		}
		if !a.Optional || !a.Computed || a.Required || a.ElementType != types.StringType {
			t.Errorf("%s: reachable_from %+v", c.name, a)
		}
		keeps := false
		for _, m := range a.PlanModifiers {
			if reflect.TypeOf(m) == reflect.TypeOf(listplanmodifier.UseStateForUnknown()) {
				keeps = true
			}
		}
		if !keeps {
			t.Errorf("%s: reachable_from isn't kept from state", c.name)
		}
		checked := false
		for _, v := range a.Validators {
			if rv, ok := v.(reachValidator); ok {
				checked = true
				if rv.stack != (c.name == "container_app") {
					t.Errorf("%s: stack check %v", c.name, rv.stack)
				}
			}
		}
		if !checked {
			t.Errorf("%s: reachable_from isn't checked at plan", c.name)
		}
		for _, w := range []string{"Network", "[] closes", "whole workspace"} {
			if !strings.Contains(a.Description, w) && !strings.Contains(a.Description, strings.ReplaceAll(w, "whole ", "")) {
				t.Errorf("%s: description lacks %q", c.name, w)
			}
		}
	}
}

// A database has no reachable_from: it is reached only by what links it.
func TestDatabaseHasNoReach(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewStorageResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	if _, ok := sr.Schema.Attributes["reachable_from"]; ok {
		t.Fatal("livellm_storage has reachable_from")
	}
	if _, ok := NewStorageResource().(resource.ResourceWithModifyPlan); ok {
		t.Error("livellm_storage has a ModifyPlan (it only warned about reachable_from)")
	}
	for _, w := range []string{"reached only by what links it", "no reachable_from"} {
		if !strings.Contains(sr.Schema.Description, w) {
			t.Errorf("livellm_storage description lacks %q", w)
		}
	}
}

func names(v ...string) types.List { return reachList(v) }

// The plan-time checks: "*" goes alone, names are resource names, none twice,
// at most 64, never the resource itself or its own stack.
func TestReachErrors(t *testing.T) {
	many := make([]string, 65)
	for i := range many {
		many[i] = "r" + strings.Repeat("a", i%30) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	long := strings.Repeat("a", 41)
	cases := []struct {
		name  string
		l     types.List
		stack types.String
		want  []string
	}{
		{"whole workspace", names("*"), sNull, nil},
		{"names", names("web", "db-1"), sNull, nil},
		{"none", names(), sNull, nil},
		{"star with a name", names("*", "web"), sNull, []string{"\"*\" goes alone"}},
		{"star twice", names("*", "*"), sNull, []string{"A name twice in reachable_from", "\"*\" goes alone"}},
		{"upper case", names("Web"), sNull, []string{"Not a resource name"}},
		{"trailing hyphen", names("web-"), sNull, []string{"Not a resource name"}},
		{"too long", names(long), sNull, []string{"Not a resource name"}},
		{"forty", names(strings.Repeat("a", 40)), sNull, nil},
		{"twice", names("web", "web"), sNull, []string{"A name twice in reachable_from"}},
		{"itself", names("self"), sNull, []string{"A resource always reaches itself"}},
		{"its own stack", names("shop"), str("shop"), []string{"The services of an app always reach each other"}},
		{"another stack", names("other"), str("shop"), nil},
		{"too many", reachList(many), sNull, []string{"Too many names in reachable_from"}},
		{"64", reachList(many[:64]), sNull, nil},
		{"a name known at apply", types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown(), types.StringValue("web")}), sNull, nil},
		{"star next to a name known at apply", types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown(), types.StringValue("*")}), sNull, []string{"\"*\" goes alone"}},
	}
	for _, c := range cases {
		var got []string
		for _, e := range reachErrors(c.l, str("self"), c.stack) {
			got = append(got, e[0])
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The validator reads the resource's own name and, for an app, its stack from
// the configuration.
func TestReachValidatorReadsTheConfiguration(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewContainerAppResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	list := func(v ...string) tftypes.Value {
		vals := make([]tftypes.Value, 0, len(v))
		for _, x := range v {
			vals = append(vals, s(x))
		}
		return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, vals)
	}
	cfg := func(vals map[string]tftypes.Value) tfsdk.Config {
		all := map[string]tftypes.Value{}
		for k, at := range typ.AttributeTypes {
			all[k] = tftypes.NewValue(at, nil)
		}
		for k, v := range vals {
			all[k] = v
		}
		return tfsdk.Config{Schema: sr.Schema, Raw: tftypes.NewValue(typ, all)}
	}
	cases := []struct {
		name    string
		vals    map[string]tftypes.Value
		refused bool
	}{
		{"left out", map[string]tftypes.Value{"name": s("web")}, false},
		{"another resource", map[string]tftypes.Value{"name": s("web"), "reachable_from": list("db")}, false},
		{"itself", map[string]tftypes.Value{"name": s("web"), "reachable_from": list("web")}, true},
		{"its stack", map[string]tftypes.Value{"name": s("web"), "stack": s("shop"), "reachable_from": list("shop")}, true},
		{"star and a name", map[string]tftypes.Value{"name": s("web"), "reachable_from": list("*", "db")}, true},
		{"known at apply", map[string]tftypes.Value{"name": s("web"), "reachable_from": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, tftypes.UnknownValue)}, false},
	}
	for _, c := range cases {
		conf := cfg(c.vals)
		var v types.List
		conf.GetAttribute(ctx, path.Root("reachable_from"), &v)
		resp := &validator.ListResponse{}
		reachValidator{stack: true}.ValidateList(ctx, validator.ListRequest{
			Path: path.Root("reachable_from"), ConfigValue: v, Config: conf,
		}, resp)
		if got := resp.Diagnostics.HasError(); got != c.refused {
			t.Errorf("%s: refused=%v, want %v (%v)", c.name, got, c.refused, resp.Diagnostics)
		}
	}
}

// Left out sends nothing; [] is sent as [], never null; a value not known
// yet sends nothing.
func TestReachSent(t *testing.T) {
	ctx := context.Background()
	if reachOf(ctx, types.ListNull(types.StringType)) != nil || reachOf(ctx, types.ListUnknown(types.StringType)) != nil {
		t.Error("left out or unknown is sent")
	}
	if reachOf(ctx, types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown()})) != nil {
		t.Error("a name not known yet is sent")
	}
	body := map[string]any{"id": "web"}
	reachBody(body, reachOf(ctx, names()))
	if b, _ := json.Marshal(body); string(b) != `{"id":"web","reachableFrom":[]}` {
		t.Errorf("closed create body %s", b)
	}
	body = map[string]any{"id": "web"}
	reachBody(body, nil)
	if b, _ := json.Marshal(body); string(b) != `{"id":"web"}` {
		t.Errorf("left-out create body %s", b)
	}
	// A PUT carries it only when set: left out, the platform keeps its value.
	for _, c := range []struct {
		reach *[]string
		want  string
	}{
		{nil, `{"id":"web","type":"pod"}`},
		{reachOf(ctx, names()), `{"id":"web","type":"pod","reachableFrom":[]}`},
		{reachOf(ctx, names("*")), `{"id":"web","type":"pod","reachableFrom":["*"]}`},
	} {
		if b, _ := json.Marshal(client.Workload{ID: "web", Type: "pod", ReachableFrom: c.reach}); string(b) != c.want {
			t.Errorf("PUT %s, want %s", b, c.want)
		}
	}
	// The write that stops an app born stopped says nothing about it.
	app := containerAppModel{Image: str("nginx"), Stopped: types.BoolValue(true), Name: str("web")}
	if w := stopAfterCreateBody(ctx, app); w == nil || w.ReachableFrom != nil {
		t.Errorf("stop write %+v", w)
	}
}

// A refresh reads what the platform holds; the same names in another order
// keep the order written; nothing held reads as null.
func TestReadReach(t *testing.T) {
	ctx := context.Background()
	held := func(v ...string) *[]string { return &v }
	cases := []struct {
		name string
		was  types.List
		held *[]string
		want types.List
	}{
		{"import", types.ListNull(types.StringType), held("*"), names("*")},
		{"nothing held", names("web"), nil, types.ListNull(types.StringType)},
		{"closed", types.ListNull(types.StringType), held(), names()},
		{"same names reordered", names("b", "a"), held("a", "b"), names("b", "a")},
		{"changed in the console", names("a"), held("a", "c"), names("a", "c")},
		{"opened to all", names(), held("*"), names("*")},
	}
	for _, c := range cases {
		if got := readReach(ctx, c.was, c.held); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// After an apply: a planned value stays as planned (Terraform holds the
// apply to its plan); a value left to the platform is read back; a value the
// platform didn't keep warns.
func TestSettleReach(t *testing.T) {
	ctx := context.Background()
	held := func(v *[]string) *client.Client {
		return fakePlatform(t, client.Workload{ID: "web", Type: "pod", Pod: map[string]any{}, ReachableFrom: v})
	}
	all := &[]string{"*"}
	partly := types.ListValueMust(types.StringType, []attr.Value{types.StringUnknown()})
	cases := []struct {
		name    string
		planned types.List
		sent    *[]string
		held    *[]string
		want    types.List
		warns   bool
	}{
		{"create left out, closed", types.ListUnknown(types.StringType), nil, &[]string{}, names(), false},
		{"create left out, old platform", types.ListUnknown(types.StringType), nil, nil, types.ListNull(types.StringType), false},
		{"update left out keeps the plan", names("a"), nil, all, names("a"), false},
		{"kept as written", names("*"), all, all, names("*"), false},
		{"kept in another order", names("b", "a"), &[]string{"b", "a"}, &[]string{"a", "b"}, names("b", "a"), false},
		{"old platform", names("*"), all, nil, names("*"), true},
		{"a stack mate wrote another", names("*"), all, &[]string{}, names("*"), true},
		// A name known only at apply: the plan's list holds an unknown name,
		// and the state takes the names sent, never the unknown.
		{"a name known at apply", partly, &[]string{"web2"}, &[]string{"web2"}, names("web2"), false},
		{"a name known at apply, not kept", partly, &[]string{"web2"}, &[]string{}, names("web2"), true},
		{"partly known, nothing sent", partly, nil, &[]string{"a"}, names("a"), false},
	}
	for _, c := range cases {
		var diags diag.Diagnostics
		got := settleReach(ctx, held(c.held), "web", c.planned, c.sent, &diags)
		if !got.Equal(c.want) || !whollyKnown(got) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if diags.HasError() || (diags.WarningsCount() > 0) != c.warns {
			t.Errorf("%s: diagnostics %v", c.name, diags)
		}
	}
}

// recordingPlatform keeps one workload: a create takes the body's
// reachableFrom (none means closed, as the platform does), a PUT replaces it
// when the body has one and keeps it otherwise. It records every write.
type recordingPlatform struct {
	mu     sync.Mutex
	w      client.Workload
	block  func(w *client.Workload, sp map[string]any)
	writes []map[string]any
}

func (p *recordingPlatform) serve(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/workloads/"):
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(raw, &body)
			p.writes = append(p.writes, body)
			p.w.ID, _ = body["id"].(string)
			p.w.ReachableFrom = &[]string{}
			if v, ok := body["reachableFrom"].([]any); ok {
				got := []string{}
				for _, x := range v {
					got = append(got, x.(string))
				}
				p.w.ReachableFrom = &got
			}
			p.block(&p.w, kindBlock(p.w.Type))
		case r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(raw, &body)
			p.writes = append(p.writes, body)
			var w client.Workload
			json.Unmarshal(raw, &w)
			if w.ReachableFrom != nil {
				p.w.ReachableFrom = w.ReachableFrom
			}
		case r.URL.Path == "/v1/workspace":
			_ = json.NewEncoder(rw).Encode(map[string]any{"spec": map[string]any{"workloads": []client.Workload{p.w}}})
		case r.URL.Path == "/v1/status":
			_, _ = rw.Write([]byte(`{"workloads":[{"id":"` + p.w.ID + `","type":"` + p.w.Type + `","phase":"Running","ready":true}]}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "llc_test")
}

// kindBlock is a small stored block for a workload type (an empty one would
// read back as no block at all).
func kindBlock(wtype string) map[string]any {
	switch wtype {
	case "pod":
		return map[string]any{"image": "nginx"}
	case "controller":
		return map[string]any{"autodiscover": true}
	}
	return map[string]any{"cpu": "1"}
}

// plannedValues builds a create's configuration and plan from attribute
// values: the configuration holds only them; the plan holds unknown for every
// computed attribute they leave out, as Terraform plans a create.
func plannedValues(ctx context.Context, sch schema.Schema, vals map[string]tftypes.Value) (tfsdk.Config, tfsdk.Plan) {
	typ := sch.Type().TerraformType(ctx).(tftypes.Object)
	cfg, plan := map[string]tftypes.Value{}, map[string]tftypes.Value{}
	for k, at := range typ.AttributeTypes {
		cfg[k], plan[k] = tftypes.NewValue(at, nil), tftypes.NewValue(at, nil)
		if a, ok := sch.Attributes[k]; ok && a.IsComputed() {
			plan[k] = tftypes.NewValue(at, tftypes.UnknownValue)
		}
		if v, ok := vals[k]; ok {
			cfg[k], plan[k] = v, v
		}
	}
	return tfsdk.Config{Schema: sch, Raw: tftypes.NewValue(typ, cfg)},
		tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(typ, plan)}
}

// Through each resource's own Create, Read and Update: a create that leaves
// reachable_from out sends none and stores what the platform made ([]); one
// that sets it sends it and stores exactly what was planned (anything else
// fails the apply); a refresh reads a change made elsewhere; an update that
// leaves it out sends none, and one that sets it sends it.
func TestReachThroughEveryResource(t *testing.T) {
	ctx := context.Background()
	strList := func(v ...string) tftypes.Value {
		vals := make([]tftypes.Value, 0, len(v))
		for _, x := range v {
			vals = append(vals, tftypes.NewValue(tftypes.String, x))
		}
		return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, vals)
	}
	for _, c := range reachResources() {
		for _, set := range []bool{false, true} {
			r := c.r()
			p := &recordingPlatform{w: client.Workload{Type: c.wtype}, block: c.block}
			r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
				ProviderData: &providerData{Client: p.serve(t)},
			}, &resource.ConfigureResponse{})
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			vals := map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "res")}
			for k, v := range c.vals {
				vals[k] = v
			}
			if set {
				vals["reachable_from"] = strList("web", "db")
			}
			cfg, plan := plannedValues(ctx, sr.Schema, vals)
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
			r.Create(ctx, resource.CreateRequest{Config: cfg, Plan: plan}, &resp)
			label := c.name
			if set {
				label += " (set)"
			}
			if resp.Diagnostics.HasError() || resp.Diagnostics.WarningsCount() > 0 {
				t.Errorf("%s: create: %v", label, resp.Diagnostics)
				continue
			}
			sent, has := p.writes[0]["reachableFrom"]
			var got types.List
			resp.State.GetAttribute(ctx, path.Root("reachable_from"), &got)
			if set {
				if !has || !reflect.DeepEqual(sent, []any{"web", "db"}) {
					t.Errorf("%s: create sent %v", label, sent)
				}
				if !got.Equal(names("web", "db")) {
					t.Errorf("%s: stored %v, want the planned value", label, got)
				}
			} else {
				if has {
					t.Errorf("%s: create sent %v, want nothing", label, sent)
				}
				if !got.Equal(names()) {
					t.Errorf("%s: stored %v, want what the platform made ([])", label, got)
				}
			}

			// Changed in the console: a refresh reads it.
			p.mu.Lock()
			p.w.ReachableFrom = &[]string{"*"}
			p.mu.Unlock()
			rr := resource.ReadResponse{State: resp.State}
			r.Read(ctx, resource.ReadRequest{State: resp.State}, &rr)
			rr.State.GetAttribute(ctx, path.Root("reachable_from"), &got)
			if rr.Diagnostics.HasError() || !got.Equal(names("*")) {
				t.Errorf("%s: refresh read %v (%v)", label, got, rr.Diagnostics)
			}

			// An update: the plan is the refreshed state (reachable_from kept
			// from state when left out, the configured value otherwise).
			stateVals := map[string]tftypes.Value{}
			var obj map[string]tftypes.Value
			rr.State.Raw.As(&obj)
			for k, v := range obj {
				stateVals[k] = v
			}
			updVals := map[string]tftypes.Value{}
			for k, v := range vals {
				updVals[k] = v
			}
			if set {
				updVals["reachable_from"] = strList()
			}
			ucfg, _ := plannedValues(ctx, sr.Schema, updVals)
			for k, v := range updVals {
				stateVals[k] = v
			}
			if !set {
				stateVals["reachable_from"] = obj["reachable_from"]
			}
			uplan := tfsdk.Plan{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), stateVals)}
			uresp := resource.UpdateResponse{State: rr.State}
			p.writes = nil
			r.Update(ctx, resource.UpdateRequest{Config: ucfg, Plan: uplan, State: rr.State}, &uresp)
			if uresp.Diagnostics.HasError() || uresp.Diagnostics.WarningsCount() > 0 {
				t.Errorf("%s: update: %v", label, uresp.Diagnostics)
				continue
			}
			if len(p.writes) != 1 {
				t.Errorf("%s: update wrote %d times", label, len(p.writes))
				continue
			}
			sent, has = p.writes[0]["reachableFrom"]
			uresp.State.GetAttribute(ctx, path.Root("reachable_from"), &got)
			if set {
				if !has || !reflect.DeepEqual(sent, []any{}) {
					t.Errorf("%s: update sent %v, want []", label, sent)
				}
				if !got.Equal(names()) {
					t.Errorf("%s: update stored %v", label, got)
				}
			} else {
				if has {
					t.Errorf("%s: update sent %v, want nothing", label, sent)
				}
				if !got.Equal(names("*")) || !reflect.DeepEqual(p.w.ReachableFrom, &[]string{"*"}) {
					t.Errorf("%s: update stored %v, platform %v; want the console's value kept", label, got, p.w.ReachableFrom)
				}
			}
		}
	}
}

// A key refused for letting resources reach each other gets the platform's
// words and how a person turns on Network; any other refusal passes as it was.
func TestNetworkRefusalDiagnostic(t *testing.T) {
	msg := "This API key can't let web reach nextcloud-db inside the workspace. A person can turn on Network for it on the Keys page."
	for _, body := range []map[string]string{
		{"error": msg, "code": "network_permission"},
		{"error": msg},
		{"error": "refused", "code": "network_permission"},
	} {
		raw, _ := json.Marshal(body)
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create container app", &client.APIError{Status: 403, Body: string(raw)})
		if len(diags) != 1 || !strings.Contains(diags[0].Summary(), "reach each other") ||
			!strings.Contains(diags[0].Detail(), body["error"]) || !strings.Contains(diags[0].Detail(), "Network") {
			t.Errorf("%v: diagnostic %v", body, diags)
		}
	}
	for _, body := range []string{
		`{"error":"forbidden"}`,
		`{"error":"This API key can't change browser proxies. A person can give it the proxies permission on the Keys page."}`,
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot update browser", &client.APIError{Status: 403, Body: body})
		if len(diags) != 1 || diags[0].Summary() != "Cannot update browser" {
			t.Errorf("other 403 %s: %v", body, diags)
		}
	}
	var diags diag.Diagnostics
	apiDiag(&diags, "x", errors.New("boom"))
	if len(diags) != 1 || diags[0].Summary() != "x" {
		t.Errorf("plain error: %v", diags)
	}
}

// An import reads reachable_from into state for every resource.
func TestImportReadsReach(t *testing.T) {
	ctx := context.Background()
	for _, c := range reachResources() {
		w := client.Workload{ID: "res", Type: c.wtype, ReachableFrom: &[]string{"web"}}
		c.block(&w, kindBlock(c.wtype))
		r := c.r()
		r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
			ProviderData: &providerData{Client: fakePlatform(t, w)},
		}, &resource.ConfigureResponse{})
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		st.SetAttribute(ctx, path.Root("name"), "res")
		resp := resource.ReadResponse{State: st}
		r.Read(ctx, resource.ReadRequest{State: st}, &resp)
		var got types.List
		resp.State.GetAttribute(ctx, path.Root("reachable_from"), &got)
		if resp.Diagnostics.HasError() || !got.Equal(names("web")) {
			t.Errorf("%s: import read %v (%v)", c.name, got, resp.Diagnostics)
		}
	}
}
