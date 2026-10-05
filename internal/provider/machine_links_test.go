package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
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

// Through the resources: a create sends the links flat, a refresh reads the
// platform's, and an update without blocks sends none (the links go).
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
	for _, c := range reachResources() {
		if c.name != "vm" && c.name != "desktop_app" {
			continue
		}
		r := c.r()
		stored := map[string]any{}
		p := &recordingPlatform{w: client.Workload{Type: c.wtype}, block: func(w *client.Workload, sp map[string]any) {
			for k, v := range stored {
				sp[k] = v
			}
			c.block(w, sp)
		}}
		r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
			ProviderData: &providerData{Client: p.serve(t)},
		}, &resource.ConfigureResponse{})
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		vals := map[string]tftypes.Value{"name": tftypes.NewValue(tftypes.String, "res"), "database": dbVal("app-db")}
		for k, v := range c.vals {
			vals[k] = v
		}
		extra := map[string]tftypes.Value{}
		if c.name == "vm" {
			// a vm block with something in it (an empty one isn't sent)
			extra["cpus"] = tftypes.NewValue(tftypes.Number, 2)
		} else {
			extra["cpu"] = tftypes.NewValue(tftypes.String, "2")
		}
		for k, v := range extra {
			vals[k] = v
		}
		cfg, plan := plannedValues(ctx, sr.Schema, vals)
		resp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
		r.Create(ctx, resource.CreateRequest{Config: cfg, Plan: plan}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: create: %v", c.name, resp.Diagnostics)
			continue
		}
		if got := p.writes[0]["databases"]; !reflect.DeepEqual(got, []any{map[string]any{"id": "app-db"}}) {
			t.Errorf("%s: create sent databases %v", c.name, got)
		}
		// The console added a second link: a refresh reads both.
		stored["databases"] = []any{map[string]any{"id": "app-db"}, map[string]any{"id": "cache"}}
		p.mu.Lock()
		p.block(&p.w, kindBlock(p.w.Type))
		p.mu.Unlock()
		rr := resource.ReadResponse{State: resp.State}
		r.Read(ctx, resource.ReadRequest{State: resp.State}, &rr)
		var got types.List
		rr.State.GetAttribute(ctx, path.Root("database"), &got)
		if rr.Diagnostics.HasError() || !got.Equal(machineDBs("app-db", "cache")) {
			t.Errorf("%s: refresh read %v (%v)", c.name, got, rr.Diagnostics)
		}
		// An update whose configuration has no blocks sends no links.
		var obj map[string]tftypes.Value
		rr.State.Raw.As(&obj)
		obj["database"] = dbVal()
		uvals := map[string]tftypes.Value{"name": vals["name"]}
		for k, v := range c.vals {
			uvals[k] = v
		}
		for k, v := range extra {
			uvals[k] = v
		}
		ucfg, _ := plannedValues(ctx, sr.Schema, uvals)
		uplan := tfsdk.Plan{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), obj)}
		p.writes = nil
		uresp := resource.UpdateResponse{State: rr.State}
		r.Update(ctx, resource.UpdateRequest{Config: ucfg, Plan: uplan, State: rr.State}, &uresp)
		if uresp.Diagnostics.HasError() || len(p.writes) != 1 {
			t.Errorf("%s: update: %v (%d writes)", c.name, uresp.Diagnostics, len(p.writes))
			continue
		}
		block := map[string]string{"vm": "vm", "desktop_app": "desktop"}[c.name]
		kind, _ := p.writes[0][block].(map[string]any)
		if kind == nil {
			t.Errorf("%s: update sent no %s block", c.name, block)
		}
		if _, ok := kind["databases"]; ok {
			t.Errorf("%s: update without blocks sent %v", c.name, kind["databases"])
		}
	}
}
