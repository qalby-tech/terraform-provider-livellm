package provider

import (
	"context"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// Sizes (cpu, memory, disks) are optional and computed on every resource: a
// resource that leaves one out is given its kind's default by the platform,
// which reads back into state, and a save that leaves it out keeps what the
// resource has.

// keepSizeWhenUnset plans a size the configuration leaves out as what the
// resource already has — including nothing, for a resource saved before the
// platform filled sizes in. UseStateForUnknown skips a null prior value, which
// left such a resource "(known after apply)" and updated on every plan; and
// sending the platform's CPU or memory to it would restart it with resources
// it never had. On a create (and the create half of a replacement) the value
// stays unknown until the platform has chosen it.
type keepSizeWhenUnset struct{}

func (keepSizeWhenUnset) Description(context.Context) string {
	return "Keeps the resource's current value when the configuration leaves it out."
}

func (m keepSizeWhenUnset) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (keepSizeWhenUnset) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepSizeWhenUnset) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepSizeWhenUnset) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

// sizeGi reads a size the platform reports ("10Gi", "1Ti", "2048Mi") as
// whole GiB; false for anything else (a size in Mi that isn't whole GiB, a
// decimal unit, a fraction).
func sizeGi(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for _, u := range []struct {
		suffix   string
		mul, div int64
	}{{"Ti", 1024, 1}, {"Gi", 1, 1}, {"Mi", 1, 1024}} {
		if !strings.HasSuffix(s, u.suffix) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(s, u.suffix), 10, 64)
		if err != nil || n < 0 || n%u.div != 0 {
			return 0, false
		}
		return n / u.div * u.mul, true
	}
	return 0, false
}

// readGi reads a size in GiB from the platform: null when it holds none, and
// what state had when it holds one that isn't whole GiB (rather than reading
// it as some other number).
func readGi(prev types.Int64, raw any) types.Int64 {
	s, _ := raw.(string)
	if strings.TrimSpace(s) == "" {
		return types.Int64Null()
	}
	if gi, ok := sizeGi(s); ok && gi > 0 {
		return types.Int64Value(gi)
	}
	if known(prev) {
		return prev
	}
	return types.Int64Null()
}

// readCount reads a whole number the platform holds (a machine's cores): null
// when it holds none.
func readCount(raw any) types.Int64 {
	if f, ok := number(raw); ok && f > 0 {
		return types.Int64Value(int64(f))
	}
	return types.Int64Null()
}

// readSize reads a cpu or memory string as the platform holds it, null when
// it holds none.
func readSize(raw any) types.String {
	if s, _ := raw.(string); strings.TrimSpace(s) != "" {
		return types.StringValue(s)
	}
	return types.StringNull()
}

// settleString resolves a size a write left unknown (left out on a create)
// from what the platform chose; a planned value comes back as planned.
func settleString(v types.String, raw any) types.String {
	if v.IsUnknown() {
		return readSize(raw)
	}
	return v
}

// settleInt64 is settleString for a number: read is what the platform holds.
func settleInt64(v, read types.Int64) types.Int64 {
	if v.IsUnknown() {
		return read
	}
	return v
}

// fetchWorkload is the workload as the platform holds it now, nil when it
// can't be read.
func fetchWorkload(ctx context.Context, c *client.Client, id string) *client.Workload {
	ws, err := c.Workloads(ctx)
	if err != nil {
		return nil
	}
	return findWorkload(ws, id)
}
