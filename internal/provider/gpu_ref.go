package provider

import (
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A machine's, an app's or a Desktop App's GPU: a rental of the workspace
// (livellm_gpu_rental's id). The platform keeps it when a save leaves it out
// and takes it off with "", so it is sent only when the plan knows it, and a
// configuration that leaves it out keeps what the resource has.

var gpuRefRe = regexp.MustCompile(`^(g-[0-9a-f]{8})?$`)

func gpuRefAttribute(what string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional: true,
		Computed: true,
		Description: "A GPU rental of the workspace this " + what + " uses: livellm_gpu_rental's id (g-…). " + gpuRefUse(what) +
			" Left out, it keeps what it has; \"\" takes the GPU off.",
		Validators:    []validator.String{stringvalidator.RegexMatches(gpuRefRe, `must be a GPU rental's id (g- and 8 hex digits), or "" for none`)},
		PlanModifiers: []planmodifier.String{keepWhenLeftOut{}},
	}
}

func gpuRefUse(what string) string {
	if what == "machine" {
		return "One for machines: the machine then runs on that GPU's server (one machine at a time; a change reaches a " +
			"running machine at its next restart)."
	}
	return "One for apps, which apps and Desktop Apps share."
}

// readGPURef is the resource's GPU as the platform holds it: its rental, or
// none; "" stays "" when that is what state says (so gpu = "" plans no
// change), else none is null.
func readGPURef(prior types.String, sp map[string]any) types.String {
	if g, _ := sp["gpu"].(string); g != "" {
		return types.StringValue(g)
	}
	if !prior.IsUnknown() && !prior.IsNull() && prior.ValueString() == "" {
		return prior
	}
	return types.StringNull()
}

// settleGPURef resolves a GPU a write left unknown (a rental made in the
// same apply) from the platform's copy; a planned one stays as planned.
func settleGPURef(v *types.String, sp map[string]any) {
	if v.IsUnknown() {
		*v = readGPURef(types.StringNull(), sp)
	}
}

// gpuRefSpec sends the GPU when the plan knows it, "" included.
func gpuRefSpec(v types.String, spec map[string]any) {
	if known(v) {
		spec["gpu"] = v.ValueString()
	}
}
