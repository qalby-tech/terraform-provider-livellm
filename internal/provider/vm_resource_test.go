package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	testKeyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB3NqVqf0V9Z8k5sQ0qkq5JmLqK7v5oQ1xW2Yy3Zz4Ab me@laptop"
	testKeyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK0wmN/Cr3JXqmLW7u+g9pTh+wyqDHpSQEIQczXkVx9q ci"
)

// The stop clock starts when the value changes or the machine is started
// again — never on an apply that touches something else.
func TestStopAfterToSend(t *testing.T) {
	null := types.StringNull()
	four := types.StringValue("4h")
	m := func(stopAfter types.String, stopped bool) vmResourceModel {
		return vmResourceModel{StopAfter: stopAfter, Stopped: types.BoolValue(stopped)}
	}
	cases := []struct {
		name        string
		plan, state vmResourceModel
		want        string
	}{
		{"never set", m(null, false), m(null, false), ""},
		{"unchanged: another setting was edited", m(four, false), m(four, false), ""},
		{"set for the first time", m(four, false), m(null, false), "4h"},
		{"changed", m(types.StringValue("90m"), false), m(four, false), "90m"},
		{"removed", m(null, false), m(four, false), "off"},
		{"started again after the platform stopped it", m(four, false), m(four, true), "4h"},
		{"being stopped", m(four, true), m(four, false), ""},
		{"changed while stopped", m(types.StringValue("90m"), true), m(four, true), ""},
	}
	for _, c := range cases {
		if got := stopAfterToSend(c.plan, c.state); got != c.want {
			t.Errorf("%s: sent %q, want %q", c.name, got, c.want)
		}
	}
}

// Keys travel inside credentials on create and on the machine afterwards; an
// empty or unmanaged list is never sent, because the platform reads that as
// "keep what the machine has".
func TestVMSpecKeysAndStopTime(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{
		Username: types.StringValue("agent"),
		SSHKeys:  keyList([]string{testKeyA}),
	}
	create := vmSpec(ctx, m, vmWrite{password: "pw", stopAfter: "4h", create: true})
	creds, _ := create["credentials"].(map[string]any)
	if !reflect.DeepEqual(creds["sshKeys"], []string{testKeyA}) || create["sshKeys"] != nil {
		t.Errorf("create: credentials=%v machine=%v", creds, create["sshKeys"])
	}
	if create["stopAfter"] != "4h" {
		t.Errorf("create: stopAfter=%v", create["stopAfter"])
	}

	update := vmSpec(ctx, m, vmWrite{})
	if !reflect.DeepEqual(update["sshKeys"], []string{testKeyA}) || update["credentials"] != nil {
		t.Errorf("update: %v", update)
	}
	if _, sent := update["stopAfter"]; sent {
		t.Error("update: a stop time was sent without being asked for")
	}

	rotate := vmSpec(ctx, m, vmWrite{password: "new"})
	if c, _ := rotate["credentials"].(map[string]any); c["sshKeys"] != nil || rotate["sshKeys"] == nil {
		t.Errorf("password change: %v", rotate)
	}

	for name, l := range map[string]types.List{
		"left out": types.ListUnknown(types.StringType),
		"null":     types.ListNull(types.StringType),
		"empty":    keyList(nil),
	} {
		m.SSHKeys = l
		if got := vmSpec(ctx, m, vmWrite{}); got["sshKeys"] != nil {
			t.Errorf("%s: sent %v", name, got["sshKeys"])
		}
	}
}

// State keeps the keys as they were written; it only changes when the machine
// holds different ones.
func TestSameKeys(t *testing.T) {
	spaced := "  ssh-ed25519   AAAAC3NzaC1lZDI1NTE5AAAAIB3NqVqf0V9Z8k5sQ0qkq5JmLqK7v5oQ1xW2Yy3Zz4Ab  me@laptop "
	if !sameKeys([]string{spaced}, []string{testKeyA}) {
		t.Error("the same key, spaced differently, read as a change")
	}
	if sameKeys([]string{testKeyA}, []string{testKeyB}) {
		t.Error("another key read as the same")
	}
	if sameKeys([]string{testKeyA}, []string{testKeyA, testKeyB}) {
		t.Error("an added key went unnoticed")
	}
	if !sameKeys(nil, []string{}) {
		t.Error("no keys and an empty list differ")
	}
	got := remoteKeys(map[string]any{"sshKeys": []any{testKeyA, 7, testKeyB}})
	if !reflect.DeepEqual(got, []string{testKeyA, testKeyB}) {
		t.Errorf("remote keys: %v", got)
	}
}

// Only a system other than Ubuntu travels; Ubuntu is what the platform assumes.
func TestVMSpecSystem(t *testing.T) {
	ctx := context.Background()
	for os, want := range map[string]any{"ubuntu": nil, "debian": "debian", "fedora": "fedora"} {
		m := vmResourceModel{Username: types.StringValue("agent"), OS: types.StringValue(os)}
		if got := vmSpec(ctx, m, vmWrite{})["os"]; got != want {
			t.Errorf("%s: os=%v, want %v", os, got, want)
		}
	}
}

func TestVMBackup(t *testing.T) {
	cases := []struct {
		name string
		b    *vmBackupModel
		want int
	}{
		{"no block", nil, 0},
		{"daily, keep 7", &vmBackupModel{Schedule: types.StringValue("@daily"), Keep: types.Int64Value(7)}, 0},
		{"cron", &vmBackupModel{Schedule: types.StringValue("0 3 * * *"), Keep: types.Int64Value(3)}, 0},
		{"no schedule", &vmBackupModel{Schedule: types.StringNull(), Keep: types.Int64Value(3)}, 1},
		{"bad schedule", &vmBackupModel{Schedule: types.StringValue("nightly"), Keep: types.Int64Value(3)}, 1},
		{"no keep", &vmBackupModel{Schedule: types.StringValue("@daily"), Keep: types.Int64Null()}, 1},
	}
	for _, c := range cases {
		if got := vmConfigErrors(vmResourceModel{Backup: c.b}); len(got) != c.want {
			t.Errorf("%s: %v, want %d errors", c.name, got, c.want)
		}
	}
	if got := readVMBackup(map[string]any{"schedule": "@daily", "keep": float64(7)}); got == nil ||
		got.Schedule.ValueString() != "@daily" || got.Keep.ValueInt64() != 7 {
		t.Errorf("read: %+v", got)
	}
	if readVMBackup(nil) != nil || readVMBackup(map[string]any{"keep": float64(3)}) != nil {
		t.Error("no schedule reads back as no block")
	}
}

// A Windows machine is its own type with its edition; it never sends os.
func TestVMSpecWindows(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{
		OS:             types.StringValue("windows"),
		WindowsEdition: types.StringValue("server"),
		Username:       types.StringValue("admin"),
		DiskGi:         types.Int64Value(80),
		SSHKeys:        types.ListNull(types.StringType),
	}
	if got := m.workloadType(); got != "vm-windows" {
		t.Errorf("type %q, want vm-windows", got)
	}
	spec := vmSpec(ctx, m, vmWrite{password: "pw12345678", create: true})
	if spec["windowsEdition"] != "server" || spec["os"] != nil || spec["storageSize"] != "80Gi" {
		t.Errorf("windows spec: %v", spec)
	}
	m.WindowsEdition = types.StringNull()
	if spec := vmSpec(ctx, m, vmWrite{}); spec["windowsEdition"] != "desktop" {
		t.Errorf("an unset edition should be sent as desktop: %v", spec)
	}
	if d := m.defaultWait(); d.Minutes() < 40 {
		t.Errorf("Windows waits %s by default, too short for the install", d)
	}
	linux := vmResourceModel{OS: types.StringValue("debian"), WindowsEdition: types.StringNull(), SSHKeys: types.ListNull(types.StringType)}
	if spec := vmSpec(ctx, linux, vmWrite{}); spec["os"] != "debian" || spec["windowsEdition"] != nil {
		t.Errorf("debian spec: %v", spec)
	}
}

// The platform's Windows rules, at plan time.
func TestWindowsConfigErrors(t *testing.T) {
	base := func() vmResourceModel {
		return vmResourceModel{
			OS: types.StringValue("windows"), WindowsEdition: types.StringNull(), Desktop: types.BoolNull(),
			DiskGi: types.Int64Null(), Username: types.StringValue("admin"), SSHKeys: types.ListNull(types.StringType),
		}
	}
	if errs := windowsConfigErrors(base()); len(errs) != 0 {
		t.Errorf("a plain Windows machine: %v", errs)
	}
	cases := map[string]func(*vmResourceModel){
		"disk_gi":  func(m *vmResourceModel) { m.DiskGi = types.Int64Value(40) },
		"username": func(m *vmResourceModel) { m.Username = types.StringValue("administrator") },
		"desktop":  func(m *vmResourceModel) { m.Desktop = types.BoolValue(true) },
		"ssh_keys": func(m *vmResourceModel) { m.SSHKeys = keyList([]string{testKeyA}) },
		"windows_edition": func(m *vmResourceModel) {
			m.OS = types.StringValue("ubuntu")
			m.WindowsEdition = types.StringValue("server")
		},
	}
	for attr, change := range cases {
		m := base()
		change(&m)
		errs := windowsConfigErrors(m)
		if len(errs) != 1 || errs[0][0] != attr {
			t.Errorf("%s: %v", attr, errs)
		}
	}
	ok := base()
	ok.DiskGi = types.Int64Value(64)
	if errs := windowsConfigErrors(ok); len(errs) != 0 {
		t.Errorf("64 GiB is enough: %v", errs)
	}
}
