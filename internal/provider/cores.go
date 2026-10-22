package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// A machine's cores: shared (the default) or dedicated, every core reserved
// on its server; with dedicated cores, on servers whose cores run at
// different clock speeds, a speed asked for (core_speed, MHz) or kept on
// mixed speeds (mixed_speeds). The platform keeps each when a save leaves it
// out, clears it with false or 0, and drops core_speed and mixed_speeds on
// shared cores; it holds only true and a clock, so absent reads as false and
// 0. Where the cores are is the platform's (the machine's pin), never sent.

// keepCoreChoice plans a core setting the configuration leaves out as what
// the machine already has, and on a create as the platform's default (false,
// 0): unlike a size, nothing is chosen for it, so the plan knows it at once.
// A Default would send false on every save that leaves it out and turn off
// what the console set.
type keepCoreChoice struct{}

func (keepCoreChoice) Description(context.Context) string {
	return "Keeps the machine's current value when the configuration leaves it out; false or 0 on a new machine."
}

func (m keepCoreChoice) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (keepCoreChoice) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.BoolValue(false)
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepCoreChoice) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if !req.ConfigValue.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		resp.PlanValue = types.Int64Value(0)
		return
	}
	resp.PlanValue = req.StateValue
}

// readVMCores fills the machine's core settings from the platform's copy:
// what it doesn't hold is false and 0.
func readVMCores(m *vmResourceModel, sp map[string]any) {
	m.DedicatedCores = types.BoolValue(sp["dedicatedCores"] == true)
	m.MixedSpeeds = types.BoolValue(sp["mixedSpeeds"] == true)
	m.CoreSpeed = types.Int64Value(0)
	if f, ok := number(sp["coreSpeed"]); ok && f > 0 {
		m.CoreSpeed = types.Int64Value(int64(f))
	}
}

// settleVMCores resolves the core settings a write left unknown from the
// machine's settings (nil when they could not be read: false and 0).
func settleVMCores(m *vmResourceModel, sp map[string]any) {
	var read vmResourceModel
	readVMCores(&read, sp)
	if m.DedicatedCores.IsUnknown() {
		m.DedicatedCores = read.DedicatedCores
	}
	if m.MixedSpeeds.IsUnknown() {
		m.MixedSpeeds = read.MixedSpeeds
	}
	m.CoreSpeed = settleInt64(m.CoreSpeed, read.CoreSpeed)
}

// vmCoresSpec adds the core settings a write carries: each one known, false
// and 0 included (that is how one is cleared). The pin is never sent.
func vmCoresSpec(m vmResourceModel, spec map[string]any) {
	if known(m.DedicatedCores) {
		spec["dedicatedCores"] = m.DedicatedCores.ValueBool()
	}
	if known(m.MixedSpeeds) {
		spec["mixedSpeeds"] = m.MixedSpeeds.ValueBool()
	}
	if known(m.CoreSpeed) {
		spec["coreSpeed"] = m.CoreSpeed.ValueInt64()
	}
}

// planVMCores settles the plan's core settings against dedicated_cores: on
// shared cores the platform drops core_speed and mixed_speeds, so left out
// they plan as 0 and false, and set they are refused (else the plan would
// show them again after every apply). While dedicated_cores is unknown, so
// are the ones left out. A machine read before the platform had the settings
// (null) is left as it is.
func planVMCores(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var dedicated, mixed, cfgMixed types.Bool
	var speed, cfgSpeed types.Int64
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("dedicated_cores"), &dedicated)...)
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("mixed_speeds"), &mixed)...)
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("core_speed"), &speed)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("mixed_speeds"), &cfgMixed)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("core_speed"), &cfgSpeed)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range sharedCoreErrors(dedicated, cfgMixed, cfgSpeed) {
		resp.Diagnostics.AddAttributeError(path.Root(e[0]), e[1], e[2])
	}
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case dedicated.IsUnknown():
		if cfgMixed.IsNull() {
			mixed = types.BoolUnknown()
		}
		if cfgSpeed.IsNull() {
			speed = types.Int64Unknown()
		}
	case dedicated.IsNull() || dedicated.ValueBool():
		return
	default:
		if cfgMixed.IsNull() && !mixed.IsNull() {
			mixed = types.BoolValue(false)
		}
		if cfgSpeed.IsNull() && !speed.IsNull() {
			speed = types.Int64Value(0)
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("mixed_speeds"), mixed)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("core_speed"), speed)...)
}

// sharedCoreErrors refuses core_speed and mixed_speeds on a machine whose
// cores are planned shared: {attribute, summary, detail}.
func sharedCoreErrors(dedicated, cfgMixed types.Bool, cfgSpeed types.Int64) [][3]string {
	if !known(dedicated) || dedicated.ValueBool() {
		return nil
	}
	var errs [][3]string
	if known(cfgSpeed) && cfgSpeed.ValueInt64() > 0 {
		errs = append(errs, [3]string{"core_speed", "core_speed is for dedicated cores",
			"Shared cores run at whatever speed the server gives them. Set dedicated_cores = true to ask for " +
				"cores of one speed, or leave core_speed out."})
	}
	if known(cfgMixed) && cfgMixed.ValueBool() {
		errs = append(errs, [3]string{"mixed_speeds", "mixed_speeds is for dedicated cores",
			"Shared cores may always run at different speeds. Set dedicated_cores = true, or leave mixed_speeds out."})
	}
	return errs
}

// warnCoresNotKept warns when the platform holds other core settings than
// the write sent: a platform older than this provider ignores them, and the
// machine's cores stay shared.
func warnCoresNotKept(id string, planned vmResourceModel, sp map[string]any, diags *diag.Diagnostics) {
	if sp == nil {
		return
	}
	var held vmResourceModel
	readVMCores(&held, sp)
	var differ []string
	if known(planned.DedicatedCores) && !planned.DedicatedCores.Equal(held.DedicatedCores) {
		differ = append(differ, "dedicated_cores")
	}
	if known(planned.CoreSpeed) && !planned.CoreSpeed.Equal(held.CoreSpeed) {
		differ = append(differ, "core_speed")
	}
	if known(planned.MixedSpeeds) && !planned.MixedSpeeds.Equal(held.MixedSpeeds) {
		differ = append(differ, "mixed_speeds")
	}
	if len(differ) == 0 {
		return
	}
	diags.AddWarning("Cores not kept as written",
		fmt.Sprintf("%q doesn't hold %s as written. A platform older than this provider doesn't have dedicated "+
			"cores, so the machine's cores stay shared: the next plan shows the difference.", id, strings.Join(differ, ", ")))
}

// runningCores fills the clock of a running machine's cores from its status
// row: null when the platform doesn't say (stopped, starting, servers that
// list no speeds, a platform without dedicated cores).
func runningCores(st *client.WorkloadStatus) (speed types.Int64, mixed types.Bool) {
	if st == nil || st.Cores == nil {
		return types.Int64Null(), types.BoolNull()
	}
	if st.Cores.Mixed || st.Cores.SpeedMHz <= 0 {
		return types.Int64Null(), types.BoolValue(true)
	}
	return types.Int64Value(int64(st.Cores.SpeedMHz)), types.BoolValue(false)
}

// coresRefusals are the platform's answers when a machine's cores or room
// can't be had, by its code (409, or 422 for core_speed_unknown): what the
// diagnostic is called and what to write instead. The platform's own words
// come first in the detail; the console's buttons ("Use N cores", "Continue
// with mixed speeds") are what to write in the configuration.
var coresRefusals = map[string]struct {
	summary string
	next    func(fit int) string
}{
	"core_speed_short":  {"Too few cores at that speed", useCoresOrMixed},
	"core_speeds_mixed": {"Mixed core speeds", useCoresOrMixed},
	"core_speed_unknown": {"No cores at that speed", func(int) string {
		return "Set core_speed to a speed the servers have, or to 0 for any cores of one speed."
	}},
	"server_full": {"No room for this machine", func(int) string {
		return "Stop or shrink another machine, or give this one fewer cores or less memory, then apply again."
	}},
}

// useCoresOrMixed is the console's two buttons, written as configuration.
func useCoresOrMixed(fit int) string {
	mixed := "Continue with mixed speeds: set mixed_speeds = true (the cores may run at different clock speeds)."
	if fit <= 0 {
		return mixed
	}
	return fmt.Sprintf("Use %d cores: set cpus = %d. %s", fit, fit, mixed)
}

// coresDiag reports a cores refusal in its own words; false when err is not
// one.
func coresDiag(diags *diag.Diagnostics, summary string, apiErr *client.APIError) bool {
	if apiErr.Status != 409 && apiErr.Status != 422 {
		return false
	}
	r, ok := coresRefusals[apiErr.Code()]
	if !ok {
		return false
	}
	fit, _ := apiErr.Count("fit")
	diags.AddError(r.summary, fmt.Sprintf("%s: %s\n\n%s", summary, apiErr.Message(), r.next(fit)))
	return true
}
