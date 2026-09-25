package provider

import (
	"context"
	"reflect"
	"testing"

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
		BackupSchedule: types.StringNull(), BackupKeep: types.Int64Null(),
	}
}

// Every form that asks for backups sends enabled: a schedule alone once
// meant backups were never taken.
func TestStorageBackupBody(t *testing.T) {
	ctx := context.Background()
	legacy := baseStorage("postgres")
	legacy.BackupSchedule = types.StringValue("0 3 * * *")
	legacy.BackupKeep = types.Int64Value(7)
	legacyNoKeep := baseStorage("postgres")
	legacyNoKeep.BackupSchedule = types.StringValue("0 3 * * *")

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
		{"deprecated schedule turns backups on and leaves the mode alone", legacy, false,
			map[string]any{"enabled": true, "schedule": "0 3 * * *", "keepDays": int64(7)}},
		{"deprecated schedule without backup_keep puts the default keep", legacyNoKeep, true,
			map[string]any{"enabled": true, "schedule": "0 3 * * *", "keepDays": int64(10)}},
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

	// The deprecated form stays in its own attributes; the platform no longer
	// echoes the schedule, so the configured one is kept.
	s = baseStorage("postgres")
	s.BackupSchedule = types.StringValue("@daily")
	readStorageBackup(&s, map[string]any{"enabled": true, "mode": "daily", "keepDays": float64(7)})
	if s.Backup != nil || s.BackupSchedule.ValueString() != "@daily" || s.BackupKeep.ValueInt64() != 7 {
		t.Errorf("deprecated form: backup=%+v schedule=%v keep=%v", s.Backup, s.BackupSchedule, s.BackupKeep)
	}
	readStorageBackup(&s, nil)
	if !s.BackupSchedule.IsNull() || !s.BackupKeep.IsNull() {
		t.Error("deprecated form: backups off should clear the schedule")
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
		{"both backup forms", func(m *storageModel) {
			m.Backup = block
			m.BackupSchedule = types.StringValue("@daily")
		}, 1},
		{"backup_keep alone", func(m *storageModel) { m.BackupKeep = types.Int64Value(5) }, 1},
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
