package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

func TestSizeGi(t *testing.T) {
	for in, want := range map[string]int64{"3Gi": 3, " 20Gi ": 20, "1Ti": 1024, "2048Mi": 2, "0Gi": 0} {
		if got, ok := sizeGi(in); !ok || got != want {
			t.Errorf("sizeGi(%q) = %d, %v; want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "512Mi", "1.5Gi", "3G", "3", "Gi", "-1Gi", "3Gix"} {
		if got, ok := sizeGi(in); ok {
			t.Errorf("sizeGi(%q) = %d, true; want false", in, got)
		}
	}
}

func TestReadGi(t *testing.T) {
	cases := []struct {
		prev types.Int64
		raw  any
		want types.Int64
	}{
		{types.Int64Null(), "3Gi", types.Int64Value(3)},
		{types.Int64Value(8), "4Gi", types.Int64Value(4)},
		{types.Int64Unknown(), "1Ti", types.Int64Value(1024)},
		{types.Int64Value(8), "", types.Int64Null()},
		{types.Int64Value(8), nil, types.Int64Null()},
		{types.Int64Value(8), 3.0, types.Int64Null()},
		// Not whole GiB: state keeps what it had, never reads 512 GiB.
		{types.Int64Value(1), "512Mi", types.Int64Value(1)},
		{types.Int64Null(), "512Mi", types.Int64Null()},
		{types.Int64Unknown(), "512Mi", types.Int64Null()},
	}
	for _, c := range cases {
		if got := readGi(c.prev, c.raw); !got.Equal(c.want) {
			t.Errorf("readGi(%v, %#v) = %v; want %v", c.prev, c.raw, got, c.want)
		}
	}
}

func TestReadCountAndSize(t *testing.T) {
	if got := readCount(2.0); !got.Equal(types.Int64Value(2)) {
		t.Errorf("readCount(2) = %v", got)
	}
	for _, raw := range []any{nil, 0.0, "2"} {
		if got := readCount(raw); !got.IsNull() {
			t.Errorf("readCount(%#v) = %v; want null", raw, got)
		}
	}
	if got := readSize("250m"); !got.Equal(types.StringValue("250m")) {
		t.Errorf("readSize = %v", got)
	}
	for _, raw := range []any{nil, "", " ", 1.0} {
		if got := readSize(raw); !got.IsNull() {
			t.Errorf("readSize(%#v) = %v; want null", raw, got)
		}
	}
}

func TestSettleSizes(t *testing.T) {
	// Unknown (left out on a create): what the platform chose, or null.
	if got := settleString(types.StringUnknown(), "1Gi"); !got.Equal(types.StringValue("1Gi")) {
		t.Errorf("settleString unknown = %v", got)
	}
	if got := settleString(types.StringUnknown(), nil); !got.IsNull() {
		t.Errorf("settleString unknown, none = %v", got)
	}
	// Planned: comes back as planned, whatever the platform spells.
	if got := settleString(types.StringValue("1024Mi"), "1Gi"); !got.Equal(types.StringValue("1024Mi")) {
		t.Errorf("settleString planned = %v", got)
	}
	if got := settleString(types.StringNull(), "1Gi"); !got.IsNull() {
		t.Errorf("settleString null = %v", got)
	}
	if got := settleInt64(types.Int64Unknown(), types.Int64Value(3)); !got.Equal(types.Int64Value(3)) {
		t.Errorf("settleInt64 unknown = %v", got)
	}
	if got := settleInt64(types.Int64Value(4), types.Int64Value(3)); !got.Equal(types.Int64Value(4)) {
		t.Errorf("settleInt64 planned = %v", got)
	}
}

// sizeAttrs is every size attribute of the five resources whose sizes the
// platform now writes when they are left out (and storage's, the model).
var sizeAttrs = map[string]struct {
	r     func() resource.Resource
	attrs []string
}{
	"vm":            {NewVMResource, []string{"cpus", "memory_gi", "disk_gi"}},
	"browser":       {NewBrowserResource, []string{"cpu", "memory"}},
	"desktop_app":   {NewDesktopAppResource, []string{"cpu", "memory", "storage_gi"}},
	"browser_api":   {NewBrowserAPIResource, []string{"cpu", "memory"}},
	"container_app": {NewContainerAppResource, []string{"cpu", "memory"}},
	"storage":       {NewStorageResource, []string{"cpu", "memory", "disk_gi"}},
}

func TestSizeAttributesOptionalComputed(t *testing.T) {
	ctx := context.Background()
	for name, c := range sizeAttrs {
		var resp resource.SchemaResponse
		c.r().Schema(ctx, resource.SchemaRequest{}, &resp)
		if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatalf("%s schema: %v", name, d)
		}
		for _, a := range c.attrs {
			var optional, computed, keeps bool
			switch v := resp.Schema.Attributes[a].(type) {
			case schema.StringAttribute:
				optional, computed = v.Optional, v.Computed
				for _, m := range v.PlanModifiers {
					_, ok := m.(keepSizeWhenUnset)
					keeps = keeps || ok
				}
			case schema.Int64Attribute:
				optional, computed = v.Optional, v.Computed
				for i, m := range v.PlanModifiers {
					if _, ok := m.(keepSizeWhenUnset); ok {
						keeps = true
						if i != 0 {
							t.Errorf("%s.%s: keepSizeWhenUnset must run first (before a replace check)", name, a)
						}
					}
				}
			default:
				t.Errorf("%s.%s: unexpected attribute %T", name, a, v)
				continue
			}
			if !optional || !computed || !keeps {
				t.Errorf("%s.%s: optional=%v computed=%v keepSizeWhenUnset=%v; want all true", name, a, optional, computed, keeps)
			}
		}
	}
}

func TestKeepSizeWhenUnsetPlans(t *testing.T) {
	ctx := context.Background()
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"cpu": tftypes.String}}
	state := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{"cpu": tftypes.NewValue(tftypes.String, "1")})}
	noState := tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	run := func(st tfsdk.State, cfg, stateVal types.String) types.String {
		req := planmodifier.StringRequest{State: st, ConfigValue: cfg, StateValue: stateVal, PlanValue: types.StringUnknown()}
		resp := &planmodifier.StringResponse{PlanValue: req.PlanValue}
		keepSizeWhenUnset{}.PlanModifyString(ctx, req, resp)
		return resp.PlanValue
	}
	// Left out on an update: the size the resource has, null included.
	if got := run(state, types.StringNull(), types.StringValue("1")); !got.Equal(types.StringValue("1")) {
		t.Errorf("left out, kept: %v", got)
	}
	if got := run(state, types.StringNull(), types.StringNull()); !got.IsNull() {
		t.Errorf("left out, none stored: %v", got)
	}
	// A create stays unknown: the platform chooses.
	if got := run(noState, types.StringNull(), types.StringNull()); !got.IsUnknown() {
		t.Errorf("create: %v", got)
	}
	// A configured size is the plan's own.
	if got := run(state, types.StringValue("2"), types.StringValue("1")); !got.IsUnknown() {
		t.Errorf("configured: plan touched: %v", got)
	}

	ireq := planmodifier.Int64Request{State: state, ConfigValue: types.Int64Null(), StateValue: types.Int64Value(3), PlanValue: types.Int64Unknown()}
	iresp := &planmodifier.Int64Response{PlanValue: ireq.PlanValue}
	keepSizeWhenUnset{}.PlanModifyInt64(ctx, ireq, iresp)
	if !iresp.PlanValue.Equal(types.Int64Value(3)) {
		t.Errorf("int64 left out: %v", iresp.PlanValue)
	}
}

func TestVMSizesReadAndSettle(t *testing.T) {
	var m vmResourceModel
	m.MemoryGi, m.DiskGi = types.Int64Value(9), types.Int64Value(9)
	readVMSizes(&m, map[string]any{"cpus": 2.0, "memory": "3Gi", "storageSize": "32Gi"})
	if !m.CPUs.Equal(types.Int64Value(2)) || !m.MemoryGi.Equal(types.Int64Value(3)) || !m.DiskGi.Equal(types.Int64Value(32)) {
		t.Errorf("read: %v %v %v", m.CPUs, m.MemoryGi, m.DiskGi)
	}
	readVMSizes(&m, map[string]any{})
	if !m.CPUs.IsNull() || !m.MemoryGi.IsNull() || !m.DiskGi.IsNull() {
		t.Errorf("read none: %v %v %v", m.CPUs, m.MemoryGi, m.DiskGi)
	}

	// A create that left sizes out reads the platform's back; a planned one stays.
	m = vmResourceModel{CPUs: types.Int64Unknown(), MemoryGi: types.Int64Value(4), DiskGi: types.Int64Unknown()}
	settleVMSizes(&m, map[string]any{"cpus": 1.0, "memory": "4Gi", "storageSize": "20Gi"})
	if !m.CPUs.Equal(types.Int64Value(1)) || !m.MemoryGi.Equal(types.Int64Value(4)) || !m.DiskGi.Equal(types.Int64Value(20)) {
		t.Errorf("settle: %v %v %v", m.CPUs, m.MemoryGi, m.DiskGi)
	}
	m = vmResourceModel{CPUs: types.Int64Unknown(), MemoryGi: types.Int64Unknown(), DiskGi: types.Int64Unknown()}
	settleVMSizes(&m, nil)
	if !m.CPUs.IsNull() || !m.MemoryGi.IsNull() || !m.DiskGi.IsNull() {
		t.Errorf("settle unreadable: %v %v %v", m.CPUs, m.MemoryGi, m.DiskGi)
	}
}

func TestVMSpecLeavesUnknownSizesOut(t *testing.T) {
	ctx := context.Background()
	m := vmResourceModel{
		OS: types.StringNull(), CPUs: types.Int64Unknown(), MemoryGi: types.Int64Unknown(), DiskGi: types.Int64Unknown(),
		SSHKeys: types.ListNull(types.StringType),
	}
	spec := vmSpec(ctx, m, vmWrite{})
	for _, k := range []string{"cpus", "memory", "storageSize"} {
		if _, ok := spec[k]; ok {
			t.Errorf("unknown size sent as %s=%v", k, spec[k])
		}
	}
	m.CPUs, m.MemoryGi, m.DiskGi = types.Int64Value(2), types.Int64Value(3), types.Int64Value(32)
	spec = vmSpec(ctx, m, vmWrite{})
	if spec["cpus"] != int64(2) || spec["memory"] != "3Gi" || spec["storageSize"] != "32Gi" {
		t.Errorf("sizes: %v %v %v", spec["cpus"], spec["memory"], spec["storageSize"])
	}
}

func TestDesktopSizesSettle(t *testing.T) {
	m := desktopAppModel{CPU: types.StringUnknown(), Memory: types.StringValue("4Gi"), StorageGi: types.Int64Unknown()}
	settleDesktopSizes(&m, map[string]any{"cpu": "1", "memory": "2Gi"})
	if !m.CPU.Equal(types.StringValue("1")) || !m.Memory.Equal(types.StringValue("4Gi")) || !m.StorageGi.IsNull() {
		t.Errorf("settle: %v %v %v", m.CPU, m.Memory, m.StorageGi)
	}
	m = desktopAppModel{CPU: types.StringUnknown(), Memory: types.StringUnknown(), StorageGi: types.Int64Unknown()}
	settleDesktopSizes(&m, map[string]any{"storageSize": "10Gi"})
	if !m.CPU.IsNull() || !m.Memory.IsNull() || !m.StorageGi.Equal(types.Int64Value(10)) {
		t.Errorf("settle keep_files: %v %v %v", m.CPU, m.Memory, m.StorageGi)
	}
	m = desktopAppModel{CPU: types.StringUnknown(), Memory: types.StringUnknown(), StorageGi: types.Int64Unknown()}
	settleDesktopSizes(&m, nil)
	if !m.CPU.IsNull() || !m.Memory.IsNull() || !m.StorageGi.IsNull() {
		t.Errorf("settle unreadable: %v %v %v", m.CPU, m.Memory, m.StorageGi)
	}
	// A refresh reads the default the platform wrote.
	var r desktopAppModel
	readDesktopSpec(&r, &client.Workload{ID: "d", Type: "desktop", Desktop: map[string]any{"cpu": "1", "memory": "2Gi"}})
	if !r.CPU.Equal(types.StringValue("1")) || !r.Memory.Equal(types.StringValue("2Gi")) {
		t.Errorf("read: %v %v", r.CPU, r.Memory)
	}
}

func TestBrowserSizesRead(t *testing.T) {
	ctx := context.Background()
	b := baseBrowser()
	got := readBrowser(ctx, b, map[string]any{"cpu": "1", "memory": "3Gi", "engine": "camoufox"}, false)
	if !got.CPU.Equal(types.StringValue("1")) || !got.Memory.Equal(types.StringValue("3Gi")) {
		t.Errorf("read: %v %v", got.CPU, got.Memory)
	}
	b.CPU, b.Memory = types.StringValue("2"), types.StringValue("4Gi")
	got = readBrowser(ctx, b, map[string]any{}, false)
	if !got.CPU.IsNull() || !got.Memory.IsNull() {
		t.Errorf("read none: %v %v", got.CPU, got.Memory)
	}
}

func TestBrowserAPISizesRead(t *testing.T) {
	got := readBrowserAPI(baseBrowserAPI(), map[string]any{"cpu": "250m", "memory": "256Mi"})
	if !got.CPU.Equal(types.StringValue("250m")) || !got.Memory.Equal(types.StringValue("256Mi")) {
		t.Errorf("read: %v %v", got.CPU, got.Memory)
	}
	prev := baseBrowserAPI()
	prev.CPU, prev.Memory = types.StringValue("1"), types.StringValue("1Gi")
	got = readBrowserAPI(prev, map[string]any{})
	if !got.CPU.IsNull() || !got.Memory.IsNull() {
		t.Errorf("read none: %v %v", got.CPU, got.Memory)
	}
}
