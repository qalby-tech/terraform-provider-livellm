package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Every attribute inside port_groups is computed. Terraform proposes null for
// an optional and computed nested attribute left out of the configuration
// when what state holds has any value only a configuration sets: one
// attribute in there that isn't computed would plan a machine's groups away
// when port_groups is left out, and a mirrored group's ports on every plan.
func TestPortGroupsEveryNestedAttributeComputed(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewVMResource().Schema(ctx, resource.SchemaRequest{}, &resp)
	pg, ok := resp.Schema.Attributes["port_groups"].(schema.ListNestedAttribute)
	if !ok || !pg.Optional || !pg.Computed || len(pg.PlanModifiers) != 1 {
		t.Fatalf("port_groups: %+v", resp.Schema.Attributes["port_groups"])
	}
	if _, ok := pg.PlanModifiers[0].(portGroupsPlan); !ok {
		t.Errorf("plan modifier %T", pg.PlanModifiers[0])
	}
	var walk func(prefix string, attrs map[string]schema.Attribute)
	walk = func(prefix string, attrs map[string]schema.Attribute) {
		for name, a := range attrs {
			if !a.IsComputed() || a.IsRequired() {
				t.Errorf("%s%s is not computed", prefix, name)
			}
			if n, ok := a.(schema.ListNestedAttribute); ok {
				walk(prefix+name+".", n.NestedObject.Attributes)
			}
		}
	}
	walk("port_groups.", pg.NestedObject.Attributes)
}

func group(name string) portGroupModel {
	return portGroupModel{
		Name: types.StringValue(name), Mirrored: types.BoolNull(), Count: types.Int64Null(), TCP: types.BoolNull(),
		UDP: types.BoolNull(), AllowCIDRs: types.ListNull(types.StringType), Offset: types.Int64Null(),
		Ports: types.ListNull(groupPortType),
	}
}

func ports(ps ...groupPortModel) types.List { return groupPortList(ps) }

func gport(n int64, udp bool) groupPortModel {
	p := groupPortModel{Port: types.Int64Value(n), TCP: types.BoolNull(), UDP: types.BoolNull()}
	if udp {
		p.UDP = types.BoolValue(true)
	}
	return p
}

func groupsOf(t *testing.T, l types.List) []portGroupModel {
	t.Helper()
	var gs []portGroupModel
	if d := l.ElementsAs(context.Background(), &gs, false); d.HasError() {
		t.Fatal(d)
	}
	return gs
}

// Planned: as written with false and [] for what is left out; a shifted
// group keeps its offset by name (a group added before it moves nothing); a
// mirrored group's offset is 0 and its ports stay while its count does; a new
// group's are known after the apply.
func TestPlanPortGroups(t *testing.T) {
	ctx := context.Background()
	moon := group("moon")
	moon.Ports = ports(gport(47989, false), gport(47998, true))
	ftp := group("ftp")
	ftp.Mirrored, ftp.Count = types.BoolValue(true), types.Int64Value(2)

	var diags diag.Diagnostics
	created := groupsOf(t, planPortGroups(ctx, []portGroupModel{moon, ftp}, nil, &diags))
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !created[0].Offset.IsUnknown() || !created[0].Mirrored.Equal(types.BoolValue(false)) ||
		!created[0].TCP.Equal(types.BoolValue(false)) || len(created[0].AllowCIDRs.Elements()) != 0 {
		t.Errorf("new shifted group: %+v", created[0])
	}
	if ps := groupPortsOf(t, created[0].Ports); len(ps) != 2 || !ps[0].TCP.Equal(types.BoolValue(false)) || !ps[1].UDP.Equal(types.BoolValue(true)) {
		t.Errorf("new shifted group's ports: %+v", ps)
	}
	if !created[1].Offset.Equal(types.Int64Value(0)) || !created[1].Ports.IsUnknown() {
		t.Errorf("new mirrored group: %+v", created[1])
	}

	// What the machine holds after that apply.
	heldMoon, heldFtp := created[0], created[1]
	heldMoon.Offset = types.Int64Value(30000)
	heldFtp.Ports = ports(groupPortModel{Port: types.Int64Value(41000), TCP: types.BoolValue(true), UDP: types.BoolValue(false)},
		groupPortModel{Port: types.Int64Value(41001), TCP: types.BoolValue(true), UDP: types.BoolValue(false)})
	prior := []portGroupModel{heldMoon, heldFtp}

	web := group("web")
	web.Ports = ports(gport(8000, false))
	got := groupsOf(t, planPortGroups(ctx, []portGroupModel{web, moon, ftp}, prior, &diags))
	if !got[0].Offset.IsUnknown() || !got[1].Offset.Equal(types.Int64Value(30000)) || !got[2].Ports.Equal(heldFtp.Ports) {
		t.Errorf("a group added first: %+v", got)
	}
	grown := ftp
	grown.Count = types.Int64Value(3)
	if got := groupsOf(t, planPortGroups(ctx, []portGroupModel{grown}, prior, &diags)); !got[0].Ports.IsUnknown() {
		t.Errorf("a mirrored group grown keeps its old ports: %+v", got[0])
	}
	turned := moon
	turned.Mirrored, turned.Count, turned.Ports = types.BoolValue(true), types.Int64Value(2), types.ListNull(groupPortType)
	if got := groupsOf(t, planPortGroups(ctx, []portGroupModel{turned}, prior, &diags)); !got[0].Ports.IsUnknown() || !got[0].Offset.Equal(types.Int64Value(0)) {
		t.Errorf("a shifted group turned mirrored: %+v", got[0])
	}
}

func groupPortsOf(t *testing.T, l types.List) []groupPortModel {
	t.Helper()
	var ps []groupPortModel
	if d := l.ElementsAs(context.Background(), &ps, false); d.HasError() {
		t.Fatal(d)
	}
	return ps
}

// Read: as the platform holds the groups, false and [] for what it doesn't,
// the offset (0 on a mirrored group) and a mirrored group's ports.
func TestReadPortGroups(t *testing.T) {
	var sp map[string]any
	_ = json.Unmarshal([]byte(`{"portGroups":[
		{"name":"moon","offset":30100,"allowCIDRs":["203.0.113.0/24"],"ports":[{"port":47989},{"port":47998,"udp":true}]},
		{"name":"ftp","mirrored":true,"count":2,"ports":[{"port":41000,"tcp":true},{"port":41001,"tcp":true}]}]}`), &sp)
	gs := groupsOf(t, readPortGroups(sp))
	if len(gs) != 2 {
		t.Fatalf("%+v", gs)
	}
	m, f := gs[0], gs[1]
	if m.Offset.ValueInt64() != 30100 || m.Mirrored.ValueBool() || !m.Count.IsNull() || listStrings(m.AllowCIDRs)[0] != "203.0.113.0/24" ||
		!m.TCP.Equal(types.BoolValue(false)) {
		t.Errorf("moon: %+v", m)
	}
	if ps := groupPortsOf(t, m.Ports); ps[1].Port.ValueInt64() != 47998 || !ps[1].UDP.ValueBool() || ps[0].TCP.ValueBool() {
		t.Errorf("moon's ports: %+v", ps)
	}
	if !f.Mirrored.ValueBool() || f.Count.ValueInt64() != 2 || f.Offset.ValueInt64() != 0 || len(f.Ports.Elements()) != 2 ||
		f.AllowCIDRs.IsNull() || len(f.AllowCIDRs.Elements()) != 0 {
		t.Errorf("ftp: %+v", f)
	}
	if got := readPortGroups(map[string]any{}); got.IsNull() || len(got.Elements()) != 0 {
		t.Errorf("none: %v", got)
	}
}

// Sent only when written, [] included; never the offset, never a mirrored
// group's ports, never a protocol or an address list left out.
func TestPortGroupsSpec(t *testing.T) {
	ctx := context.Background()
	if got := portGroupsSpec(ctx, types.ListNull(portGroupType)); got != nil {
		t.Errorf("left out: %v", got)
	}
	if got := portGroupsSpec(ctx, portGroupList(nil)); got == nil || len(got) != 0 {
		t.Errorf("[]: %v", got)
	}
	moon := group("moon")
	moon.Offset = types.Int64Value(30000)
	moon.Ports = ports(gport(47989, false), gport(47998, true))
	moon.AllowCIDRs = stringList([]string{"203.0.113.0/24"})
	ftp := group("ftp")
	ftp.Mirrored, ftp.Count, ftp.UDP = types.BoolValue(true), types.Int64Value(2), types.BoolValue(true)
	ftp.Ports = ports(gport(41000, false))
	b, _ := json.Marshal(portGroupsSpec(ctx, portGroupList([]portGroupModel{moon, ftp})))
	want := `[{"allowCIDRs":["203.0.113.0/24"],"name":"moon","ports":[{"port":47989},{"port":47998,"udp":true}]},` +
		`{"count":2,"mirrored":true,"name":"ftp","udp":true}]`
	if string(b) != want {
		t.Errorf("sent %s\nwant %s", b, want)
	}
}

func TestPortGroupErrors(t *testing.T) {
	ctx := context.Background()
	moon := group("moon")
	moon.Ports = ports(gport(47989, false))
	ftp := group("ftp")
	ftp.Mirrored, ftp.Count = types.BoolValue(true), types.Int64Value(2)
	if errs := portGroupErrors(ctx, portGroupList([]portGroupModel{moon, ftp})); len(errs) != 0 {
		t.Errorf("fine: %v", errs)
	}
	noCount := group("a")
	noCount.Mirrored, noCount.Ports = types.BoolValue(true), ports(gport(1, false))
	noPorts := group("b")
	counted := group("c")
	counted.Count, counted.TCP, counted.Ports = types.Int64Value(2), types.BoolValue(true), ports(gport(22, false), gport(3000, false))
	twice := group("moon")
	twice.Ports = ports(gport(3000, false))
	nameless := group("x")
	nameless.Name = types.StringNull()
	nameless.Ports = ports(gport(4000, false))
	var got []string
	for _, e := range portGroupErrors(ctx, portGroupList([]portGroupModel{moon, noCount, noPorts, counted, twice, nameless})) {
		got = append(got, e[0])
	}
	for _, want := range []string{"A mirrored group needs count", "A mirrored group's ports are the platform's", "A group needs its ports",
		"count is for a mirrored group", "tcp and udp on a group are for a mirrored group", "A port in two places", "A port group named twice",
		"A port group needs a name"} {
		if !strings.Contains(strings.Join(got, "|"), want) {
			t.Errorf("no %q in %v", want, got)
		}
	}
	// 22 (SSH) and 3000 twice: two ports in two places.
	if n := strings.Count(strings.Join(got, "|"), "A port in two places"); n != 2 {
		t.Errorf("ports in two places: %d (%v)", n, got)
	}
}

// After a write: a new group's offset and a mirrored group's ports come from
// the platform by name; a platform that kept none warns and leaves them 0
// and empty, never unknown.
func TestSettleVMPortGroups(t *testing.T) {
	ctx := context.Background()
	moon := group("moon")
	moon.Ports = ports(gport(47989, false))
	ftp := group("ftp")
	ftp.Mirrored, ftp.Count = types.BoolValue(true), types.Int64Value(1)
	var diags diag.Diagnostics
	planned := planPortGroups(ctx, []portGroupModel{moon, ftp}, nil, &diags)
	var sp map[string]any
	_ = json.Unmarshal([]byte(`{"portGroups":[{"name":"moon","offset":30000,"ports":[{"port":47989}]},
		{"name":"ftp","mirrored":true,"count":1,"ports":[{"port":41000,"tcp":true}]}]}`), &sp)
	m := vmResourceModel{Name: types.StringValue("box"), PortGroups: planned}
	settleVMPortGroups(ctx, &m, sp, true, &diags)
	gs := groupsOf(t, m.PortGroups)
	if gs[0].Offset.ValueInt64() != 30000 || groupPortsOf(t, gs[1].Ports)[0].Port.ValueInt64() != 41000 || diags.WarningsCount() != 0 {
		t.Errorf("settled: %+v %v", gs, diags)
	}
	if !m.PortGroups.Equal(readPortGroups(sp)) {
		t.Errorf("settled %v\nheld %v", m.PortGroups, readPortGroups(sp))
	}
	m = vmResourceModel{Name: types.StringValue("box"), PortGroups: planned}
	settleVMPortGroups(ctx, &m, map[string]any{}, true, &diags)
	gs = groupsOf(t, m.PortGroups)
	if gs[0].Offset.ValueInt64() != 0 || gs[1].Ports.IsUnknown() || diags.WarningsCount() != 1 {
		t.Errorf("not kept: %+v %v", gs, diags)
	}
}
