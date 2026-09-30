package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

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
