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
