package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A machine's port groups: ports published together on the server's address.
// A group keeps its ports' own numbers inside the machine and publishes each
// at its number plus one offset the platform picks, so the gaps between them
// stay (Moonlight works its ports out from one); a mirrored group's numbers
// are the same inside and out, picked by the platform (count of them: FTP's
// passive range). The offset, and a mirrored group's ports, are the
// platform's: a group already there keeps them for its life, a new one gets
// them when it is saved. The platform keeps a machine's groups when a save
// leaves them out and removes them with an empty list, so they are sent only
// when the configuration writes them.
//
// Every attribute inside port_groups is computed, the ones a person writes
// too (name and port are checked in ValidateConfig instead): Terraform
// proposes null for an optional and computed nested attribute left out of the
// configuration whenever what state holds has a value that only a
// configuration sets, which would plan a machine's groups away instead of
// keeping them, and a mirrored group's numbers away on every plan.

const (
	maxPortGroups    = 8
	maxGroupPorts    = 64
	portGroupNameMax = 15
)

var portGroupNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

var groupPortAttrTypes = map[string]attr.Type{
	"port": types.Int64Type,
	"tcp":  types.BoolType,
	"udp":  types.BoolType,
}

var portGroupAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"mirrored":    types.BoolType,
	"count":       types.Int64Type,
	"tcp":         types.BoolType,
	"udp":         types.BoolType,
	"allow_cidrs": types.ListType{ElemType: types.StringType},
	"offset":      types.Int64Type,
	"ports":       types.ListType{ElemType: types.ObjectType{AttrTypes: groupPortAttrTypes}},
}

type portGroupModel struct {
	Name       types.String `tfsdk:"name"`
	Mirrored   types.Bool   `tfsdk:"mirrored"`
	Count      types.Int64  `tfsdk:"count"`
	TCP        types.Bool   `tfsdk:"tcp"`
	UDP        types.Bool   `tfsdk:"udp"`
	AllowCIDRs types.List   `tfsdk:"allow_cidrs"`
	Offset     types.Int64  `tfsdk:"offset"`
	Ports      types.List   `tfsdk:"ports"`
}

type groupPortModel struct {
	Port types.Int64 `tfsdk:"port"`
	TCP  types.Bool  `tfsdk:"tcp"`
	UDP  types.Bool  `tfsdk:"udp"`
}

var (
	portGroupType = types.ObjectType{AttrTypes: portGroupAttrTypes}
	groupPortType = types.ObjectType{AttrTypes: groupPortAttrTypes}
)

func (g portGroupModel) object() types.Object {
	return types.ObjectValueMust(portGroupAttrTypes, map[string]attr.Value{
		"name": g.Name, "mirrored": g.Mirrored, "count": g.Count, "tcp": g.TCP, "udp": g.UDP,
		"allow_cidrs": g.AllowCIDRs, "offset": g.Offset, "ports": g.Ports,
	})
}

func portGroupList(gs []portGroupModel) types.List {
	vals := make([]attr.Value, 0, len(gs))
	for _, g := range gs {
		vals = append(vals, g.object())
	}
	return types.ListValueMust(portGroupType, vals)
}

func groupPortList(ps []groupPortModel) types.List {
	vals := make([]attr.Value, 0, len(ps))
	for _, p := range ps {
		vals = append(vals, types.ObjectValueMust(groupPortAttrTypes, map[string]attr.Value{"port": p.Port, "tcp": p.TCP, "udp": p.UDP}))
	}
	return types.ListValueMust(groupPortType, vals)
}

// portGroupsAttribute is livellm_vm's port_groups.
func portGroupsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		Computed: true,
		Description: "Ports published together on the server's address (the host in ssh), at most 8 groups. A group " +
			"keeps its ports' own numbers inside the machine and publishes each at its number plus the group's " +
			"offset, which the platform picks, so the gaps between them stay (Moonlight works its ports out from " +
			"one). A mirrored group's numbers are the same inside and out, count of them picked by the platform " +
			"(FTP's passive range). Open to any address unless the group's allow_cidrs names some. Left out, the " +
			"machine keeps the groups it has; [] removes them. A machine publishes at most 64 ports, SSH and its " +
			"groups included.",
		Validators:    []validator.List{listvalidator.SizeAtMost(maxPortGroups)},
		PlanModifiers: []planmodifier.List{portGroupsPlan{}},
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Optional:    true,
					Computed:    true,
					Description: "The group's name, required: lowercase letters, digits and hyphens, at most 15, unique on the machine.",
					Validators: []validator.String{
						stringvalidator.LengthBetween(1, portGroupNameMax),
						stringvalidator.RegexMatches(portGroupNameRe, "must be lowercase letters, digits and hyphens, starting and ending with a letter or digit"),
					},
				},
				"mirrored": schema.BoolAttribute{
					Optional: true,
					Computed: true,
					Description: "Published at the same numbers as inside, which the platform picks: set count, leave " +
						"ports out, and read the numbers from ports. Default false.",
				},
				"count": schema.Int64Attribute{
					Optional:    true,
					Computed:    true,
					Description: "A mirrored group's size, 1..64.",
					Validators:  []validator.Int64{int64validator.Between(1, maxGroupPorts)},
				},
				"tcp": schema.BoolAttribute{
					Optional:    true,
					Computed:    true,
					Description: "A mirrored group's ports take TCP (neither tcp nor udp: TCP).",
				},
				"udp": schema.BoolAttribute{
					Optional:    true,
					Computed:    true,
					Description: "A mirrored group's ports take UDP.",
				},
				"allow_cidrs": schema.ListAttribute{
					Optional:    true,
					Computed:    true,
					ElementType: types.StringType,
					Description: "Who may connect (CIDRs); left out or empty: any address.",
				},
				"offset": schema.Int64Attribute{
					Computed: true,
					Description: "Each port is published at its number plus offset, picked by the platform when the " +
						"group is first saved and kept for its life (0 for a mirrored group).",
				},
				"ports": schema.ListNestedAttribute{
					Optional: true,
					Computed: true,
					Description: "The group's port numbers inside the machine, 1..64 of them. A mirrored group's are " +
						"the platform's: leave them out and read them here.",
					Validators: []validator.List{listvalidator.SizeAtMost(maxGroupPorts)},
					NestedObject: schema.NestedAttributeObject{
						Attributes: map[string]schema.Attribute{
							"port": schema.Int64Attribute{
								Optional:    true,
								Computed:    true,
								Description: "The number inside the machine, required.",
								Validators:  []validator.Int64{int64validator.Between(1, 65535)},
							},
							"tcp": schema.BoolAttribute{Optional: true, Computed: true, Description: "TCP (neither tcp nor udp: TCP)."},
							"udp": schema.BoolAttribute{Optional: true, Computed: true, Description: "UDP."},
						},
					},
				},
			},
		},
	}
}

// portGroupsPlan plans the groups: left out, what the machine has (none on a
// new machine); written, as written with false and no addresses for what is
// left out, and the platform's parts as the group already has them: its
// offset while it stays shifted, a mirrored group's ports while its count and
// protocols stay. A new group's, or a changed mirrored group's, are known
// after the apply. Groups are matched by name, never by place in the list.
type portGroupsPlan struct{}

func (portGroupsPlan) Description(context.Context) string {
	return "Keeps the machine's groups when left out; keeps the offset and a mirrored group's ports the platform picked."
}

func (m portGroupsPlan) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (portGroupsPlan) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.ConfigValue.IsNull() {
		keepWhenLeftOut{}.PlanModifyList(ctx, req, resp)
		return
	}
	if req.ConfigValue.IsUnknown() {
		return
	}
	var cfg, prior []portGroupModel
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &cfg, false)...)
	if known(req.StateValue) {
		resp.Diagnostics.Append(req.StateValue.ElementsAs(ctx, &prior, false)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.PlanValue = planPortGroups(ctx, cfg, prior, &resp.Diagnostics)
}

// planPortGroups is planned groups from the configured ones and the ones the
// machine has (prior; none on a create).
func planPortGroups(ctx context.Context, cfg, prior []portGroupModel, diags *diag.Diagnostics) types.List {
	had := map[string]portGroupModel{}
	for _, g := range prior {
		if known(g.Name) {
			had[g.Name.ValueString()] = g
		}
	}
	out := make([]portGroupModel, 0, len(cfg))
	for _, g := range cfg {
		p := g
		p.Mirrored, p.TCP, p.UDP = orFalse(g.Mirrored), orFalse(g.TCP), orFalse(g.UDP)
		if g.AllowCIDRs.IsNull() {
			p.AllowCIDRs = stringList(nil)
		}
		old, kept := portGroupModel{}, false
		if known(g.Name) {
			old, kept = had[g.Name.ValueString()]
		}
		switch {
		case !known(p.Mirrored):
			p.Offset = types.Int64Unknown()
			if g.Ports.IsNull() {
				p.Ports = types.ListUnknown(groupPortType)
			}
		case p.Mirrored.ValueBool():
			p.Offset = types.Int64Value(0)
			if g.Ports.IsNull() {
				p.Ports = types.ListUnknown(groupPortType)
				if kept && old.Mirrored.Equal(types.BoolValue(true)) && known(g.Count) && old.Count.Equal(g.Count) &&
					old.TCP.Equal(p.TCP) && old.UDP.Equal(p.UDP) && known(old.Ports) {
					p.Ports = old.Ports
				}
			}
		default:
			p.Offset = types.Int64Unknown()
			if kept && old.Mirrored.Equal(types.BoolValue(false)) && known(old.Offset) {
				p.Offset = old.Offset
			}
			p.Ports = planGroupPorts(ctx, g.Ports, diags)
		}
		out = append(out, p)
	}
	return portGroupList(out)
}

// planGroupPorts is a shifted group's ports as written, with false for a
// protocol left out; none when the configuration writes none (refused).
func planGroupPorts(ctx context.Context, l types.List, diags *diag.Diagnostics) types.List {
	if l.IsUnknown() {
		return l
	}
	var ps []groupPortModel
	if !l.IsNull() {
		diags.Append(l.ElementsAs(ctx, &ps, false)...)
	}
	for i := range ps {
		ps[i].TCP, ps[i].UDP = orFalse(ps[i].TCP), orFalse(ps[i].UDP)
	}
	return groupPortList(ps)
}

// orFalse is a bool left out as false.
func orFalse(b types.Bool) types.Bool {
	if b.IsNull() {
		return types.BoolValue(false)
	}
	return b
}

// readPortGroups is the machine's groups as the platform holds them: an empty
// list for none, false and no addresses for what it doesn't hold, the offset
// (0 for a mirrored group) and a mirrored group's ports as it picked them.
func readPortGroups(sp map[string]any) types.List {
	raw, _ := sp["portGroups"].([]any)
	gs := make([]portGroupModel, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		g := portGroupModel{
			Name:       types.StringValue(name),
			Mirrored:   types.BoolValue(m["mirrored"] == true),
			Count:      types.Int64Null(),
			TCP:        types.BoolValue(m["tcp"] == true),
			UDP:        types.BoolValue(m["udp"] == true),
			AllowCIDRs: stringList(anyStrings(m["allowCIDRs"])),
			Offset:     types.Int64Value(0),
		}
		if f, ok := number(m["count"]); ok && f > 0 {
			g.Count = types.Int64Value(int64(f))
		}
		if f, ok := number(m["offset"]); ok && m["mirrored"] != true {
			g.Offset = types.Int64Value(int64(f))
		}
		raw, _ := m["ports"].([]any)
		ps := make([]groupPortModel, 0, len(raw))
		for _, pe := range raw {
			pm, ok := pe.(map[string]any)
			if !ok {
				continue
			}
			if f, ok := number(pm["port"]); ok {
				ps = append(ps, groupPortModel{Port: types.Int64Value(int64(f)),
					TCP: types.BoolValue(pm["tcp"] == true), UDP: types.BoolValue(pm["udp"] == true)})
			}
		}
		g.Ports = groupPortList(ps)
		gs = append(gs, g)
	}
	return portGroupList(gs)
}

// anyStrings is a JSON list's strings.
func anyStrings(raw any) []string {
	l, _ := raw.([]any)
	out := make([]string, 0, len(l))
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// portGroupsSpec is the groups the configuration writes, an empty list
// included (that is how they are removed); nil when it leaves them out, and
// the platform keeps what the machine has. The offset, and a mirrored group's
// ports, are the platform's and never sent.
func portGroupsSpec(ctx context.Context, cfg types.List) []map[string]any {
	if cfg.IsNull() || cfg.IsUnknown() {
		return nil
	}
	var gs []portGroupModel
	cfg.ElementsAs(ctx, &gs, false)
	out := make([]map[string]any, 0, len(gs))
	for _, g := range gs {
		e := map[string]any{"name": g.Name.ValueString()}
		if g.Mirrored.ValueBool() {
			e["mirrored"] = true
			e["count"] = g.Count.ValueInt64()
			if g.TCP.ValueBool() {
				e["tcp"] = true
			}
			if g.UDP.ValueBool() {
				e["udp"] = true
			}
		} else {
			var ps []groupPortModel
			if known(g.Ports) {
				g.Ports.ElementsAs(ctx, &ps, false)
			}
			ports := make([]map[string]any, 0, len(ps))
			for _, p := range ps {
				pe := map[string]any{"port": p.Port.ValueInt64()}
				if p.TCP.ValueBool() {
					pe["tcp"] = true
				}
				if p.UDP.ValueBool() {
					pe["udp"] = true
				}
				ports = append(ports, pe)
			}
			e["ports"] = ports
		}
		if cidrs := listStrings(g.AllowCIDRs); len(cidrs) > 0 {
			e["allowCIDRs"] = cidrs
		}
		out = append(out, e)
	}
	return out
}

// portGroupErrors checks the groups as written, at plan time: {summary,
// detail}. What is not known yet, and where the platform can publish them, is
// left for the platform.
func portGroupErrors(ctx context.Context, l types.List) [][2]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var gs []portGroupModel
	if d := l.ElementsAs(ctx, &gs, false); d.HasError() {
		return nil
	}
	var errs [][2]string
	names := map[string]bool{}
	inside := map[int64]string{22: "SSH"}
	for i, g := range gs {
		name := g.Name.ValueString()
		switch {
		case g.Name.IsNull():
			errs = append(errs, [2]string{"A port group needs a name",
				fmt.Sprintf("Port group %d has no name: give each group one, lowercase letters, digits and hyphens.", i+1)})
		case known(g.Name):
			if names[name] {
				errs = append(errs, [2]string{"A port group named twice",
					fmt.Sprintf("Two groups are named %q; each group's name is its own on the machine.", name)})
			}
			names[name] = true
		}
		if g.Mirrored.IsUnknown() {
			continue
		}
		if g.Mirrored.ValueBool() {
			if g.Count.IsNull() {
				errs = append(errs, [2]string{"A mirrored group needs count",
					fmt.Sprintf("Group %q is mirrored: the platform picks its numbers. Set count (1..64), how many it has.", name)})
			}
			if !g.Ports.IsNull() {
				errs = append(errs, [2]string{"A mirrored group's ports are the platform's",
					fmt.Sprintf("Group %q is mirrored: the platform picks its numbers. Leave ports out and read them back.", name)})
			}
			continue
		}
		if !g.Count.IsNull() {
			errs = append(errs, [2]string{"count is for a mirrored group",
				fmt.Sprintf("Group %q keeps its ports' own numbers: list them in ports and leave count out, or set mirrored = true.", name)})
		}
		if g.TCP.ValueBool() || g.UDP.ValueBool() {
			errs = append(errs, [2]string{"tcp and udp on a group are for a mirrored group",
				fmt.Sprintf("Group %q lists its ports: set tcp or udp on each port instead.", name)})
		}
		if g.Ports.IsUnknown() {
			continue
		}
		var ps []groupPortModel
		if !g.Ports.IsNull() {
			g.Ports.ElementsAs(ctx, &ps, false)
		}
		if len(ps) == 0 {
			errs = append(errs, [2]string{"A group needs its ports",
				fmt.Sprintf("Group %q has no ports: list 1..64 port numbers, or set mirrored = true and count.", name)})
		}
		for _, p := range ps {
			if p.Port.IsNull() {
				errs = append(errs, [2]string{"A group port needs its number",
					fmt.Sprintf("A port of group %q has no port: write its number inside the machine.", name)})
				continue
			}
			if !known(p.Port) {
				continue
			}
			n := p.Port.ValueInt64()
			if what, taken := inside[n]; taken {
				errs = append(errs, [2]string{"A port in two places",
					fmt.Sprintf("Port %d of group %q is taken on this machine (%s).", n, name, what)})
				continue
			}
			inside[n] = "group " + name
		}
	}
	return errs
}

// settleVMPortGroups resolves what a write left unknown about the groups from
// the platform's copy (nil when it could not be read): each group's offset
// and a mirrored group's ports, by name. A group the platform doesn't hold is
// left with offset 0 and no ports and, when the write sent the groups, a
// warning says so: a platform without port groups drops them.
func settleVMPortGroups(ctx context.Context, m *vmResourceModel, sp map[string]any, sent bool, diags *diag.Diagnostics) {
	held := readPortGroups(sp)
	if m.PortGroups.IsUnknown() {
		m.PortGroups = held
		return
	}
	if m.PortGroups.IsNull() {
		return
	}
	var planned, have []portGroupModel
	m.PortGroups.ElementsAs(ctx, &planned, false)
	held.ElementsAs(ctx, &have, false)
	byName := map[string]portGroupModel{}
	for _, g := range have {
		byName[g.Name.ValueString()] = g
	}
	for i, g := range planned {
		h, ok := byName[g.Name.ValueString()]
		if g.Offset.IsUnknown() {
			planned[i].Offset = types.Int64Value(0)
			if ok {
				planned[i].Offset = h.Offset
			}
		}
		if g.Ports.IsUnknown() {
			planned[i].Ports = groupPortList(nil)
			if ok {
				planned[i].Ports = h.Ports
			}
		}
		if g.Mirrored.IsUnknown() {
			planned[i].Mirrored = types.BoolValue(ok && h.Mirrored.ValueBool())
		}
	}
	m.PortGroups = portGroupList(planned)
	if sent && sp != nil && !held.Equal(m.PortGroups) {
		diags.AddWarning("Port groups not kept as written",
			fmt.Sprintf("%q doesn't hold its port groups as written. A platform without port groups doesn't keep "+
				"them: the next plan shows the difference.", m.Name.ValueString()))
	}
}
