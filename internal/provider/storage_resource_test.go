package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func baseStorage(engine string) storageModel {
	return storageModel{
		Name: types.StringValue("db"), Engine: types.StringValue(engine),
		Version: types.StringNull(), DiskGi: types.Int64Null(), Instances: types.Int64Null(),
		CPU: types.StringNull(), Memory: types.StringNull(), Username: types.StringNull(),
		Expose: types.BoolNull(), Allowlist: types.ListNull(types.StringType),
	}
}

// Backups are sent as enabled, mode and keepDays, never as a schedule or a
// number of backups.
func TestStorageBackupBody(t *testing.T) {
	ctx := context.Background()
	modeOnly := baseStorage("postgres")
	modeOnly.Backup = &storageBackupModel{Mode: types.StringValue("continuous"), KeepDays: types.Int64Null()}

	block := baseStorage("postgres")
	block.Backup = &storageBackupModel{Mode: types.StringValue("continuous"), KeepDays: types.Int64Value(14)}

	bare := baseStorage("postgres")
	bare.Backup = &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}

	none := baseStorage("postgres")

	cases := []struct {
		name   string
		m      storageModel
		update bool
		want   any
	}{
		{"block with mode and days", block, false,
			map[string]any{"enabled": true, "mode": "continuous", "keepDays": int64(14)}},
		{"empty block sends the defaults, so a change made elsewhere is put back", bare, true,
			map[string]any{"enabled": true, "mode": "daily", "keepDays": int64(10)}},
		{"block with only a mode sends the default keep", modeOnly, true,
			map[string]any{"enabled": true, "mode": "continuous", "keepDays": int64(10)}},
		{"no backups on create sends nothing", none, false, nil},
		{"no backups on update says off", none, true, map[string]any{"enabled": false}},
		{"redis says nothing about backups", baseStorage("redis"), true, nil},
	}
	for _, c := range cases {
		got := storageSpec(ctx, c.m, "", c.update)["backup"]
		if b, ok := got.(map[string]any); ok {
			for _, k := range []string{"schedule", "maxBackups"} {
				if _, sent := b[k]; sent {
					t.Errorf("%s: sent %s", c.name, k)
				}
			}
		}
		if c.want == nil {
			if got != nil {
				t.Errorf("%s: sent %v, want nothing", c.name, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: sent %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReadStorageBackup(t *testing.T) {
	on := map[string]any{"enabled": true, "mode": "daily", "keepDays": float64(10)}

	// The block with nothing in it reads back as written.
	s := baseStorage("postgres")
	s.Backup = &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}
	readStorageBackup(&s, on)
	if s.Backup == nil || !s.Backup.Mode.IsNull() || !s.Backup.KeepDays.IsNull() {
		t.Errorf("defaults should read back as unset: %+v", s.Backup)
	}

	// A change made elsewhere shows.
	readStorageBackup(&s, map[string]any{"enabled": true, "mode": "continuous", "keepDays": float64(3)})
	if s.Backup.Mode.ValueString() != "continuous" || s.Backup.KeepDays.ValueInt64() != 3 {
		t.Errorf("a change made elsewhere should show: %+v", s.Backup)
	}

	// Backups turned off elsewhere remove the block.
	readStorageBackup(&s, map[string]any{"enabled": false})
	if s.Backup != nil {
		t.Error("backups off should read back as no block")
	}

	// Backups turned on elsewhere show as a block the configuration lacks.
	s = baseStorage("postgres")
	readStorageBackup(&s, on)
	if s.Backup == nil {
		t.Error("backups on elsewhere should show")
	}
}

func TestStorageConfigErrors(t *testing.T) {
	block := &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}
	cases := []struct {
		name string
		edit func(*storageModel)
		want int
	}{
		{"plain postgres", func(m *storageModel) {}, 0},
		{"postgres with three instances and backups", func(m *storageModel) {
			m.Instances = types.Int64Value(3)
			m.Backup = block
		}, 0},
	}
	for _, c := range cases {
		m := baseStorage("postgres")
		c.edit(&m)
		if got := storageConfigErrors(m); len(got) != c.want {
			t.Errorf("%s: %d errors %v, want %d", c.name, len(got), got, c.want)
		}
	}
	redis := baseStorage("redis")
	redis.Instances = types.Int64Value(3)
	redis.Backup = block
	if got := storageConfigErrors(redis); len(got) != 2 {
		t.Errorf("redis with three instances and backups: %v, want 2 errors", got)
	}
	redis = baseStorage("redis")
	redis.Instances = types.Int64Value(1)
	if got := storageConfigErrors(redis); len(got) != 0 {
		t.Errorf("redis with one instance: %v", got)
	}
}

// Sizes the platform chose are read back, so the next write keeps them.
func TestReadStorageSizes(t *testing.T) {
	m := baseStorage("postgres")
	m.DiskGi, m.CPU, m.Memory = types.Int64Unknown(), types.StringUnknown(), types.StringUnknown()
	readStorageSizes(&m, map[string]any{"storageSize": "5Gi", "cpu": "1", "memory": "1Gi"})
	if m.DiskGi.ValueInt64() != 5 || m.CPU.ValueString() != "1" || m.Memory.ValueString() != "1Gi" {
		t.Errorf("sizes: %v %v %v", m.DiskGi, m.CPU, m.Memory)
	}
	body := storageSpec(context.Background(), m, "", true)
	if body["storageSize"] != "5Gi" || body["cpu"] != "1" || body["memory"] != "1Gi" {
		t.Errorf("the next write should send the sizes back: %v", body)
	}
}

// A size the configuration leaves out plans as what the database has, even
// nothing: a database saved without sizes never shows "(known after apply)"
// and an update on every plan, and never gets CPU or memory it didn't have.
func TestKeepSizeWhenUnset(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}
	saved := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{})}
	creating := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}

	cases := []struct {
		name          string
		state         tfsdk.State
		config, prior types.String
		want          types.String
	}{
		{"saved without a size stays without", saved, types.StringNull(), types.StringNull(), types.StringNull()},
		{"saved with a size keeps it", saved, types.StringNull(), types.StringValue("1"), types.StringValue("1")},
		{"a configured size is planned", saved, types.StringValue("2"), types.StringValue("1"), types.StringValue("2")},
		{"a new database leaves it to the platform", creating, types.StringNull(), types.StringNull(), types.StringUnknown()},
	}
	for _, c := range cases {
		plan := c.config
		if plan.IsNull() {
			plan = types.StringUnknown()
		}
		resp := &planmodifier.StringResponse{PlanValue: plan}
		keepSizeWhenUnset{}.PlanModifyString(ctx, planmodifier.StringRequest{
			State: c.state, ConfigValue: c.config, StateValue: c.prior, PlanValue: plan,
		}, resp)
		if !resp.PlanValue.Equal(c.want) {
			t.Errorf("%s: planned %v, want %v", c.name, resp.PlanValue, c.want)
		}
	}

	resp := &planmodifier.Int64Response{PlanValue: types.Int64Unknown()}
	keepSizeWhenUnset{}.PlanModifyInt64(ctx, planmodifier.Int64Request{
		State: saved, ConfigValue: types.Int64Null(), StateValue: types.Int64Null(), PlanValue: types.Int64Unknown(),
	}, resp)
	if !resp.PlanValue.IsNull() {
		t.Errorf("disk saved without a size: planned %v, want null", resp.PlanValue)
	}
}

// Left out of the configuration, the username plans as the name the
// database has, so a plan never replaces it for a name no one changed: the
// platform's own "app" an older provider wrote into state, nothing for a
// database saved without one, the name an import read. A name the
// configuration changes still replaces it, and a new database leaves it to
// the platform.
func TestUsernamePlan(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	mods := sr.Schema.Attributes["username"].(schema.StringAttribute).PlanModifiers
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}
	saved := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{})}
	creating := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	planned := tfsdk.Plan{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{})}
	null, app := types.StringNull(), types.StringValue("app")

	cases := []struct {
		name          string
		state         tfsdk.State
		config, prior types.String
		want          types.String
		replace       bool
	}{
		{"left out, state from an older provider", saved, null, app, app, false},
		{"left out, saved without one", saved, null, null, null, false},
		{"left out, another name read back", saved, null, types.StringValue("nextcloud"), types.StringValue("nextcloud"), false},
		{"imported, configured as the platform's", saved, app, app, app, false},
		{"configured anew", saved, types.StringValue("shop"), app, types.StringValue("shop"), true},
		{"a new database", creating, null, null, types.StringUnknown(), false},
	}
	for _, c := range cases {
		plan := c.config
		if plan.IsNull() {
			plan = types.StringUnknown() // what the framework plans for a computed value left out
		}
		replace := false
		for _, m := range mods {
			req := planmodifier.StringRequest{State: c.state, Plan: planned, ConfigValue: c.config, StateValue: c.prior, PlanValue: plan}
			resp := &planmodifier.StringResponse{PlanValue: plan}
			m.PlanModifyString(ctx, req, resp)
			plan, replace = resp.PlanValue, replace || resp.RequiresReplace
		}
		if !plan.Equal(c.want) || replace != c.replace {
			t.Errorf("%s: planned %v (replace %v), want %v (replace %v)", c.name, plan, replace, c.want, c.replace)
		}
	}
}

// Read always reads the name back; after a create that left it out, state
// holds the platform's name.
func TestStorageUsernameReadBack(t *testing.T) {
	if got := storageUsername(map[string]any{"credentials": map[string]any{"username": "app"}}); got != "app" {
		t.Errorf("read back %q", got)
	}
	if got := storageUsername(map[string]any{"engine": "redis"}); got != "" {
		t.Errorf("no login read back as %q", got)
	}
}

// Object storage (engine s3) has no backups: nothing about them is sent, on
// create or on update, so an off block never reaches the platform.
func TestStorageBackupBodyObjectStorage(t *testing.T) {
	ctx := context.Background()
	for _, update := range []bool{false, true} {
		if got, ok := storageSpec(ctx, baseStorage("s3"), "", update)["backup"]; ok {
			t.Errorf("object storage (update %v) sent backup %v", update, got)
		}
	}
	// Even a block the plan refuses is never sent.
	m := baseStorage("s3")
	m.Backup = &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}
	if got, ok := storageSpec(ctx, m, "", true)["backup"]; ok {
		t.Errorf("object storage sent backup %v", got)
	}
}

func TestStorageConfigErrorsObjectStorage(t *testing.T) {
	plain := baseStorage("s3")
	plain.Version = types.StringValue("1")
	plain.Instances = types.Int64Value(1)
	plain.PasswordWO = types.StringValue("a-long-secret-key")
	if got := storageConfigErrors(plain); len(got) != 0 {
		t.Errorf("object storage version 1, one instance: %v", got)
	}
	if got := storageConfigErrors(baseStorage("s3")); len(got) != 0 {
		t.Errorf("object storage with everything left out: %v", got)
	}
	cases := []struct {
		name string
		edit func(*storageModel)
		want string
	}{
		{"a backup block", func(m *storageModel) {
			m.Backup = &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}
		}, "Object storage has no backups yet"},
		{"three instances", func(m *storageModel) { m.Instances = types.Int64Value(3) }, "Object storage runs as one server"},
		{"another version", func(m *storageModel) { m.Version = types.StringValue("2") }, "Object storage runs version 1"},
		{"a secret key ending with a space", func(m *storageModel) { m.PasswordWO = types.StringValue("secret-key ") },
			"The secret key can't start or end with a space, tab or line break"},
		{"a secret key ending with a line break", func(m *storageModel) { m.PasswordWO = types.StringValue("secret-key\n") },
			"The secret key can't start or end with a space, tab or line break"},
		{"a secret key starting with a tab", func(m *storageModel) { m.PasswordWO = types.StringValue("\tsecret-key") },
			"The secret key can't start or end with a space, tab or line break"},
	}
	for _, c := range cases {
		m := baseStorage("s3")
		c.edit(&m)
		got := storageConfigErrors(m)
		if len(got) != 1 || got[0][0] != c.want {
			t.Errorf("%s: %v, want %q", c.name, got, c.want)
		}
	}
	// Values not known yet are left for the platform.
	m := baseStorage("s3")
	m.Version, m.Instances, m.PasswordWO = types.StringUnknown(), types.Int64Unknown(), types.StringUnknown()
	if got := storageConfigErrors(m); len(got) != 0 {
		t.Errorf("unknown values: %v", got)
	}
	// The space rule is object storage's own.
	pg := baseStorage("postgres")
	pg.PasswordWO = types.StringValue(" pw ")
	if got := storageConfigErrors(pg); len(got) != 0 {
		t.Errorf("postgres password with spaces: %v", got)
	}
}

// admin_console is sent whenever it is known, off included (a write replaces
// the database's settings), and never while it isn't.
func TestStorageAdminConsoleBody(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		v    types.Bool
		want any
	}{
		{"on", types.BoolValue(true), true},
		{"off", types.BoolValue(false), false},
		{"not known yet", types.BoolUnknown(), nil},
		{"null", types.BoolNull(), nil},
	} {
		m := baseStorage("s3")
		m.AdminConsole = c.v
		got, ok := storageSpec(ctx, m, "", true)["adminConsole"]
		if c.want == nil {
			if ok {
				t.Errorf("%s: sent %v", c.name, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: sent %v, want %v", c.name, got, c.want)
		}
	}
	if got := storageAdminConsole(map[string]any{"engine": "s3"}); !got.Equal(types.BoolValue(false)) {
		t.Errorf("a database that says nothing about its console reads %v, want false", got)
	}
	if got := storageAdminConsole(map[string]any{"adminConsole": true}); !got.Equal(types.BoolValue(true)) {
		t.Errorf("console on reads %v", got)
	}
}

// Left out, admin_console plans as what the database has (a console switched
// on in the dashboard stays on); a new database leaves it to the platform.
func TestKeepAdminConsoleWhenUnset(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{}}
	saved := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{})}
	creating := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	mods := sr.Schema.Attributes["admin_console"].(schema.BoolAttribute).PlanModifiers
	if len(mods) == 0 {
		t.Fatal("admin_console has no plan modifiers")
	}
	on, off := types.BoolValue(true), types.BoolValue(false)
	for _, c := range []struct {
		name          string
		state         tfsdk.State
		config, prior types.Bool
		want          types.Bool
	}{
		{"left out, switched on elsewhere", saved, types.BoolNull(), on, on},
		{"left out, off", saved, types.BoolNull(), off, off},
		{"configured off", saved, off, on, off},
		{"configured on", saved, on, off, on},
		{"a new database", creating, types.BoolNull(), types.BoolNull(), types.BoolUnknown()},
	} {
		plan := c.config
		if plan.IsNull() {
			plan = types.BoolUnknown()
		}
		for _, m := range mods {
			resp := &planmodifier.BoolResponse{PlanValue: plan}
			m.PlanModifyBool(ctx, planmodifier.BoolRequest{
				State: c.state, ConfigValue: c.config, StateValue: c.prior, PlanValue: plan,
			}, resp)
			plan = resp.PlanValue
		}
		if !plan.Equal(c.want) {
			t.Errorf("%s: planned %v, want %v", c.name, plan, c.want)
		}
	}
}

// The schema offers object storage as an engine, next to Postgres and Redis.
func TestStorageEngineSchema(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	vals := sr.Schema.Attributes["engine"].(schema.StringAttribute).Validators
	if len(vals) == 0 {
		t.Fatal("engine has no validators")
	}
	for engine, ok := range map[string]bool{"postgres": true, "redis": true, "s3": true, "mysql": false} {
		var errs diag.Diagnostics
		for _, v := range vals {
			resp := &validator.StringResponse{}
			v.ValidateString(ctx, validator.StringRequest{Path: path.Root("engine"), ConfigValue: types.StringValue(engine)}, resp)
			errs.Append(resp.Diagnostics...)
		}
		if errs.HasError() == ok {
			t.Errorf("engine %q: refused %v, want refused %v", engine, errs.HasError(), !ok)
		}
	}
}

// An update sends the configured password when its version changes, and when
// the console is turned on (the platform needs it in the same write).
func TestStoragePassword(t *testing.T) {
	m := func(version int64, console types.Bool) storageModel {
		s := baseStorage("s3")
		s.PasswordWOVersion = types.Int64Value(version)
		s.AdminConsole = console
		return s
	}
	on, off := types.BoolValue(true), types.BoolValue(false)
	for _, c := range []struct {
		name        string
		plan, state storageModel
		want        string
	}{
		{"nothing changed", m(1, off), m(1, off), ""},
		{"a new version", m(2, off), m(1, off), "pw"},
		{"console turned on", m(1, on), m(1, off), "pw"},
		{"console turned on from a state without it", m(1, on), m(1, types.BoolNull()), "pw"},
		{"console already on", m(1, on), m(1, on), ""},
		{"console turned off", m(1, off), m(1, on), ""},
	} {
		if got := storagePassword(c.plan, c.state, "pw"); got != c.want {
			t.Errorf("%s: sent %q, want %q", c.name, got, c.want)
		}
	}
}

// Object storage's one version reads back as left out where the
// configuration left it out and as written where it was written; the first
// read after an import takes it as the platform has it, as for Postgres.
func TestObjectStorageVersionReadBack(t *testing.T) {
	sp := map[string]any{"engine": "s3", "version": "1"}
	for _, c := range []struct {
		name     string
		engine   string
		prev     types.String
		imported bool
		version  string
		want     types.String
	}{
		{"s3 left out", "s3", types.StringNull(), false, "1", types.StringNull()},
		{"s3 written", "s3", types.StringValue("1"), false, "1", types.StringValue("1")},
		{"s3 imported", "s3", types.StringNull(), true, "1", types.StringValue("1")},
		{"s3 imported, none stored", "s3", types.StringNull(), true, "", types.StringNull()},
		{"s3 left out, none stored", "s3", types.StringNull(), false, "", types.StringNull()},
		{"postgres left out", "postgres", types.StringNull(), false, "16", types.StringValue("16")},
		{"postgres imported", "postgres", types.StringNull(), true, "16", types.StringValue("16")},
	} {
		st := baseStorage(c.engine)
		st.Version = c.prev
		sp["engine"], sp["version"] = c.engine, c.version
		readStorage(&st, sp, c.imported)
		if !st.Version.Equal(c.want) {
			t.Errorf("%s: version %v, want %v", c.name, st.Version, c.want)
		}
	}
}

// A refresh and an import read the console back from the platform, off when
// it says nothing.
func TestReadStorageAdminConsole(t *testing.T) {
	for _, c := range []struct {
		name string
		sp   map[string]any
		want bool
	}{
		{"on", map[string]any{"engine": "s3", "adminConsole": true}, true},
		{"off", map[string]any{"engine": "postgres", "adminConsole": false}, false},
		{"not said", map[string]any{"engine": "redis"}, false},
	} {
		st := baseStorage("postgres")
		st.AdminConsole = types.BoolValue(!c.want)
		readStorage(&st, c.sp, false)
		if !st.AdminConsole.Equal(types.BoolValue(c.want)) {
			t.Errorf("%s: admin_console %v, want %v", c.name, st.AdminConsole, c.want)
		}
	}
}

// After a write, an admin_console left unknown is what the platform made
// (off when it can't be read); a planned one stays as planned.
func TestFillStorageAdminConsole(t *testing.T) {
	for _, c := range []struct {
		name    string
		planned types.Bool
		sp      map[string]any
		want    types.Bool
	}{
		{"unknown, platform on", types.BoolUnknown(), map[string]any{"adminConsole": true}, types.BoolValue(true)},
		{"unknown, platform says nothing", types.BoolUnknown(), map[string]any{}, types.BoolValue(false)},
		{"unknown, not read", types.BoolUnknown(), nil, types.BoolValue(false)},
		{"planned off, platform on", types.BoolValue(false), map[string]any{"adminConsole": true}, types.BoolValue(false)},
		{"planned on", types.BoolValue(true), map[string]any{}, types.BoolValue(true)},
	} {
		m := baseStorage("s3")
		m.AdminConsole = c.planned
		fillStorageFromSpec(&m, c.sp)
		if !m.AdminConsole.Equal(c.want) {
			t.Errorf("%s: admin_console %v, want %v", c.name, m.AdminConsole, c.want)
		}
	}
}

// Removing an object storage's allowlist while its console stays on warns;
// nothing else does.
func TestConsoleAllowlistWarning(t *testing.T) {
	list := types.ListValueMust(types.StringType, []attr.Value{types.StringValue("203.0.113.0/24")})
	empty := types.ListValueMust(types.StringType, []attr.Value{})
	null := types.ListNull(types.StringType)
	for _, c := range []struct {
		name      string
		engine    string
		console   types.Bool
		planned   types.List
		state     types.List
		wantWarns bool
	}{
		{"s3 console on, list removed", "s3", types.BoolValue(true), null, list, true},
		{"s3 console on, list kept", "s3", types.BoolValue(true), list, list, false},
		{"s3 console on, opened on purpose", "s3", types.BoolValue(true), empty, list, false},
		{"s3 console on, no list before", "s3", types.BoolValue(true), null, null, false},
		{"s3 console on, empty list before", "s3", types.BoolValue(true), null, empty, false},
		{"s3 console off, list removed", "s3", types.BoolValue(false), null, list, false},
		{"s3 console unknown, list removed", "s3", types.BoolUnknown(), null, list, false},
		{"postgres console on, list removed", "postgres", types.BoolValue(true), null, list, false},
	} {
		plan, state := baseStorage(c.engine), baseStorage(c.engine)
		plan.AdminConsole, plan.Allowlist = c.console, c.planned
		state.AdminConsole, state.Allowlist = types.BoolValue(true), c.state
		if got := consoleAllowlistWarning(plan, state); (got != nil) != c.wantWarns {
			t.Errorf("%s: warning %v, want one: %v", c.name, got, c.wantWarns)
		}
	}
}

// An object storage's console that stays on although the configuration leaves
// it out, with no allowlist and no expose, warns; one asked for, limited or
// exposed on purpose does not.
func TestConsoleOpenWarning(t *testing.T) {
	list := types.ListValueMust(types.StringType, []attr.Value{types.StringValue("203.0.113.0/24")})
	empty := types.ListValueMust(types.StringType, []attr.Value{})
	null := types.ListNull(types.StringType)
	on, off := types.BoolValue(true), types.BoolValue(false)
	for _, c := range []struct {
		name      string
		engine    string
		config    types.Bool
		console   types.Bool
		expose    types.Bool
		list      types.List
		wantWarns bool
	}{
		{"kept on, no list", "s3", types.BoolNull(), on, types.BoolNull(), null, true},
		{"kept on, empty list", "s3", types.BoolNull(), on, off, empty, true},
		{"kept on, list", "s3", types.BoolNull(), on, types.BoolNull(), list, false},
		{"kept on, exposed", "s3", types.BoolNull(), on, on, null, false},
		{"configured on", "s3", on, on, types.BoolNull(), null, false},
		{"kept off", "s3", types.BoolNull(), off, types.BoolNull(), null, false},
		{"unknown", "s3", types.BoolNull(), types.BoolUnknown(), types.BoolNull(), null, false},
		{"postgres kept on", "postgres", types.BoolNull(), on, types.BoolNull(), null, false},
	} {
		plan := baseStorage(c.engine)
		plan.AdminConsole, plan.Expose, plan.Allowlist = c.console, c.expose, c.list
		if got := consoleOpenWarning(plan, c.config); (got != nil) != c.wantWarns {
			t.Errorf("%s: warning %v, want one: %v", c.name, got, c.wantWarns)
		}
	}
}

// Turning the console on without a new password_wo_version warns, for every
// engine: the apply sends password_wo, which changes the password when it is
// not the current one.
func TestConsolePasswordWarning(t *testing.T) {
	m := func(engine string, version int64, console types.Bool) storageModel {
		s := baseStorage(engine)
		s.PasswordWOVersion = types.Int64Value(version)
		s.AdminConsole = console
		return s
	}
	on, off := types.BoolValue(true), types.BoolValue(false)
	for _, c := range []struct {
		name        string
		plan, state storageModel
		wantWarns   bool
	}{
		{"s3 turned on", m("s3", 1, on), m("s3", 1, off), true},
		{"postgres turned on", m("postgres", 1, on), m("postgres", 1, off), true},
		{"turned on from a state without it", m("redis", 1, on), m("redis", 1, types.BoolNull()), true},
		{"turned on with a new version", m("s3", 2, on), m("s3", 1, off), false},
		{"already on", m("s3", 1, on), m("s3", 1, on), false},
		{"turned off", m("s3", 1, off), m("s3", 1, on), false},
		{"unknown", m("s3", 1, types.BoolUnknown()), m("s3", 1, off), false},
	} {
		if got := consolePasswordWarning(c.plan, c.state); (got != nil) != c.wantWarns {
			t.Errorf("%s: warning %v, want one: %v", c.name, got, c.wantWarns)
		}
	}
}

// ModifyPlan reads admin_console from the configuration and attaches each
// warning to its attribute on a real update plan.
func TestStorageModifyPlanWarnings(t *testing.T) {
	ctx := context.Background()
	var sr resource.SchemaResponse
	(&storageResource{}).Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	value := func(set map[string]tftypes.Value) tftypes.Value {
		vals := map[string]tftypes.Value{}
		for name, at := range typ.AttributeTypes {
			vals[name] = tftypes.NewValue(at, nil)
		}
		vals["name"] = tftypes.NewValue(tftypes.String, "files")
		vals["engine"] = tftypes.NewValue(tftypes.String, "s3")
		vals["password_wo_version"] = tftypes.NewValue(tftypes.Number, 1)
		for k, v := range set {
			vals[k] = v
		}
		return tftypes.NewValue(typ, vals)
	}
	on := tftypes.NewValue(tftypes.Bool, true)
	off := tftypes.NewValue(tftypes.Bool, false)
	for _, c := range []struct {
		name          string
		config, plan  map[string]tftypes.Value
		state         map[string]tftypes.Value
		wantWarningOn []string
	}{
		{"console kept on, left out", nil, map[string]tftypes.Value{"admin_console": on},
			map[string]tftypes.Value{"admin_console": on}, []string{"admin_console"}},
		{"console turned on", map[string]tftypes.Value{"admin_console": on}, map[string]tftypes.Value{"admin_console": on},
			map[string]tftypes.Value{"admin_console": off}, []string{"password_wo"}},
		{"nothing to say", map[string]tftypes.Value{"admin_console": off}, map[string]tftypes.Value{"admin_console": off},
			map[string]tftypes.Value{"admin_console": off}, nil},
	} {
		req := resource.ModifyPlanRequest{
			Config: tfsdk.Config{Schema: sr.Schema, Raw: value(c.config)},
			Plan:   tfsdk.Plan{Schema: sr.Schema, Raw: value(c.plan)},
			State:  tfsdk.State{Schema: sr.Schema, Raw: value(c.state)},
		}
		resp := &resource.ModifyPlanResponse{Plan: req.Plan}
		(&storageResource{}).ModifyPlan(ctx, req, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%s: %v", c.name, resp.Diagnostics)
		}
		var got []string
		for _, d := range resp.Diagnostics.Warnings() {
			if wp, ok := d.(diag.DiagnosticWithPath); ok {
				got = append(got, wp.Path().String())
			}
		}
		if !reflect.DeepEqual(got, c.wantWarningOn) {
			t.Errorf("%s: warnings on %v, want %v", c.name, got, c.wantWarningOn)
		}
	}
}
