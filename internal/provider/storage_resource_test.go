package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
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
			"The secret key can't start or end with a space"},
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
		resp := &planmodifier.BoolResponse{PlanValue: plan}
		keepSizeWhenUnset{}.PlanModifyBool(ctx, planmodifier.BoolRequest{
			State: c.state, ConfigValue: c.config, StateValue: c.prior, PlanValue: plan,
		}, resp)
		if !resp.PlanValue.Equal(c.want) {
			t.Errorf("%s: planned %v, want %v", c.name, resp.PlanValue, c.want)
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
// configuration left it out, and as written where it was written.
func TestObjectStorageVersionReadBack(t *testing.T) {
	if got := readDefaulted(types.StringNull(), "1", "1"); !got.IsNull() {
		t.Errorf("left out reads %v", got)
	}
	if got := readDefaulted(types.StringValue("1"), "1", "1"); got.ValueString() != "1" {
		t.Errorf("written reads %v", got)
	}
}
