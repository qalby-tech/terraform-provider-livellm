package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// dedicated_cores on an app, a Desktop App, a database, a browser and a
// Browser API: optional and computed, planned with keepCoreChoice (never a
// Default, which would send false on every save that leaves it out and turn
// off what the console set).
func TestDedicatedCoresAttributes(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]func() resource.Resource{
		"container_app": NewContainerAppResource,
		"desktop_app":   NewDesktopAppResource,
		"storage":       NewStorageResource,
		"browser":       NewBrowserResource,
		"browser_api":   NewBrowserAPIResource,
	} {
		var resp resource.SchemaResponse
		r().Schema(ctx, resource.SchemaRequest{}, &resp)
		v, ok := resp.Schema.Attributes["dedicated_cores"].(schema.BoolAttribute)
		if !ok || !v.Optional || !v.Computed || v.Default != nil || len(v.PlanModifiers) != 1 {
			t.Errorf("%s.dedicated_cores: %+v", name, resp.Schema.Attributes["dedicated_cores"])
			continue
		}
		if _, ok := v.PlanModifiers[0].(keepCoreChoice); !ok {
			t.Errorf("%s.dedicated_cores: plan modifier %T", name, v.PlanModifiers[0])
		}
	}
}

// A known value is sent, false included (that clears it); an unknown one is
// left out. Absent on the platform reads as false.
func TestDedicatedCoresSpecAndRead(t *testing.T) {
	for _, c := range []struct {
		v    types.Bool
		want any
	}{{types.BoolValue(true), true}, {types.BoolValue(false), false}, {types.BoolUnknown(), nil}, {types.BoolNull(), nil}} {
		spec := map[string]any{}
		dedicatedSpec(c.v, spec)
		if spec["dedicatedCores"] != c.want {
			t.Errorf("dedicatedSpec(%v) = %v, want %v", c.v, spec["dedicatedCores"], c.want)
		}
	}
	if !readDedicated(map[string]any{"dedicatedCores": true}).ValueBool() || readDedicated(map[string]any{}).ValueBool() || readDedicated(nil).ValueBool() {
		t.Error("readDedicated")
	}
	if got := settleDedicated(types.BoolUnknown(), map[string]any{"dedicatedCores": true}); !got.ValueBool() {
		t.Errorf("settle unknown from the platform: %v", got)
	}
	if got := settleDedicated(types.BoolValue(false), map[string]any{"dedicatedCores": true}); got.ValueBool() {
		t.Errorf("settle keeps a planned value: %v", got)
	}
	if got := settleDedicated(types.BoolUnknown(), nil); got.IsUnknown() || got.ValueBool() {
		t.Errorf("settle unread: %v", got)
	}
	m := desktopAppModel{CPU: types.StringValue("1"), DedicatedCores: types.BoolValue(true)}
	if got := desktopSpec(m); got["dedicatedCores"] != true {
		t.Errorf("desktop spec: %v", got)
	}
}

// An object storage has no dedicated cores: refused at plan time, and never
// sent.
func TestStorageDedicatedCores(t *testing.T) {
	s3 := baseStorage("s3")
	s3.DedicatedCores = types.BoolValue(true)
	if got := storageConfigErrors(s3); len(got) != 1 || got[0][0] != "Object storage has no dedicated cores" {
		t.Errorf("s3 with dedicated cores: %v", got)
	}
	s3.DedicatedCores = types.BoolValue(false)
	if got := storageConfigErrors(s3); len(got) != 0 {
		t.Errorf("s3 shared: %v", got)
	}
	pg := baseStorage("postgres")
	pg.DedicatedCores = types.BoolValue(true)
	if got := storageConfigErrors(pg); len(got) != 0 {
		t.Errorf("postgres with dedicated cores: %v", got)
	}
}
