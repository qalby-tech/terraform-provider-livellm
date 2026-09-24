package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestReadDefaulted(t *testing.T) {
	null := types.StringNull()
	cases := []struct {
		name string
		prev types.String
		api  string
		want types.String
	}{
		{"unset stays unset when the api echoes the default", null, "Dockerfile", null},
		{"unset stays unset when the api reports nothing", null, "", null},
		{"unset picks up a real change made elsewhere", null, "build/Dockerfile", types.StringValue("build/Dockerfile")},
		{"explicit default survives an api that strips it", types.StringValue("Dockerfile"), "", types.StringValue("Dockerfile")},
		{"explicit value cleared elsewhere shows as drift", types.StringValue("x"), "", null},
		{"explicit value round-trips", types.StringValue("x"), "x", types.StringValue("x")},
	}
	for _, c := range cases {
		if got := readDefaulted(c.prev, c.api, "Dockerfile"); !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestReadEnvMap(t *testing.T) {
	prev := types.MapValueMust(types.StringType, map[string]attr.Value{"A": types.StringValue("secret-a")})
	got := readEnvMap(prev, []any{
		map[string]any{"name": "A"},
		map[string]any{"name": "B"},
	}, false)
	el := got.Elements()
	if el["A"].(types.String).ValueString() != "secret-a" {
		t.Errorf("stored value not kept: %v", el["A"])
	}
	if el["B"].(types.String).ValueString() != "" {
		t.Errorf("unknown name should read as empty: %v", el["B"])
	}
	if !readEnvMap(types.MapNull(types.StringType), nil, false).IsNull() {
		t.Error("null stays null when the api reports nothing")
	}
	if !readEnvMap(prev, nil, false).IsNull() {
		t.Error("a populated map must show drift when the api reports nothing")
	}
	plain := readEnvMap(types.MapNull(types.StringType), []any{map[string]any{"name": "X", "value": "1"}}, true)
	if plain.Elements()["X"].(types.String).ValueString() != "1" {
		t.Error("plain env values come from the api")
	}
}

func TestContainerAppSpec(t *testing.T) {
	ctx := context.Background()
	m := containerAppModel{
		Name: types.StringValue("api"),
		Source: &sourceModel{
			Token: types.StringValue("tok"),
			Git: &gitSourceModel{
				URL:        types.StringValue("https://github.com/acme/api.git"),
				Ref:        types.StringValue("main"),
				Dockerfile: types.StringNull(),
				Context:    types.StringValue("services/api"),
			},
		},
		SecretEnv: types.MapValueMust(types.StringType, map[string]attr.Value{
			"B": types.StringValue("2"), "A": types.StringValue("1"),
		}),
	}
	spec := containerAppSpec(ctx, m, true)
	src := spec["source"].(map[string]any)
	git := src["git"].(map[string]any)
	if git["url"] != "https://github.com/acme/api.git" || git["ref"] != "main" || git["context"] != "services/api" {
		t.Errorf("git block: %v", git)
	}
	if _, ok := git["dockerfile"]; ok {
		t.Error("unset dockerfile must be omitted")
	}
	if src["gitAuth"].(map[string]any)["token"] != "tok" {
		t.Errorf("token not sent: %v", src)
	}
	if _, ok := containerAppSpec(ctx, m, false)["source"].(map[string]any)["gitAuth"]; ok {
		t.Error("token must be omitted when unchanged")
	}
	se := spec["secretEnv"].([]map[string]any)
	if len(se) != 2 || se[0]["name"] != "A" || se[0]["value"] != "1" || se[1]["name"] != "B" {
		t.Errorf("secretEnv: %v", se)
	}
	if _, ok := spec["image"]; ok {
		t.Error("no image for a source app")
	}
}

func TestContainerAppImageAuth(t *testing.T) {
	ctx := context.Background()
	m := containerAppModel{
		Name:  types.StringValue("grafana"),
		Image: types.StringValue("ghcr.io/acme/grafana:11"),
		ImageAuth: &imageAuthModel{
			Username: types.StringValue("acme-bot"),
			Password: types.StringValue("ghp_x"),
		},
	}
	spec := containerAppSpec(ctx, m, true)
	auth, ok := spec["imageAuth"].(map[string]any)
	if !ok || auth["username"] != "acme-bot" || auth["password"] != "ghp_x" {
		t.Fatalf("imageAuth: %v", spec["imageAuth"])
	}
	if s, _ := appShapeError(m); s != "" {
		t.Errorf("image + image_auth must be valid, got %q", s)
	}

	withSource := m
	withSource.Image = types.StringNull()
	withSource.Source = &sourceModel{Git: &gitSourceModel{URL: types.StringValue("https://github.com/acme/api.git")}}
	if s, _ := appShapeError(withSource); s != "Conflicting image_auth and source" {
		t.Errorf("source + image_auth must be rejected, got %q", s)
	}
	if _, ok := containerAppSpec(ctx, withSource, true)["imageAuth"]; ok {
		t.Error("imageAuth must never be sent for a source app")
	}

	if _, ok := containerAppSpec(ctx, containerAppModel{Image: types.StringValue("nginx")}, true)["imageAuth"]; ok {
		t.Error("no image_auth block, no imageAuth")
	}

	prev := &imageAuthModel{Username: types.StringValue("old"), Password: types.StringValue("kept")}
	got := readImageAuth(prev, map[string]any{"username": "acme-bot"})
	if got == nil || got.Username.ValueString() != "acme-bot" || got.Password.ValueString() != "kept" {
		t.Errorf("read keeps the password from state and takes the username from the api: %+v", got)
	}
	if readImageAuth(prev, nil) != nil {
		t.Error("api without imageAuth reads as no block (drift when configured)")
	}
	if got := readImageAuth(nil, map[string]any{"username": "u"}); got == nil || !got.Password.IsNull() {
		t.Errorf("import without state leaves the password null: %+v", got)
	}
}

// A block's needs are checked only when the block is there: an app that just
// runs an image must not be asked for registry credentials or a repo.
func TestAppConfigErrors(t *testing.T) {
	str := types.StringValue
	null := types.StringNull()
	unknown := types.StringUnknown()
	git := func(url types.String) *sourceModel { return &sourceModel{Git: &gitSourceModel{URL: url}} }
	cases := []struct {
		name string
		m    containerAppModel
		want string
	}{
		{"an image alone", containerAppModel{Image: str("nginx")}, ""},
		{"a repo alone", containerAppModel{Image: null, Source: git(str("https://github.com/acme/app"))}, ""},
		{"a private image", containerAppModel{Image: str("x"), ImageAuth: &imageAuthModel{Username: str("bot"), Password: unknown}}, ""},
		{"an image from a variable", containerAppModel{Image: unknown}, ""},
		{"neither", containerAppModel{Image: null}, "Missing image"},
		{"both", containerAppModel{Image: str("nginx"), Source: git(str("https://github.com/acme/app"))}, "Conflicting image and source"},
		{"a source block without a repo", containerAppModel{Image: null, Source: &sourceModel{}}, "Missing source.git.url"},
		{"a git block without a url", containerAppModel{Image: null, Source: git(null)}, "Missing source.git.url"},
		{"credentials without a password", containerAppModel{Image: str("x"), ImageAuth: &imageAuthModel{Username: str("bot"), Password: null}}, "Incomplete image_auth"},
		{"credentials for a repo", containerAppModel{Image: null, Source: git(str("https://github.com/acme/app")), ImageAuth: &imageAuthModel{Username: str("bot"), Password: str("pw")}}, "Conflicting image_auth and source"},
	}
	for _, c := range cases {
		got := ""
		if errs := appConfigErrors(c.m); len(errs) > 0 {
			got = errs[0][0]
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// A service of a composed app carries its stack, its hostname, what it
// starts after and which ports are internal — the settings that were lost on
// every apply before 0.6.0.
func TestContainerAppSpecStack(t *testing.T) {
	ctx := context.Background()
	ports := portList(t, []appPortModel{
		{Name: types.StringValue("pg"), Port: types.Int64Value(5432), Internal: types.BoolValue(true)},
		{Name: types.StringValue("http"), Port: types.Int64Value(80), Internal: types.BoolNull()},
	})
	after, _ := types.ListValueFrom(ctx, types.StringType, []string{"shop-cache"})
	m := containerAppModel{
		Name: types.StringValue("shop-db"), Image: types.StringValue("postgres:17"),
		Stack: types.StringValue("shop"), Hostname: types.StringValue("db"), StartsAfter: after, Port: ports,
	}
	spec := containerAppSpec(ctx, m, false)
	if spec["stack"] != "shop" || spec["hostname"] != "db" {
		t.Errorf("stack/hostname: %v %v", spec["stack"], spec["hostname"])
	}
	if got, _ := spec["dependsOn"].([]string); len(got) != 1 || got[0] != "shop-cache" {
		t.Errorf("dependsOn: %v", spec["dependsOn"])
	}
	got := spec["ports"].([]map[string]any)
	if got[0]["internal"] != true || got[1]["internal"] != nil {
		t.Errorf("ports: %v", got)
	}
	// without a stack nothing about a stack is sent, and the hostname settles to nothing
	m2 := containerAppModel{Name: types.StringValue("web"), Image: types.StringValue("nginx"), Hostname: types.StringUnknown()}
	if spec := containerAppSpec(ctx, m2, false); spec["stack"] != nil || spec["hostname"] != nil || spec["dependsOn"] != nil {
		t.Errorf("a lone app sent stack fields: %v", spec)
	}
	settleHostname(&m2)
	if !m2.Hostname.IsNull() {
		t.Errorf("hostname without a stack: %v", m2.Hostname)
	}
	m.Hostname = types.StringUnknown()
	settleHostname(&m)
	if m.Hostname.ValueString() != "shop-db" {
		t.Errorf("hostname defaulted to %q, want the name", m.Hostname.ValueString())
	}
}

var appPortAttrTypes = map[string]attr.Type{
	"name": types.StringType, "port": types.Int64Type, "tcp": types.BoolType, "udp": types.BoolType,
	"internal": types.BoolType, "allow_cidrs": types.ListType{ElemType: types.StringType},
}

// portList builds a port block list the way Terraform hands it over; unset
// fields are null.
func portList(t *testing.T, ports []appPortModel) types.List {
	t.Helper()
	for i := range ports {
		p := &ports[i]
		for _, b := range []*types.Bool{&p.TCP, &p.UDP, &p.Internal} {
			if *b == (types.Bool{}) {
				*b = types.BoolNull()
			}
		}
		if p.AllowCIDRs.ElementType(context.Background()) == nil {
			p.AllowCIDRs = types.ListNull(types.StringType)
		}
	}
	l, d := types.ListValueFrom(context.Background(), types.ObjectType{AttrTypes: appPortAttrTypes}, ports)
	if d.HasError() {
		t.Fatalf("port list: %v", d)
	}
	return l
}

func volumeList(t *testing.T, vols []appVolumeModel) types.List {
	t.Helper()
	l, d := types.ListValueFrom(context.Background(), types.ObjectType{AttrTypes: appVolumeAttrTypes}, vols)
	if d.HasError() {
		t.Fatalf("volume list: %v", d)
	}
	return l
}

func vol(name string, gi int64, mount string) appVolumeModel {
	return appVolumeModel{Name: types.StringValue(name), SizeGi: types.Int64Value(gi), MountPath: types.StringValue(mount)}
}

func cidrs(v ...string) types.List {
	l, _ := types.ListValueFrom(context.Background(), types.StringType, v)
	return l
}

// Raw ports carry tcp or udp and their allow-list as access.allowCIDRs;
// volumes go out with their size in Gi.
func TestContainerAppSpecRawPortsAndVolumes(t *testing.T) {
	ctx := context.Background()
	m := containerAppModel{
		Name: types.StringValue("game"), Image: types.StringValue("itzg/minecraft-server"),
		Port: portList(t, []appPortModel{
			{Name: types.StringValue("mc"), Port: types.Int64Value(25565), TCP: types.BoolValue(true), AllowCIDRs: cidrs("203.0.113.0/24")},
			{Name: types.StringValue("voice"), Port: types.Int64Value(9987), UDP: types.BoolValue(true)},
			{Name: types.StringValue("http"), Port: types.Int64Value(8080), AllowCIDRs: cidrs()},
		}),
		Volume: volumeList(t, []appVolumeModel{vol("world", 20, "/data"), vol("logs", 1, "/var/log/mc")}),
	}
	spec := containerAppSpec(ctx, m, false)
	ports := spec["ports"].([]map[string]any)
	if ports[0]["tcp"] != true || ports[0]["udp"] != nil {
		t.Errorf("tcp port: %v", ports[0])
	}
	acc, _ := ports[0]["access"].(map[string]any)
	if got, _ := acc["allowCIDRs"].([]string); len(got) != 1 || got[0] != "203.0.113.0/24" {
		t.Errorf("tcp port allow-list: %v", ports[0]["access"])
	}
	if ports[1]["udp"] != true || ports[1]["tcp"] != nil || ports[1]["access"] != nil {
		t.Errorf("udp port: %v", ports[1])
	}
	if ports[2]["tcp"] != nil || ports[2]["access"] != nil {
		t.Errorf("an empty allow-list sends no access rules: %v", ports[2])
	}
	vols := spec["volumes"].([]map[string]any)
	if len(vols) != 2 || vols[0]["name"] != "world" || vols[0]["size"] != "20Gi" || vols[0]["mountPath"] != "/data" || vols[1]["size"] != "1Gi" {
		t.Errorf("volumes: %v", vols)
	}
	if _, ok := spec["storage"]; ok {
		t.Error("the old storage form is never sent")
	}
}

func TestPortErrors(t *testing.T) {
	ctx := context.Background()
	str, yes := types.StringValue, types.BoolValue(true)
	p := func(name string) appPortModel {
		return appPortModel{Name: str(name), Port: types.Int64Value(1000), AllowCIDRs: types.ListNull(types.StringType)}
	}
	with := func(m appPortModel, f func(*appPortModel)) appPortModel { f(&m); return m }
	unknownCIDR, _ := types.ListValue(types.StringType, []attr.Value{types.StringUnknown()})
	cases := []struct {
		name  string
		ports []appPortModel
		want  string
	}{
		{"http", []appPortModel{p("http")}, ""},
		{"tcp with an allow-list", []appPortModel{with(p("mc"), func(m *appPortModel) { m.TCP = yes; m.AllowCIDRs = cidrs("10.0.0.0/8", "203.0.113.7/32") })}, ""},
		{"udp open to all", []appPortModel{with(p("wg"), func(m *appPortModel) { m.UDP = yes })}, ""},
		{"http with an allow-list", []appPortModel{with(p("http"), func(m *appPortModel) { m.AllowCIDRs = cidrs("203.0.113.0/24") })}, ""},
		{"an address from a variable", []appPortModel{with(p("mc"), func(m *appPortModel) { m.TCP = yes; m.AllowCIDRs = unknownCIDR })}, ""},
		{"tcp and udp", []appPortModel{with(p("x"), func(m *appPortModel) { m.TCP = yes; m.UDP = yes })}, "Conflicting tcp and udp"},
		{"internal tcp", []appPortModel{with(p("x"), func(m *appPortModel) { m.TCP = yes; m.Internal = yes })}, "Conflicting internal and tcp/udp"},
		{"internal udp", []appPortModel{with(p("x"), func(m *appPortModel) { m.UDP = yes; m.Internal = yes })}, "Conflicting internal and tcp/udp"},
		{"internal with an allow-list", []appPortModel{with(p("pg"), func(m *appPortModel) { m.Internal = yes; m.AllowCIDRs = cidrs("10.0.0.0/8") })}, "allow_cidrs on an internal port"},
		{"not a cidr", []appPortModel{with(p("mc"), func(m *appPortModel) { m.TCP = yes; m.AllowCIDRs = cidrs("203.0.113.7") })}, "Invalid allow_cidrs"},
		{"a name too long", []appPortModel{p("a-very-long-port-name")}, "Invalid port name"},
		{"the same name twice", []appPortModel{p("http"), p("http")}, "Duplicate port name"},
	}
	for _, c := range cases {
		got := ""
		if errs := portErrors(ctx, c.ports); len(errs) > 0 {
			got = errs[0][0]
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVolumeErrors(t *testing.T) {
	nine := make([]appVolumeModel, 9)
	for i := range nine {
		nine[i] = vol("v"+string(rune('a'+i)), 1, "/v/"+string(rune('a'+i)))
	}
	cases := []struct {
		name string
		vols []appVolumeModel
		want string
	}{
		{"one", []appVolumeModel{vol("data", 10, "/data")}, ""},
		{"siblings", []appVolumeModel{vol("a", 1, "/srv/a"), vol("b", 1, "/srv/ab")}, ""},
		{"eight", nine[:8], ""},
		{"a path from a variable", []appVolumeModel{{Name: types.StringValue("a"), SizeGi: types.Int64Value(1), MountPath: types.StringUnknown()}}, ""},
		{"nine", nine, "Too many volumes"},
		{"a bad name", []appVolumeModel{vol("Data", 1, "/data")}, "Invalid volume name"},
		{"a long name", []appVolumeModel{vol("sixteen-chars-xx", 1, "/data")}, "Invalid volume name"},
		{"the same name twice", []appVolumeModel{vol("data", 1, "/a"), vol("data", 1, "/b")}, "Duplicate volume name"},
		{"no size", []appVolumeModel{vol("data", 0, "/data")}, "Invalid volume size"},
		{"a relative path", []appVolumeModel{vol("data", 1, "data")}, "Invalid mount_path"},
		{"the root", []appVolumeModel{vol("data", 1, "/")}, "Invalid mount_path"},
		{"the root, spelled twice", []appVolumeModel{vol("data", 1, "//")}, "Invalid mount_path"},
		{"the same path", []appVolumeModel{vol("a", 1, "/data"), vol("b", 1, "/data")}, "Duplicate mount_path"},
		{"a trailing slash", []appVolumeModel{vol("a", 1, "/data/")}, "Invalid mount_path"},
		{"a doubled slash", []appVolumeModel{vol("a", 1, "/srv//data")}, "Invalid mount_path"},
		{"a dot-dot folder", []appVolumeModel{vol("a", 1, "/srv/../data")}, "Invalid mount_path"},
		{"a space in a folder", []appVolumeModel{vol("a", 1, "/my data")}, "Invalid mount_path"},
		{"a system folder", []appVolumeModel{vol("a", 1, "/proc/x")}, "Invalid mount_path"},
		{"/dev itself", []appVolumeModel{vol("a", 1, "/dev")}, "Invalid mount_path"},
		{"a long path", []appVolumeModel{vol("a", 1, "/"+strings.Repeat("a", 200))}, "Invalid mount_path"},
		{"odd but allowed characters", []appVolumeModel{vol("a", 1, "/srv/.cache/app@2+x_y-z")}, ""},
		{"/device is not /dev", []appVolumeModel{vol("a", 1, "/device")}, ""},
		{"one inside the other", []appVolumeModel{vol("a", 1, "/data"), vol("b", 1, "/data/cache")}, "Nested mount_path"},
		{"the other inside the one", []appVolumeModel{vol("b", 1, "/data/cache"), vol("a", 1, "/data")}, "Nested mount_path"},
	}
	for _, c := range cases {
		got := ""
		if errs := volumeErrors(c.vols); len(errs) > 0 {
			got = errs[0][0]
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// Growing is fine, shrinking is refused, and a volume that leaves the
// configuration is named in a warning before its data goes.
func TestVolumePlanChecks(t *testing.T) {
	had := []appVolumeModel{vol("data", 10, "/data"), vol("cache", 5, "/cache")}
	if errs, warns := volumePlanChecks([]appVolumeModel{vol("data", 20, "/data"), vol("cache", 5, "/cache")}, had); len(errs)+len(warns) != 0 {
		t.Errorf("growing: %v %v", errs, warns)
	}
	errs, _ := volumePlanChecks([]appVolumeModel{vol("data", 5, "/data"), vol("cache", 5, "/cache")}, had)
	if len(errs) != 1 || errs[0][0] != "A volume can't shrink" || !strings.Contains(errs[0][1], `"data" is 10 GiB`) {
		t.Errorf("shrinking: %v", errs)
	}
	errs, warns := volumePlanChecks([]appVolumeModel{vol("data", 10, "/data")}, had)
	if len(errs) != 0 || len(warns) != 1 || !strings.Contains(warns[0][1], `"cache"`) {
		t.Errorf("removing: %v %v", errs, warns)
	}
	if _, warns := volumePlanChecks(nil, had); len(warns) != 2 {
		t.Errorf("removing all: %v", warns)
	}
	if errs, warns := volumePlanChecks([]appVolumeModel{vol("new", 1, "/new")}, nil); len(errs)+len(warns) != 0 {
		t.Errorf("a first volume: %v %v", errs, warns)
	}
}

func TestReadVolumes(t *testing.T) {
	get := func(l types.List) []appVolumeModel {
		var out []appVolumeModel
		l.ElementsAs(context.Background(), &out, false)
		return out
	}
	got := get(readVolumes(types.ListNull(types.ObjectType{AttrTypes: appVolumeAttrTypes}), map[string]any{
		"volumes": []any{
			map[string]any{"name": "world", "size": "20Gi", "mountPath": "/data"},
			map[string]any{"name": "big", "size": "2Ti", "mountPath": "/big"},
		},
	}))
	if len(got) != 2 || got[0].Name.ValueString() != "world" || got[0].SizeGi.ValueInt64() != 20 || got[1].SizeGi.ValueInt64() != 2048 {
		t.Errorf("volumes: %+v", got)
	}
	// an app from before volumes existed: its one disk is the volume "data"
	got = get(readVolumes(types.ListNull(types.ObjectType{AttrTypes: appVolumeAttrTypes}), map[string]any{
		"storage": map[string]any{"size": "5Gi", "mountPath": "/var/lib/app"},
	}))
	if len(got) != 1 || got[0].Name.ValueString() != "data" || got[0].SizeGi.ValueInt64() != 5 || got[0].MountPath.ValueString() != "/var/lib/app" {
		t.Errorf("storage as data: %+v", got)
	}
	// a size the provider can't read in GiB keeps what state had
	prev := volumeList(t, []appVolumeModel{vol("odd", 3, "/odd")})
	got = get(readVolumes(prev, map[string]any{"volumes": []any{map[string]any{"name": "odd", "size": "3072Mi", "mountPath": "/odd"}}}))
	if len(got) != 1 || got[0].SizeGi.ValueInt64() != 3 {
		t.Errorf("odd size: %+v", got)
	}
	if l := readVolumes(prev, map[string]any{}); l.IsNull() || len(l.Elements()) != 0 {
		t.Errorf("no volumes reads as an empty list (no blocks), got %v", l)
	}
}
