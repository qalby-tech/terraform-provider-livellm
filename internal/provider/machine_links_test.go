package provider

import (
	"context"
	"encoding/json"
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
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

func machineDBs(v ...string) types.List {
	vals := make([]attr.Value, 0, len(v))
	for _, n := range v {
		vals = append(vals, types.ObjectValueMust(machineDatabaseAttrTypes, map[string]attr.Value{"name": types.StringValue(n)}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: machineDatabaseAttrTypes}, vals)
}

// livellm_vm and livellm_desktop_app have a database block holding a name
// only: a machine's link is reach only, with no variables.
func TestMachineDatabaseBlock(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		r    resource.Resource
		what string
	}{{"vm", NewVMResource(), "machine"}, {"desktop_app", NewDesktopAppResource(), "Desktop App"}} {
		var sr resource.SchemaResponse
		c.r.Schema(ctx, resource.SchemaRequest{}, &sr)
		b, ok := sr.Schema.Blocks["database"].(schema.ListNestedBlock)
		if !ok {
			t.Errorf("%s: no database block", c.name)
			continue
		}
		attrs := b.NestedObject.Attributes
		if len(attrs) != 1 {
			t.Errorf("%s: database block has %d attributes, want name only", c.name, len(attrs))
		}
		if n, ok := attrs["name"].(schema.StringAttribute); !ok || !n.Required {
			t.Errorf("%s: database.name %+v", c.name, attrs["name"])
		}
		for _, w := range []string{"Reach only", "no variables", "nothing restarts", c.what, "Network"} {
			if !strings.Contains(b.Description, w) {
				t.Errorf("%s: database description lacks %q", c.name, w)
			}
		}
	}
}

// The blocks go out as databases: [{id}], in the order written, in the
// create body and in every later save (the one that stops a machine born
// stopped too); none, nothing is sent.
func TestMachineDatabaseLinksSent(t *testing.T) {
	ctx := context.Background()
	want := []map[string]any{{"id": "app-db"}, {"id": "cache"}}
	vm := vmResourceModel{Name: types.StringValue("box"), OS: types.StringValue("ubuntu"), Database: machineDBs("app-db", "cache")}
	for _, wr := range []vmWrite{{password: "pw", create: true}, {}} {
		if got := vmSpec(ctx, vm, wr)["databases"]; !reflect.DeepEqual(got, want) {
			t.Errorf("vm %+v: databases %v, want %v", wr, got, want)
		}
	}
	desk := desktopAppModel{Name: types.StringValue("desk"), Database: machineDBs("app-db", "cache")}
	if got := desktopSpec(desk)["databases"]; !reflect.DeepEqual(got, want) {
		t.Errorf("desktop: databases %v, want %v", got, want)
	}
	for _, l := range []types.List{machineDBs(), types.ListNull(types.ObjectType{AttrTypes: machineDatabaseAttrTypes}), {}} {
		vm.Database, desk.Database = l, l
		if _, ok := vmSpec(ctx, vm, vmWrite{})["databases"]; ok {
			t.Errorf("vm: no blocks (%v), yet databases sent", l)
		}
		if _, ok := desktopSpec(desk)["databases"]; ok {
			t.Errorf("desktop: no blocks (%v), yet databases sent", l)
		}
	}
}

func TestMachineDatabaseErrors(t *testing.T) {
	if errs := machineDatabaseErrors(machineDBs("a", "b"), "machine"); len(errs) != 0 {
		t.Errorf("valid links refused: %v", errs)
	}
	unknown := types.ListValueMust(types.ObjectType{AttrTypes: machineDatabaseAttrTypes}, []attr.Value{
		types.ObjectValueMust(machineDatabaseAttrTypes, map[string]attr.Value{"name": types.StringUnknown()}),
		types.ObjectValueMust(machineDatabaseAttrTypes, map[string]attr.Value{"name": types.StringUnknown()}),
	})
	if errs := machineDatabaseErrors(unknown, "machine"); len(errs) != 0 {
		t.Errorf("names not known yet are for the platform: %v", errs)
	}
	many := []string{}
	for i := 0; i < 9; i++ {
		many = append(many, string(rune('a'+i)))
	}
	for _, c := range []struct {
		name string
		l    types.List
		want string
	}{
		{"more than 8", machineDBs(many...), "Too many databases"},
		{"twice", machineDBs("db", "db"), "Duplicate database"},
	} {
		found := false
		for _, e := range machineDatabaseErrors(c.l, "Desktop App") {
			found = found || e[0] == c.want
		}
		if !found {
			t.Errorf("%s: want %q", c.name, c.want)
		}
	}
}

// The links read back as the platform keeps them; none is an empty list.
func TestReadMachineDatabases(t *testing.T) {
	got := readMachineDatabases(map[string]any{"databases": []any{
		map[string]any{"id": "app-db"}, map[string]any{"id": "cache"},
	}})
	if !got.Equal(machineDBs("app-db", "cache")) {
		t.Errorf("read %v", got)
	}
	if empty := readMachineDatabases(map[string]any{}); empty.IsNull() || len(empty.Elements()) != 0 {
		t.Errorf("no links read as %v, want an empty list", empty)
	}
}

// machinePlatform keeps one machine or Desktop App the way the platform
// does: a create's flat body is its settings, and a PUT replaces them whole,
// links included. keeps false is a platform older than reach-only links: it
// drops the databases key without a word.
type machinePlatform struct {
	mu     sync.Mutex
	w      client.Workload
	keeps  bool
	writes []map[string]any
}

func (p *machinePlatform) store(sp map[string]any) {
	if sp == nil {
		sp = map[string]any{}
	}
	if !p.keeps {
		delete(sp, "databases")
	}
	if p.w.Type == "desktop" {
		p.w.Desktop = sp
	} else {
		p.w.VM = sp
	}
}

func (p *machinePlatform) held() types.List {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.w.Type == "desktop" {
		return readMachineDatabases(p.w.Desktop)
	}
	return readMachineDatabases(p.w.VM)
}

func (p *machinePlatform) serve(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/workloads/"):
			raw, _ := io.ReadAll(r.Body)
			var body, sp map[string]any
			json.Unmarshal(raw, &body)
			json.Unmarshal(raw, &sp)
			p.writes = append(p.writes, body)
			p.w.ID, _ = sp["id"].(string)
			p.w.ReachableFrom = &[]string{}
			delete(sp, "id")
			delete(sp, "reachableFrom")
			p.store(sp)
		case r.Method == http.MethodPut:
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			json.Unmarshal(raw, &body)
			p.writes = append(p.writes, body)
			var w client.Workload
			json.Unmarshal(raw, &w)
			p.w.Stopped = w.Stopped
			if p.w.Type == "desktop" {
				p.store(w.Desktop)
			} else {
				p.store(w.VM)
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

// notKept counts the warnings that the platform didn't keep the links.
func notKept(d diag.Diagnostics) int {
	n := 0
	for _, w := range d.Warnings() {
		if w.Summary() == "Database links not kept as written" {
			n++
		}
	}
	return n
}

// Through the resources, against a platform that keeps the links and one
// that drops them: a create sends the links flat; a refresh reads the
// platform's (a link added in the console, or none on the older platform);
// an update sends the blocks as written, a changed set included, and the
// platform holds exactly them; an update without blocks sends none and the
// links go. The older platform's applies succeed with a warning that the
// links weren't kept; the state holds the plan, as Terraform requires.
func TestMachineDatabasesThroughTheResources(t *testing.T) {
	ctx := context.Background()
	dbType := tftypes.List{ElementType: tftypes.Object{AttributeTypes: map[string]tftypes.Type{"name": tftypes.String}}}
	dbVal := func(v ...string) tftypes.Value {
		vals := []tftypes.Value{}
		for _, n := range v {
			vals = append(vals, tftypes.NewValue(dbType.ElementType, map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, n)}))
		}
		return tftypes.NewValue(dbType, vals)
	}
	links := func(v ...string) []any {
		out := []any{}
		for _, n := range v {
			out = append(out, map[string]any{"id": n})
		}
		return out
	}
	for _, c := range reachResources() {
		if c.name != "vm" && c.name != "desktop_app" {
			continue
		}
		blockKey := map[string]string{"vm": "vm", "desktop_app": "desktop"}[c.name]
		for _, keeps := range []bool{true, false} {
			label := c.name
			if !keeps {
				label += " (older platform)"
			}
			r := c.r()
			p := &machinePlatform{w: client.Workload{Type: c.wtype}, keeps: keeps}
			r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
				ProviderData: &providerData{Client: p.serve(t)},
			}, &resource.ConfigureResponse{})
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			objType := sr.Schema.Type().TerraformType(ctx)
			withDBs := func(v ...string) map[string]tftypes.Value {
				vals := map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "res"), "database": dbVal(v...)}
				for k, x := range c.vals {
					vals[k] = x
				}
				// a block with something in it (an empty one isn't sent)
				if c.name == "vm" {
					vals["cpus"] = tftypes.NewValue(tftypes.Number, 2)
				} else {
					vals["cpu"] = tftypes.NewValue(tftypes.String, "2")
				}
				return vals
			}
			wantWarn := func(d diag.Diagnostics) bool {
				if keeps {
					return d.WarningsCount() == 0
				}
				return d.WarningsCount() == 1 && notKept(d) == 1
			}

			cfg, plan := plannedValues(ctx, sr.Schema, withDBs("app-db"))
			resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(objType, nil)}}
			r.Create(ctx, resource.CreateRequest{Config: cfg, Plan: plan}, &resp)
			if resp.Diagnostics.HasError() || !wantWarn(resp.Diagnostics) {
				t.Errorf("%s: create: %v", label, resp.Diagnostics)
				continue
			}
			if got := p.writes[0]["databases"]; !reflect.DeepEqual(got, links("app-db")) {
				t.Errorf("%s: create sent databases %v", label, got)
			}
			var got types.List
			resp.State.GetAttribute(ctx, path.Root("database"), &got)
			if !got.Equal(machineDBs("app-db")) {
				t.Errorf("%s: create stored %v, want the plan", label, got)
			}

			// A refresh reads what the platform holds: on the older one, no
			// links (the plan then shows the block to add again); on the
			// other, a link added in the console too.
			want := machineDBs()
			if keeps {
				p.mu.Lock()
				sp := p.w.VM
				if c.name == "desktop_app" {
					sp = p.w.Desktop
				}
				sp["databases"] = links("app-db", "cache")
				p.mu.Unlock()
				want = machineDBs("app-db", "cache")
			}
			rr := resource.ReadResponse{State: resp.State}
			r.Read(ctx, resource.ReadRequest{State: resp.State}, &rr)
			rr.State.GetAttribute(ctx, path.Root("database"), &got)
			if rr.Diagnostics.HasError() || !got.Equal(want) {
				t.Errorf("%s: refresh read %v, want %v (%v)", label, got, want, rr.Diagnostics)
			}

			update := func(state tfsdk.State, dbs ...string) resource.UpdateResponse {
				var obj map[string]tftypes.Value
				state.Raw.As(&obj)
				obj["database"] = dbVal(dbs...)
				ucfg, _ := plannedValues(ctx, sr.Schema, withDBs(dbs...))
				uplan := tfsdk.Plan{Schema: sr.Schema, Raw: tftypes.NewValue(objType, obj)}
				p.writes = nil
				uresp := resource.UpdateResponse{State: state}
				r.Update(ctx, resource.UpdateRequest{Config: ucfg, Plan: uplan, State: state}, &uresp)
				return uresp
			}

			// Another set in another order: sent as written, held exactly.
			uresp := update(rr.State, "cache", "other-db")
			if uresp.Diagnostics.HasError() || !wantWarn(uresp.Diagnostics) || len(p.writes) != 1 {
				t.Errorf("%s: update: %v (%d writes)", label, uresp.Diagnostics, len(p.writes))
				continue
			}
			kind, _ := p.writes[0][blockKey].(map[string]any)
			if !reflect.DeepEqual(kind["databases"], links("cache", "other-db")) {
				t.Errorf("%s: update sent databases %v", label, kind["databases"])
			}
			held := machineDBs()
			if keeps {
				held = machineDBs("cache", "other-db")
			}
			if got := p.held(); !got.Equal(held) {
				t.Errorf("%s: platform holds %v after the update, want %v", label, got, held)
			}
			uresp.State.GetAttribute(ctx, path.Root("database"), &got)
			if !got.Equal(machineDBs("cache", "other-db")) {
				t.Errorf("%s: update stored %v, want the plan", label, got)
			}

			// No blocks: nothing sent about links, and the full save drops them.
			uresp = update(uresp.State)
			if uresp.Diagnostics.HasError() || uresp.Diagnostics.WarningsCount() > 0 || len(p.writes) != 1 {
				t.Errorf("%s: update without blocks: %v (%d writes)", label, uresp.Diagnostics, len(p.writes))
				continue
			}
			kind, _ = p.writes[0][blockKey].(map[string]any)
			if kind == nil {
				t.Errorf("%s: update sent no %s block", label, blockKey)
			}
			if _, ok := kind["databases"]; ok {
				t.Errorf("%s: update without blocks sent %v", label, kind["databases"])
			}
			if got := p.held(); !got.Equal(machineDBs()) {
				t.Errorf("%s: platform still holds %v", label, got)
			}
		}
	}
}

// settleMachineDatabases warns only when the platform holds other links than
// the plan, in any order, and stays quiet when it can't read the workspace.
func TestSettleMachineDatabases(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		held    []any
		planned types.List
		warns   bool
	}{
		{"kept", []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}, machineDBs("a", "b"), false},
		{"kept in another order", []any{map[string]any{"id": "b"}, map[string]any{"id": "a"}}, machineDBs("a", "b"), false},
		{"none either way", nil, machineDBs(), false},
		{"dropped", nil, machineDBs("a"), true},
		{"one dropped", []any{map[string]any{"id": "a"}}, machineDBs("a", "b"), true},
		{"unknown plan", nil, types.ListUnknown(types.ObjectType{AttrTypes: machineDatabaseAttrTypes}), false},
	}
	for _, c := range cases {
		sp := map[string]any{"cpus": float64(1)}
		if c.held != nil {
			sp["databases"] = c.held
		}
		cl := fakePlatform(t, client.Workload{ID: "box", Type: "vm-ubuntu", VM: sp})
		var diags diag.Diagnostics
		settleMachineDatabases(ctx, cl, "box", "machine", c.planned, vmBlock, &diags)
		if diags.HasError() || (notKept(diags) == 1) != c.warns || diags.WarningsCount() != notKept(diags) {
			t.Errorf("%s: %v", c.name, diags)
		}
	}
	var diags diag.Diagnostics
	settleMachineDatabases(ctx, client.New("http://127.0.0.1:1", "llc_test"), "box", "machine", machineDBs("a"), vmBlock, &diags)
	if diags.WarningsCount() > 0 || diags.HasError() {
		t.Errorf("unreadable workspace: %v", diags)
	}
}
