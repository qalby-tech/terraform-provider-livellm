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

// apiDiag turns a client error into an actionable diagnostic. The one case
// with dedicated wording is 402: the workspace plan's resource pool is
// exhausted — the fix is a plan change, not a config change.
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
	diags.AddError(summary, err.Error())
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
			Validators: []validator.String{stringvalidator.OneOf("auto", "host", "region")},
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
// holds, null for what it doesn't (all three null when it runs automatically).
func readPlacement(sp map[string]any) (strategy, host, region types.String) {
	strategy, host, region = types.StringNull(), types.StringNull(), types.StringNull()
	pl, _ := sp["placement"].(map[string]any)
	str := func(key string) types.String {
		if v, _ := pl[key].(string); v != "" {
			return types.StringValue(v)
		}
		return types.StringNull()
	}
	if pl != nil {
		strategy, host, region = str("strategy"), str("host"), str("region")
	}
	return strategy, host, region
}

// refreshPlacement puts the platform's placement into state. A resource that
// runs automatically and was written as automatic (left out, or "auto",
// perhaps with a host or region next to it) keeps what was written, so it
// plans no change; anything else takes the platform's values, so a location
// changed in the console shows in the plan and an import is complete.
func refreshPlacement(sp map[string]any, strategy, host, region *types.String) {
	s, h, r := readPlacement(sp)
	if s.IsNull() && placementSpec(*strategy, *host, *region) == nil {
		return
	}
	*strategy, *host, *region = s, h, r
}
