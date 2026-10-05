package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// A machine's or a Desktop App's database blocks: reach only. The link lets
// the machine reach the database inside the workspace; nothing is put into
// the machine (no variables) and nothing restarts. The platform keeps them as
// vm.databases / desktop.databases, [{id}], at most maxDatabaseLinks.

// machineDatabaseModel is one database block of a machine or a Desktop App.
type machineDatabaseModel struct {
	Name types.String `tfsdk:"name"`
}

var machineDatabaseAttrTypes = map[string]attr.Type{"name": types.StringType}

// machineDatabaseBlock is the database block; what names the resource kind
// ("machine", "Desktop App").
func machineDatabaseBlock(what string) schema.ListNestedBlock {
	return schema.ListNestedBlock{
		Description: fmt.Sprintf("A database of the workspace this %s may reach (at most %d). Reach only: nothing is "+
			"put into the %s (no variables) and nothing restarts; connect with the database's own address and login. "+
			"A database is reached only by what links it, and it can't be deleted while a %s links it. Through an "+
			"API key the link needs the Network permission unless the key made both this %s and the database.",
			what, maxDatabaseLinks, what, what, what),
		NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Required:    true,
					Description: "The database's name, e.g. livellm_storage.db.name.",
				},
			},
		},
	}
}

// machineDatabaseNames are the database blocks' names, in the order written;
// nil when there are none or the list isn't known.
func machineDatabaseNames(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	for _, e := range l.Elements() {
		o, ok := e.(types.Object)
		if !ok || o.IsNull() || o.IsUnknown() {
			continue
		}
		n, _ := o.Attributes()["name"].(types.String)
		out = append(out, n.ValueString())
	}
	return out
}

// machineDatabaseLinks is the databases list a write sends ([{id}], no env:
// a machine gets no variables), or nil when there are no blocks: a full save
// without blocks removes the links.
func machineDatabaseLinks(l types.List) []map[string]any {
	names := machineDatabaseNames(l)
	if len(names) == 0 {
		return nil
	}
	links := make([]map[string]any, 0, len(names))
	for _, n := range names {
		links = append(links, map[string]any{"id": n})
	}
	return links
}

// machineDatabaseErrors checks the blocks as written: at most 8, each
// database once. Names not known yet are left for the platform.
func machineDatabaseErrors(l types.List, what string) [][2]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out [][2]string
	elems := l.Elements()
	if len(elems) > maxDatabaseLinks {
		out = append(out, [2]string{"Too many databases",
			fmt.Sprintf("A %s links at most %d databases; this one has %d.", what, maxDatabaseLinks, len(elems))})
	}
	seen := map[string]bool{}
	for _, e := range elems {
		o, ok := e.(types.Object)
		if !ok || o.IsNull() || o.IsUnknown() {
			continue
		}
		n, _ := o.Attributes()["name"].(types.String)
		if n.IsNull() || n.IsUnknown() {
			continue
		}
		if seen[n.ValueString()] {
			out = append(out, [2]string{"Duplicate database",
				fmt.Sprintf("The database %q is linked twice: one block per database.", n.ValueString())})
		}
		seen[n.ValueString()] = true
	}
	return out
}

// readMachineDatabases maps the platform's links back into state, in its
// order, so a link changed in the console shows as drift and an import fills
// the blocks. Always a known list (empty without links), as an unwritten
// block list is.
func readMachineDatabases(sp map[string]any) types.List {
	objType := types.ObjectType{AttrTypes: machineDatabaseAttrTypes}
	raw, _ := sp["databases"].([]any)
	vals := make([]attr.Value, 0, len(raw))
	for _, e := range raw {
		l, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := l["id"].(string)
		if id == "" {
			continue
		}
		vals = append(vals, types.ObjectValueMust(machineDatabaseAttrTypes, map[string]attr.Value{
			"name": types.StringValue(id),
		}))
	}
	return types.ListValueMust(objType, vals)
}

// settleMachineDatabases checks after a write that the platform kept the
// database blocks as written. The state keeps the plan (Terraform holds an
// apply to it); when the platform holds other links, a platform older than
// this provider having dropped them, a warning says so at the apply instead
// of a difference turning up only at the next plan. block picks the
// workload's machine or desktop settings.
func settleMachineDatabases(ctx context.Context, c *client.Client, id, what string, planned types.List,
	block func(*client.Workload) map[string]any, diags *diag.Diagnostics) {
	if planned.IsUnknown() {
		return
	}
	ws, err := c.Workloads(ctx)
	if err != nil {
		return
	}
	w := findWorkload(ws, id)
	if w == nil {
		return
	}
	held := machineDatabaseNames(readMachineDatabases(block(w)))
	want := machineDatabaseNames(planned)
	if sameNames(held, want) {
		return
	}
	diags.AddWarning("Database links not kept as written",
		fmt.Sprintf("%q links %s, not %s. A platform older than this provider doesn't keep a %s's database "+
			"links, so the %s can't reach those databases: the next plan shows the difference.",
			id, describeLinks(held), describeLinks(want), what, what))
}

// describeLinks writes database names out for a message.
func describeLinks(names []string) string {
	if len(names) == 0 {
		return "no database"
	}
	return strings.Join(names, ", ")
}
