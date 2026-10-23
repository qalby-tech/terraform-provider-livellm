package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A machine's devices and its time zone. Devices are the cluster's (a GPU, a
// USB controller), named by id: the platform keeps a machine's devices when a
// save leaves them out and removes them with an empty list, so they are sent
// only when the configuration writes them. The time zone is the machine's
// when it is made, then the machine's own to change: the platform doesn't
// keep it over a save that leaves it out, so every save sends the one state
// holds, and a change on an existing machine is refused at plan time (a
// replacement would delete the disk for a setting that does nothing there).
// What the platform resolves a device into (what is passed through, the
// hypervisor hidden for a GPU) is its own and never read or sent.

// keepWhenLeftOut plans an attribute the configuration leaves out as what the
// resource already has, and on a create as nothing (an empty list, or null):
// the platform chooses nothing for it, so the plan knows it at once.
type keepWhenLeftOut struct{}

func (keepWhenLeftOut) Description(context.Context) string {
	return "Keeps the resource's current value when the configuration leaves it out; nothing on a new resource."
}

func (m keepWhenLeftOut) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (keepWhenLeftOut) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.StringNull()
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepWhenLeftOut) PlanModifyList(_ context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.ListValueMust(req.PlanValue.ElementType(context.Background()), []attr.Value{})
		return
	}
	resp.PlanValue = req.StateValue
}

// maxMachineDevices is the most devices a machine has.
const maxMachineDevices = 8

// readVMDevices is the machine's devices as the platform holds them: an
// empty list for none.
func readVMDevices(sp map[string]any) types.List {
	raw, _ := sp["devices"].([]any)
	ids := make([]string, 0, len(raw))
	for _, d := range raw {
		if s, ok := d.(string); ok && s != "" {
			ids = append(ids, s)
		}
	}
	return stringList(ids)
}

// listStrings is a known list's strings; nil when it is null or unknown.
func listStrings(l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := make([]string, 0, len(l.Elements()))
	for _, e := range l.Elements() {
		if s, ok := e.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			out = append(out, s.ValueString())
		}
	}
	return out
}

// vmDevicesSpec adds the devices the configuration writes, an empty list
// included (that is how they are removed); left out, nothing is sent and the
// platform keeps what the machine has.
func vmDevicesSpec(cfg types.List, spec map[string]any) {
	if cfg.IsNull() || cfg.IsUnknown() {
		return
	}
	spec["devices"] = listStrings(cfg)
}

// vmDeviceErrors checks the devices as written: at most 8, each once. Ids not
// known yet, and whether the cluster has them, are left for the platform.
func vmDeviceErrors(l types.List) [][2]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var errs [][2]string
	if n := len(l.Elements()); n > maxMachineDevices {
		errs = append(errs, [2]string{"Too many devices",
			fmt.Sprintf("A machine has at most %d devices; this one lists %d.", maxMachineDevices, n)})
	}
	seen := map[string]bool{}
	for _, id := range listStrings(l) {
		if seen[id] {
			errs = append(errs, [2]string{"A device listed twice",
				fmt.Sprintf("%q is in devices twice; a machine has each device once.", id)})
			continue
		}
		seen[id] = true
	}
	return errs
}

// settleVMDevices resolves devices a write left unknown from the platform's
// copy (nil when it could not be read: none), and, when the write sent them,
// warns when the platform holds others: a platform without devices for
// machines drops them.
func settleVMDevices(m *vmResourceModel, sp map[string]any, sent bool, diags *diag.Diagnostics) {
	held := readVMDevices(sp)
	if m.Devices.IsUnknown() {
		m.Devices = held
		return
	}
	if !sent || sp == nil || m.Devices.IsNull() {
		return
	}
	want, have := listStrings(m.Devices), listStrings(held)
	if slices.Equal(want, have) {
		return
	}
	diags.AddWarning("Devices not kept as written",
		fmt.Sprintf("%q has %s, not %s. A platform without devices for machines doesn't keep them: the next plan "+
			"shows the difference.", m.Name.ValueString(), deviceWords(have), deviceWords(want)))
}

// deviceWords writes a machine's devices out for a message.
func deviceWords(ids []string) string {
	if len(ids) == 0 {
		return "no devices"
	}
	return strings.Join(ids, ", ")
}

// readVMTimeZone is the machine's time zone as the platform holds it: null
// when it holds none (the cluster's zone).
func readVMTimeZone(sp map[string]any) types.String {
	if z, _ := sp["timeZone"].(string); z != "" {
		return types.StringValue(z)
	}
	return types.StringNull()
}

// vmTimeZoneSpec adds the machine's time zone to every write that has one, so
// the platform keeps it.
func vmTimeZoneSpec(m vmResourceModel, spec map[string]any) {
	if known(m.TimeZone) && m.TimeZone.ValueString() != "" {
		spec["timeZone"] = m.TimeZone.ValueString()
	}
}

// timeZoneChange refuses a new time zone on an existing machine: the machine
// takes it when it is made and keeps its own after that. Nil when the plan
// keeps it or doesn't know it yet; a replacement is never asked here.
func timeZoneChange(plan, state vmResourceModel) *[2]string {
	if !known(plan.TimeZone) || state.TimeZone.IsUnknown() {
		return nil
	}
	from, to := state.TimeZone.ValueString(), plan.TimeZone.ValueString()
	if from == to {
		return nil
	}
	keep := "leave time_zone out"
	if from != "" {
		keep = fmt.Sprintf("keep time_zone = %q or leave it out", from)
	}
	return &[2]string{"A machine's time zone is set when it is made",
		fmt.Sprintf("%s takes its time zone when it is made; after that the zone is the machine's own: change it on "+
			"the machine itself, and %s here.", plan.Name.ValueString(), keep)}
}

// settleVMTimeZone resolves a time zone a write left unknown from the
// platform's copy, and warns when the platform holds another than the write
// sent: a platform without time zones for machines drops it, and the machine
// runs on the cluster's zone.
func settleVMTimeZone(m *vmResourceModel, sp map[string]any, diags *diag.Diagnostics) {
	held := readVMTimeZone(sp)
	if m.TimeZone.IsUnknown() {
		m.TimeZone = held
		return
	}
	if !known(m.TimeZone) || m.TimeZone.Equal(held) {
		return
	}
	diags.AddWarning("Time zone not kept as written",
		fmt.Sprintf("%q doesn't hold time_zone %q. A platform without time zones for machines doesn't keep it, and the "+
			"machine runs on the cluster's zone.", m.Name.ValueString(), m.TimeZone.ValueString()))
}
