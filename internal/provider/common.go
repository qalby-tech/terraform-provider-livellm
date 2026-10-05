package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// apiDiag turns a client error into an actionable diagnostic. Two cases have
// dedicated wording: 402, the workspace plan's resource pool is exhausted (the
// fix is a plan change, not a config change); a 403 for letting one resource
// reach another without the Network permission; and a 403 that names proxies,
// which only a platform from before the proxies permission was dropped sends:
// it is shown in the platform's words and says where it comes from.
func apiDiag(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 402 {
		diags.AddError(
			"Workspace plan pool exceeded",
			fmt.Sprintf("%s: %s\n\nThe workspace's plan does not have enough free CPU/RAM/disk for this. "+
				"Raise the plan (or enable metered billing) at https://cloud.live-llm.com/billing, "+
				"or free resources first.", summary, apiErr.Body),
		)
		return
	}
	if errors.As(err, &apiErr) && apiErr.Status == 403 && networkRefusal(apiErr) {
		diags.AddError(
			"This key can't let resources reach each other",
			fmt.Sprintf("%s: %s\n\nThrough a key without the Network permission, letting a resource reach another "+
				"one (reachable_from, a database block, starts_after, a browser put into a Browser API that others may "+
				"reach, or all_browsers turned on) is refused, except between resources this key made itself or when "+
				"the one reached already let the whole workspace in. Setting reachable_from = [\"*\"] is refused "+
				"whoever made the resource. A person turns on Network for the key on the workspace's API keys page; "+
				"until then keep reachable_from, the links and the Browser API's browsers as they were.",
				summary, apiErr.Message()),
		)
		return
	}
	if errors.As(err, &apiErr) && apiErr.Status == 403 && strings.Contains(strings.ToLower(apiErr.Message()), "prox") {
		diags.AddError(
			summary,
			fmt.Sprintf("%s: %s\n\nThis is an older platform: it still asks the API key for the proxies permission "+
				"before a proxy change (newer platforms don't). A person can turn it on for the key on the API "+
				"keys page; until then leave the proxy block as it is.", summary, apiErr.Message()),
		)
		return
	}
	diags.AddError(summary, err.Error())
}

// networkRefusal says whether a 403 is the platform refusing to let one
// resource reach another: its code, or its wording from a platform that
// sends no code.
func networkRefusal(e *client.APIError) bool {
	if e.Code() == "network_permission" {
		return true
	}
	return strings.Contains(e.Message(), "turn on Network")
}

// findWorkload returns the workload with the given id, or nil.
func findWorkload(ws []client.Workload, id string) *client.Workload {
	for i := range ws {
		if ws[i].ID == id {
			return &ws[i]
		}
	}
	return nil
}

// pollEvery is how often a wait looks at the workspace again.
var pollEvery = 5 * time.Second

// waitReady polls the workspace status until the workload reports ready,
// fails, or ctx (the per-resource timeout) expires. A stopped workload is
// waited for by existence only — a halted VM never turns ready. Right after a
// change the old version may still read ready: a status that says the change
// is still rolling out (updating) is waited through, so ready means the new
// spec is up. When the platform could not be read at the end (unreachable,
// or refusing), the error says so and why, next to the last status it saw.
func waitReady(ctx context.Context, c *client.Client, id string, stopped bool) error {
	var last string
	var lastErr error
	for {
		st, err := c.Status(ctx)
		if err != nil && ctx.Err() == nil {
			lastErr = err
		}
		if err == nil {
			lastErr = nil
			found := false
			for _, w := range st.Workloads {
				if w.ID != id {
					continue
				}
				found = true
				if stopped {
					return nil // it exists; halted is its desired state
				}
				if w.Ready && !w.Updating {
					return nil
				}
				if w.Phase == "Failed" {
					return fmt.Errorf("workload %q failed: %s", id, w.Message)
				}
				last = strings.TrimSpace(w.Phase + " " + w.Message)
			}
			if !found {
				last = "not in the workspace's status"
			}
		}
		select {
		case <-ctx.Done():
			switch {
			case lastErr != nil && last != "":
				last += "; then the workspace's status could not be read: " + lastErr.Error()
			case lastErr != nil:
				last = "the workspace's status could not be read: " + lastErr.Error()
			case last == "":
				last = "no status reported yet"
			}
			return fmt.Errorf("timed out waiting for %q to become ready (last status: %s)", id, last)
		case <-time.After(pollEvery):
		}
	}
}

// waitGone polls until the workload disappears from the workspace spec —
// deletes are asynchronous (the platform tears the resources down).
func waitGone(ctx context.Context, c *client.Client, id string) error {
	for {
		ws, err := c.Workloads(ctx)
		if err == nil && findWorkload(ws, id) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %q to be deleted", id)
		case <-time.After(5 * time.Second):
		}
	}
}

// statusOf fetches one workload's live status row (nil when absent).
func statusOf(ctx context.Context, c *client.Client, id string) (*client.WorkloadStatus, error) {
	st, err := c.Status(ctx)
	if err != nil {
		return nil, err
	}
	for i := range st.Workloads {
		if st.Workloads[i].ID == id {
			return &st.Workloads[i], nil
		}
	}
	return nil, nil
}

// placementAttributes is where a resource runs, the same three attributes on
// every resource: placement_strategy, placement_host, placement_region.
func placementAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"placement_strategy": schema.StringAttribute{
			Optional: true,
			Description: "Where it runs: omit for automatic (the default; LiveLLM picks the host), \"region\" for any " +
				"host in placement_region, \"host\" to pin placement_host. A resource pinned to a host waits for " +
				"that host while it is down.",
			Validators: []validator.String{stringvalidator.OneOf("auto", "host", "region"), placementValueValidator{}},
		},
		"placement_host": schema.StringAttribute{
			Optional:    true,
			Description: "Host id to pin to (placement_strategy = \"host\"); ids come from the livellm_hosts data source.",
		},
		"placement_region": schema.StringAttribute{
			Optional:    true,
			Description: "Region to run in (placement_strategy = \"region\").",
		},
	}
}

// withPlacement adds the placement attributes to a resource's attributes.
func withPlacement(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	for k, v := range placementAttributes() {
		attrs[k] = v
	}
	return attrs
}

// placementSpec is the placement object sent inside the resource's block, or
// nil for automatic (strategy left out or "auto"), which sends nothing.
func placementSpec(strategy, host, region types.String) map[string]any {
	s := strategy.ValueString()
	if s == "" || s == "auto" {
		return nil
	}
	pl := map[string]any{"strategy": s}
	if v := host.ValueString(); v != "" {
		pl["host"] = v
	}
	if v := region.ValueString(); v != "" {
		pl["region"] = v
	}
	return pl
}

// readPlacement reads a block's placement back: each value the platform
// holds, null for what it doesn't. A placement that runs automatically (none,
// "auto", or a strategy without its host or region) reads as all three null,
// the same rule placementSpec and the platform use.
func readPlacement(sp map[string]any) (strategy, host, region types.String) {
	strategy, host, region = types.StringNull(), types.StringNull(), types.StringNull()
	pl, _ := sp["placement"].(map[string]any)
	str := func(key string) types.String {
		if v, _ := pl[key].(string); v != "" {
			return types.StringValue(v)
		}
		return types.StringNull()
	}
	s, h, r := str("strategy"), str("host"), str("region")
	switch s.ValueString() {
	case "host":
		if h.IsNull() {
			return strategy, host, region
		}
	case "region":
		if r.IsNull() {
			return strategy, host, region
		}
	default:
		return strategy, host, region
	}
	return s, h, r
}

// refreshPlacement puts the platform's placement into state. A resource that
// runs automatically and was written as automatic (left out, or "auto",
// perhaps with a host or region next to it) keeps what was written, so it
// plans no change; anything else takes the platform's values, so a location
// changed in the console shows in the plan and an import is complete. A host
// or region written as "" stays "" where the platform holds none, since ""
// and left out send the same thing.
func refreshPlacement(sp map[string]any, strategy, host, region *types.String) {
	s, h, r := readPlacement(sp)
	if s.IsNull() && placementSpec(*strategy, *host, *region) == nil {
		return
	}
	keepEmpty := func(read types.String, was *types.String) types.String {
		if read.IsNull() && !was.IsNull() && !was.IsUnknown() && was.ValueString() == "" {
			return *was
		}
		return read
	}
	h, r = keepEmpty(h, host), keepEmpty(r, region)
	*strategy, *host, *region = s, h, r
}

// placementValueValidator refuses placement_strategy "host" without a
// placement_host, or "region" without a placement_region, at plan time
// rather than at apply. A value not known until apply passes.
type placementValueValidator struct{}

func (placementValueValidator) Description(context.Context) string {
	return "placement_strategy \"host\" needs placement_host, \"region\" needs placement_region"
}

func (v placementValueValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (placementValueValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var key string
	switch req.ConfigValue.ValueString() {
	case "host":
		key = "placement_host"
	case "region":
		key = "placement_region"
	default:
		return
	}
	var val types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, req.Path.ParentPath().AtName(key), &val)...)
	if resp.Diagnostics.HasError() || val.IsUnknown() {
		return
	}
	if val.IsNull() || val.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Missing "+key,
			fmt.Sprintf("placement_strategy = %q needs %s.", req.ConfigValue.ValueString(), key))
	}
}
