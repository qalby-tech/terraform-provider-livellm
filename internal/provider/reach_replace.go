package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// replacing says whether this plan replaces the resource: whether one of its
// attributes' own plan modifiers asks for a replacement. The framework
// doesn't show a resource's ModifyPlan what those modifiers decided, so they
// run again here (they only look at the request). A replacement asked for
// from outside (-replace, a tainted resource) isn't seen.
func replacing(ctx context.Context, req resource.ModifyPlanRequest) bool {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return false
	}
	sch, ok := req.Plan.Schema.(schema.Schema)
	if !ok {
		return false
	}
	for name, a := range sch.Attributes {
		p := path.Root(name)
		switch at := a.(type) {
		case schema.StringAttribute:
			var cv, pv, sv types.String
			req.Config.GetAttribute(ctx, p, &cv)
			req.Plan.GetAttribute(ctx, p, &pv)
			req.State.GetAttribute(ctx, p, &sv)
			for _, m := range at.PlanModifiers {
				r := planmodifier.StringRequest{Path: p, Config: req.Config, ConfigValue: cv, Plan: req.Plan, PlanValue: pv, State: req.State, StateValue: sv}
				resp := planmodifier.StringResponse{PlanValue: pv}
				m.PlanModifyString(ctx, r, &resp)
				if resp.RequiresReplace {
					return true
				}
				pv = resp.PlanValue
			}
		case schema.BoolAttribute:
			var cv, pv, sv types.Bool
			req.Config.GetAttribute(ctx, p, &cv)
			req.Plan.GetAttribute(ctx, p, &pv)
			req.State.GetAttribute(ctx, p, &sv)
			for _, m := range at.PlanModifiers {
				r := planmodifier.BoolRequest{Path: p, Config: req.Config, ConfigValue: cv, Plan: req.Plan, PlanValue: pv, State: req.State, StateValue: sv}
				resp := planmodifier.BoolResponse{PlanValue: pv}
				m.PlanModifyBool(ctx, r, &resp)
				if resp.RequiresReplace {
					return true
				}
				pv = resp.PlanValue
			}
		case schema.Int64Attribute:
			var cv, pv, sv types.Int64
			req.Config.GetAttribute(ctx, p, &cv)
			req.Plan.GetAttribute(ctx, p, &pv)
			req.State.GetAttribute(ctx, p, &sv)
			for _, m := range at.PlanModifiers {
				r := planmodifier.Int64Request{Path: p, Config: req.Config, ConfigValue: cv, Plan: req.Plan, PlanValue: pv, State: req.State, StateValue: sv}
				resp := planmodifier.Int64Response{PlanValue: pv}
				m.PlanModifyInt64(ctx, r, &resp)
				if resp.RequiresReplace {
					return true
				}
				pv = resp.PlanValue
			}
		}
	}
	return false
}

// warnReachReplace warns at plan when a replacement changes who reaches
// what inside the workspace. A replacement deletes the resource and makes a
// new one: made with reachable_from left out, the new one is closed (unless
// it joins a stack whose other services stay: it takes their value), and
// the platform drops the name from every other resource that let it in, so
// those don't let the new one in until a second apply writes the name back
// (or, where their configuration leaves reachable_from out, until someone
// sets it again). The workspace read is best effort: without it the warning
// says so in general words.
func warnReachReplace(ctx context.Context, data *providerData, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !replacing(ctx, req) {
		return
	}
	var name, newName types.String
	var had, cfg types.List
	req.State.GetAttribute(ctx, path.Root("name"), &name)
	req.Plan.GetAttribute(ctx, path.Root("name"), &newName)
	req.State.GetAttribute(ctx, path.Root("reachable_from"), &had)
	req.Config.GetAttribute(ctx, path.Root("reachable_from"), &cfg)
	id := name.ValueString()
	if id == "" {
		return
	}
	var ws []client.Workload
	err := errors.New("no platform")
	if data != nil && data.Client != nil {
		ws, err = data.Client.Workloads(ctx)
	}
	var parts []string
	if prev := reachOf(ctx, had); cfg.IsNull() && prev != nil && len(*prev) > 0 && !(err == nil && stackStays(ws, id)) {
		parts = append(parts, fmt.Sprintf("The new resource is made with reachable_from left out, so it starts closed to "+
			"the rest of the workspace; the one it replaces lets in %s. Write reachable_from in the configuration to keep that.",
			describeReach(prev)))
	}
	if newName.Equal(name) {
		const after = "The platform drops the name when the old %s is deleted, so after this apply they don't let the new one in: " +
			"where their configuration writes reachable_from, the next plan shows the difference and a second apply puts the " +
			"name back; where it leaves reachable_from out, set it again."
		if err != nil {
			parts = append(parts, fmt.Sprintf("Resources that let %s in by name lose that name. "+after, id, id))
		} else if n := namers(ws, id); len(n) > 0 {
			parts = append(parts, fmt.Sprintf("%s let %s in by name. "+after, strings.Join(n, ", "), id, id))
		}
	}
	if len(parts) == 0 {
		return
	}
	resp.Diagnostics.AddAttributeWarning(path.Root("reachable_from"),
		fmt.Sprintf("Replacing %s changes who reaches it inside the workspace", id), strings.Join(parts, "\n\n"))
}

// stackStays says whether id is a service of a stack that has other
// services: a new service of it takes the stack's value.
func stackStays(ws []client.Workload, id string) bool {
	w := findWorkload(ws, id)
	if w == nil {
		return false
	}
	stack, _ := w.Pod["stack"].(string)
	if stack == "" {
		return false
	}
	for _, o := range ws {
		if s, _ := o.Pod["stack"].(string); o.ID != id && s == stack {
			return true
		}
	}
	return false
}

// namers are the other resources whose reachable_from names id.
func namers(ws []client.Workload, id string) []string {
	var out []string
	for _, w := range ws {
		if w.ID == id || w.ReachableFrom == nil {
			continue
		}
		for _, n := range *w.ReachableFrom {
			if n == id {
				out = append(out, w.ID)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
