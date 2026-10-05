package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The browser engines. The platform stores an engine only when it is not
// Chrome: a workload without one runs Chrome.
const (
	engineChrome   = "chrome"
	engineCamoufox = "camoufox"
)

// engineAttribute is the engine argument of livellm_browser and
// livellm_browser_api. It is fixed at creation: another value replaces the
// resource.
func engineAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{
		Optional:      true,
		Computed:      true,
		Description:   description,
		Validators:    []validator.String{stringvalidator.OneOf(engineChrome, engineCamoufox)},
		PlanModifiers: []planmodifier.String{engineReplace{}},
	}
}

// engineSpec puts the engine into a create or update body: only Camoufox is
// sent, so a Chrome body is what 0.12.0 sent.
func engineSpec(spec map[string]any, v types.String) {
	if v.ValueString() == engineCamoufox {
		spec["engine"] = engineCamoufox
	}
}

// readEngine is the engine the platform holds: absent means Chrome.
func readEngine(raw any) types.String {
	if s, _ := raw.(string); s != "" {
		return types.StringValue(s)
	}
	return types.StringValue(engineChrome)
}

// engineReplace plans the engine and replaces the resource when it changes.
// Left out of the configuration, the engine is Chrome. A state with no engine
// (written by 0.12.0 or older) holds a Chrome browser: "chrome" over it is no
// change, and while nothing else changes it keeps its null until a refresh
// reads "chrome", so even a plan without refresh plans nothing. (A schema
// default would plan "chrome" over that null, and the framework would then
// plan every computed attribute again: an update with nothing to send.)
type engineReplace struct{}

func (engineReplace) Description(context.Context) string {
	return "Defaults to \"chrome\". Changing the engine replaces the resource."
}

func (m engineReplace) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (engineReplace) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	if req.ConfigValue.IsNull() {
		switch {
		case req.State.Raw.IsNull():
			resp.PlanValue = types.StringValue(engineChrome) // create
			return
		case req.StateValue.IsNull() && req.PlanValue.IsNull():
			return // a 0.12.0 state and nothing else to change: keep it as it is
		}
		resp.PlanValue = types.StringValue(engineChrome)
	}
	if !req.State.Raw.IsNull() && engineChanged(req.StateValue, resp.PlanValue) {
		resp.RequiresReplace = true
	}
}

// engineChanged: whether going from the stored engine to the planned one is
// a change of engine. A null stored value is Chrome; an unknown planned value
// may be another engine.
func engineChanged(state, plan types.String) bool {
	if state.IsUnknown() {
		return false
	}
	was := engineChrome
	if !state.IsNull() {
		was = state.ValueString()
	}
	if plan.IsUnknown() {
		return true
	}
	now := engineChrome
	if !plan.IsNull() {
		now = plan.ValueString()
	}
	return was != now
}
