package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// modifyPlanReq builds a ModifyPlanRequest for an update from state values
// to plan values (the configuration holds the plan values). Attributes left
// out are null.
func modifyPlanReq(ctx context.Context, sch schema.Schema, state, plan map[string]tftypes.Value) resource.ModifyPlanRequest {
	typ := sch.Type().TerraformType(ctx).(tftypes.Object)
	obj := func(vals map[string]tftypes.Value) tftypes.Value {
		m := map[string]tftypes.Value{}
		for k, at := range typ.AttributeTypes {
			m[k] = tftypes.NewValue(at, nil)
			if v, ok := vals[k]; ok {
				m[k] = v
			}
		}
		return tftypes.NewValue(typ, m)
	}
	return resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: sch, Raw: obj(plan)},
		Plan:   tfsdk.Plan{Schema: sch, Raw: obj(plan)},
		State:  tfsdk.State{Schema: sch, Raw: obj(state)},
	}
}

// replacing sees exactly the attributes whose own plan modifiers replace the
// resource, on every resource with reachable_from.
func TestReplacingFollowsTheSchema(t *testing.T) {
	ctx := context.Background()
	want := map[string][]string{
		"vm":            {"desktop", "name", "os", "username", "windows_edition"},
		"container_app": {"name"},
		"storage":       {"engine", "name", "username"},
		"browser":       {"engine", "name"},
		"browser_api":   {"name"},
		"desktop_app":   {"keep_files", "name", "storage_gi"},
	}
	for _, rr := range reachResources() {
		var sr resource.SchemaResponse
		rr.r().Schema(ctx, resource.SchemaRequest{}, &sr)
		var got []string
		for k, a := range sr.Schema.Attributes {
			var from, to tftypes.Value
			switch a.(type) {
			case schema.StringAttribute:
				from, to = tftypes.NewValue(tftypes.String, "chrome"), tftypes.NewValue(tftypes.String, "camoufox")
			case schema.BoolAttribute:
				from, to = tftypes.NewValue(tftypes.Bool, false), tftypes.NewValue(tftypes.Bool, true)
			case schema.Int64Attribute:
				from, to = tftypes.NewValue(tftypes.Number, 1), tftypes.NewValue(tftypes.Number, 2)
			default:
				continue
			}
			name := tftypes.NewValue(tftypes.String, "box")
			state := map[string]tftypes.Value{"name": name, k: from}
			plan := map[string]tftypes.Value{"name": name, k: to}
			if replacing(ctx, modifyPlanReq(ctx, sr.Schema, state, plan)) {
				got = append(got, k)
			}
			if replacing(ctx, modifyPlanReq(ctx, sr.Schema, state, state)) {
				t.Errorf("%s: no change to %s replaces", rr.name, k)
			}
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want[rr.name]) {
			t.Errorf("%s: replacing on %v, want %v", rr.name, got, want[rr.name])
		}
	}
	// A create and a destroy are no replacement.
	var sr resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	req := modifyPlanReq(ctx, sr.Schema, nil, map[string]tftypes.Value{"os": tftypes.NewValue(tftypes.String, "debian")})
	req.State.Raw = tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)
	if replacing(ctx, req) {
		t.Error("a create replaces")
	}
}

// workspacePlatform answers the workspace read with these workloads.
func workspacePlatform(t *testing.T, ws ...client.Workload) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspace" {
			http.NotFound(rw, r)
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"spec": map[string]any{"workloads": ws}})
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "llc_test")
}

// A replacement warns about the new resource starting closed and about the
// names the other resources lose; nothing else does.
func TestWarnReachReplace(t *testing.T) {
	ctx := context.Background()
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	list := func(v ...string) tftypes.Value {
		vals := []tftypes.Value{}
		for _, x := range v {
			vals = append(vals, s(x))
		}
		return tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, vals)
	}
	reach := func(v ...string) *[]string { return &v }
	box := client.Workload{ID: "box", Type: "vm-ubuntu", ReachableFrom: reach("*")}
	db := client.Workload{ID: "db", Type: "storage", ReachableFrom: reach("box", "web")}
	cache := client.Workload{ID: "cache", Type: "storage", ReachableFrom: reach("box")}
	var vmSchema, appSchema resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &vmSchema)
	NewContainerAppResource().Schema(ctx, resource.SchemaRequest{}, &appSchema)
	vmState := map[string]tftypes.Value{"name": s("box"), "os": s("ubuntu"), "reachable_from": list("*")}
	with := func(base map[string]tftypes.Value, kv ...any) map[string]tftypes.Value {
		out := map[string]tftypes.Value{}
		for k, v := range base {
			out[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1].(tftypes.Value)
		}
		return out
	}
	nullList := tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil)
	cases := []struct {
		name      string
		sch       schema.Schema
		data      *providerData
		state     map[string]tftypes.Value
		plan      map[string]tftypes.Value
		want, not []string
	}{
		{"os change, left out, named by two", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box, db, cache)},
			vmState, with(vmState, "os", s("debian"), "reachable_from", nullList),
			[]string{"starts closed", "the whole workspace", "cache, db let box in by name", "second apply"}, nil},
		{"os change, written", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box, db)},
			vmState, with(vmState, "os", s("debian")),
			[]string{"db let box in by name"}, []string{"starts closed"}},
		{"os change, written, named by none", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box)},
			vmState, with(vmState, "os", s("debian")), nil, []string{"Replacing"}},
		{"os change, closed already", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box)},
			with(vmState, "reachable_from", list()), with(vmState, "os", s("debian"), "reachable_from", nullList),
			nil, []string{"Replacing"}},
		{"no replacement", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box, db)},
			vmState, with(vmState, "reachable_from", nullList), nil, []string{"Replacing"}},
		{"platform unread", vmSchema.Schema, nil,
			vmState, with(vmState, "os", s("debian"), "reachable_from", nullList),
			[]string{"starts closed", "Resources that let box in by name lose that name"}, nil},
		{"rename: the names follow the configuration", vmSchema.Schema, &providerData{Client: workspacePlatform(t, box, db)},
			vmState, with(vmState, "name", s("box2")), nil, []string{"Replacing"}},
		{"a service of a stack that stays", appSchema.Schema, &providerData{Client: workspacePlatform(t,
			client.Workload{ID: "web", Type: "pod", Pod: map[string]any{"stack": "shop"}, ReachableFrom: reach("*")},
			client.Workload{ID: "api", Type: "pod", Pod: map[string]any{"stack": "shop"}, ReachableFrom: reach("*")})},
			map[string]tftypes.Value{"name": s("web"), "reachable_from": list("*")},
			map[string]tftypes.Value{"name": s("web2"), "reachable_from": nullList}, nil, []string{"Replacing"}},
	}
	for _, c := range cases {
		req := modifyPlanReq(ctx, c.sch, c.state, c.plan)
		resp := resource.ModifyPlanResponse{Plan: req.Plan}
		warnReachReplace(ctx, c.data, req, &resp)
		var text string
		for _, d := range resp.Diagnostics {
			text += d.Summary() + "\n" + d.Detail() + "\n"
		}
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: errors %v", c.name, resp.Diagnostics)
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: no %q in %q", c.name, w, text)
			}
		}
		for _, w := range c.not {
			if strings.Contains(text, w) {
				t.Errorf("%s: %q in %q", c.name, w, text)
			}
		}
	}
}

// Every resource's own ModifyPlan gives the warning.
func TestEveryModifyPlanWarnsOnReplace(t *testing.T) {
	ctx := context.Background()
	s := func(v string) tftypes.Value { return tftypes.NewValue(tftypes.String, v) }
	all := tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{s("*")})
	for _, rr := range reachResources() {
		r := rr.r()
		r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
			ProviderData: &providerData{Client: workspacePlatform(t, client.Workload{ID: "box", Type: rr.wtype, ReachableFrom: &[]string{"*"}})},
		}, &resource.ConfigureResponse{})
		mp, ok := r.(resource.ResourceWithModifyPlan)
		if !ok {
			t.Errorf("%s: no ModifyPlan", rr.name)
			continue
		}
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		state := map[string]tftypes.Value{"name": s("box"), "reachable_from": all}
		plan := map[string]tftypes.Value{"name": s("box2")}
		for k, v := range rr.vals {
			state[k], plan[k] = v, v
		}
		req := modifyPlanReq(ctx, sr.Schema, state, plan)
		resp := resource.ModifyPlanResponse{Plan: req.Plan}
		mp.ModifyPlan(ctx, req, &resp)
		found := false
		for _, d := range resp.Diagnostics {
			found = found || strings.Contains(d.Summary(), "Replacing box")
		}
		if !found {
			t.Errorf("%s: no replacement warning in %v", rr.name, resp.Diagnostics)
		}
	}
}
