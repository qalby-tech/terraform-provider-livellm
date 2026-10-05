package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// reachAll is the value that lets every resource of the workspace in, also
// those made later. It goes alone.
const reachAll = "*"

// reachMaxItems and reachMaxLen are the platform's limits on reachable_from.
const (
	reachMaxItems = 64
	reachMaxLen   = 40
)

// reachName is what one entry of reachable_from may be: "*" or a resource's
// name (the platform's id rule).
var reachName = regexp.MustCompile(`^(\*|[a-z0-9]([-a-z0-9]*[a-z0-9])?)$`)

// reachDescription is the reachable_from text every resource shares; extra
// is what this kind of resource adds.
func reachDescription(extra string) string {
	d := "Which other resources of the workspace may connect to this one: their names (a service of an app made of " +
		"several services, or that app's stack, stands for all its services), or [\"*\"] for every resource in the " +
		"workspace, also those made later. Left out when creating: none, so a new resource is closed to the rest of " +
		"the workspace. Left out later: kept as it is; removing the attribute doesn't change it, [] closes it. " +
		"Whatever this says, a resource is reached by its own parts and by the apps that link it (a database block) " +
		"or wait for it (starts_after). Public addresses keep their own settings. Letting more in needs an API key " +
		"with the Network permission, unless this key made both resources; [\"*\"] always needs it."
	if extra != "" {
		d += " " + extra
	}
	return d
}

// reachAttribute is reachable_from. Optional + Computed: the platform holds a
// value for every resource, and a configuration that leaves it out keeps it.
func reachAttribute(extra string, stack bool) schema.ListAttribute {
	return schema.ListAttribute{
		Optional:    true,
		Computed:    true,
		ElementType: types.StringType,
		Description: reachDescription(extra),
		Validators:  []validator.List{reachValidator{stack: stack}},
		PlanModifiers: []planmodifier.List{
			listplanmodifier.UseStateForUnknown(),
		},
	}
}

// withReach adds reachable_from to a resource's attributes.
func withReach(attrs map[string]schema.Attribute, extra string, stack bool) map[string]schema.Attribute {
	attrs["reachable_from"] = reachAttribute(extra, stack)
	return attrs
}

// configuredReach is reachable_from as the configuration sets it, or nil when
// it leaves it out (or isn't known yet): nil sends nothing, so a create is
// closed and an update keeps what the platform holds. [] is a non-nil empty
// list: it closes the resource.
func configuredReach(ctx context.Context, cfg tfsdk.Config, diags *diag.Diagnostics) *[]string {
	var l types.List
	diags.Append(cfg.GetAttribute(ctx, path.Root("reachable_from"), &l)...)
	return reachOf(ctx, l)
}

// reachOf is a known list's values (never a nil slice), or nil.
func reachOf(ctx context.Context, l types.List) *[]string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	out := []string{}
	for _, e := range l.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() {
			return nil
		}
		out = append(out, s.ValueString())
	}
	return &out
}

// reachBody puts a configured reachable_from into a create body (flat, next
// to id); nothing when the configuration leaves it out.
func reachBody(body map[string]any, reach *[]string) {
	if reach != nil {
		body["reachableFrom"] = *reach
	}
}

// reachList is a list of names as a Terraform value.
func reachList(names []string) types.List {
	vals := make([]attr.Value, 0, len(names))
	for _, n := range names {
		vals = append(vals, types.StringValue(n))
	}
	return types.ListValueMust(types.StringType, vals)
}

// sameNames says whether two lists hold the same names, in any order.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, n := range a {
		seen[n]++
	}
	for _, n := range b {
		if seen[n] == 0 {
			return false
		}
		seen[n]--
	}
	return true
}

// readReach is reachable_from after a refresh: what the platform holds, null
// when it holds nothing (a platform that doesn't know the setting). The
// order written is kept while the platform holds the same names.
func readReach(ctx context.Context, was types.List, held *[]string) types.List {
	if held == nil {
		return types.ListNull(types.StringType)
	}
	if prev := reachOf(ctx, was); prev != nil && sameNames(*prev, *held) {
		return was
	}
	return reachList(*held)
}

// settleReach is reachable_from in the state an apply writes. A planned value
// that is known stays as planned: Terraform holds an apply to its plan, and
// the next refresh shows anything the platform holds instead. Unknown (a
// create that leaves it out) takes what the platform holds. When the
// configuration set a value the platform didn't keep, a warning says so.
func settleReach(ctx context.Context, c *client.Client, id string, planned types.List, sent *[]string, diags *diag.Diagnostics) types.List {
	if !planned.IsUnknown() && sent == nil {
		return planned
	}
	var held *[]string
	ws, err := c.Workloads(ctx)
	if err == nil {
		if w := findWorkload(ws, id); w != nil {
			held = w.ReachableFrom
		}
	}
	if sent != nil {
		if err == nil && (held == nil || !sameNames(*held, *sent)) {
			diags.AddWarning("reachable_from not kept as written",
				fmt.Sprintf("%q holds %s, not %s. A platform older than this provider doesn't keep the setting, "+
					"and the services of one app share one value: the next plan shows the difference.",
					id, describeReach(held), describeReach(sent)))
		}
		if !planned.IsUnknown() {
			return planned
		}
	}
	if held == nil {
		return types.ListNull(types.StringType)
	}
	return reachList(*held)
}

// describeReach writes a value out for a message.
func describeReach(v *[]string) string {
	switch {
	case v == nil:
		return "no setting"
	case len(*v) == 0:
		return "nothing (closed)"
	case len(*v) == 1 && (*v)[0] == reachAll:
		return "the whole workspace"
	}
	return strings.Join(*v, ", ")
}

// reachValidator checks reachable_from at plan the way the platform does at
// apply: "*" goes alone, names are resource names, none twice, at most 64,
// and never the resource itself (or, for a service, its own stack: the
// services of one app always reach each other). Values known only at apply
// pass.
type reachValidator struct {
	stack bool
}

func (reachValidator) Description(context.Context) string {
	return "\"*\" goes alone; resource names, each once, at most 64, never the resource itself"
}

func (v reachValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v reachValidator) ValidateList(ctx context.Context, req validator.ListRequest, resp *validator.ListResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	var self, stack types.String
	req.Config.GetAttribute(ctx, path.Root("name"), &self)
	if v.stack {
		req.Config.GetAttribute(ctx, path.Root("stack"), &stack)
	}
	for _, e := range reachErrors(req.ConfigValue, self, stack) {
		resp.Diagnostics.AddAttributeError(req.Path, e[0], e[1])
	}
}

// reachErrors are the plan-time refusals of one reachable_from: {summary,
// detail}.
func reachErrors(l types.List, self, stack types.String) [][2]string {
	var errs [][2]string
	elems := l.Elements()
	if len(elems) > reachMaxItems {
		errs = append(errs, [2]string{"Too many names in reachable_from",
			fmt.Sprintf("reachable_from lists %d names; at most %d. Use [\"*\"] for the whole workspace.", len(elems), reachMaxItems)})
	}
	seen := map[string]bool{}
	all := false
	for _, e := range elems {
		s, ok := e.(types.String)
		if !ok || s.IsUnknown() || s.IsNull() {
			continue
		}
		n := s.ValueString()
		switch {
		case n == reachAll:
			all = true
		case len(n) > reachMaxLen || !reachName.MatchString(n):
			errs = append(errs, [2]string{"Not a resource name",
				fmt.Sprintf("%q in reachable_from isn't a resource name: lowercase letters, digits and hyphens, "+
					"at most %d characters, starting and ending with a letter or digit.", n, reachMaxLen)})
		case !self.IsNull() && !self.IsUnknown() && n == self.ValueString():
			errs = append(errs, [2]string{"A resource always reaches itself",
				fmt.Sprintf("%q is this resource; leave it out of reachable_from.", n)})
		case !stack.IsNull() && !stack.IsUnknown() && n == stack.ValueString():
			errs = append(errs, [2]string{"The services of an app always reach each other",
				fmt.Sprintf("%q is this service's own stack; leave it out of reachable_from.", n)})
		}
		if seen[n] {
			errs = append(errs, [2]string{"A name twice in reachable_from",
				fmt.Sprintf("reachable_from lists %q twice.", n)})
		}
		seen[n] = true
	}
	if all && len(elems) > 1 {
		errs = append(errs, [2]string{"\"*\" goes alone",
			"\"*\" in reachable_from is the whole workspace, so it goes alone: [\"*\"]."})
	}
	return errs
}
