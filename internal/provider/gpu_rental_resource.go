package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_gpu_rental — one GPU rental: one GPU (model and use), or one
// cluster of GPUs on one server (cluster). Renting spends money, so this
// resource never rents again by itself:
//
//   - Read never takes a rental out of state. One that ended (or dropped out
//     of the platform's list, which keeps ended rentals 30 days) stays, with
//     status "ended", and plans no change while its configuration is as it
//     was.
//   - Nothing about a rental is replaced: what the platform can change in
//     place is changed (a later end, the use, fewer GPUs of a model), and
//     anything else is an error at plan time that says to rent again on
//     purpose (taint, or a new resource).
//   - A cancel the platform charges in full (428) is confirmed only with
//     allow_charged_cancel = true.
type gpuRentalResource struct {
	data *providerData
}

func NewGPURentalResource() resource.Resource {
	return &gpuRentalResource{}
}

func (r *gpuRentalResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gpu_rental"
}

// gpuHourRe is a time as the platform takes it: a whole UTC hour.
var gpuHourRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:00:00Z$`)

// gpuPollEvery is how often a change of use is checked while the GPUs get
// ready for it (a variable so tests don't wait).
var gpuPollEvery = 5 * time.Second

// gpuNow is the clock (a variable for tests).
var gpuNow = time.Now

func gpuLocationBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		Description: "Where the GPUs may go, when they are rented: mode \"auto\" (any server this workspace may use; " +
			"the default), \"region\" (with region) or \"host\" (with host). Fixed once rented.",
		Attributes: map[string]schema.Attribute{
			"mode": schema.StringAttribute{
				Optional:    true,
				Description: "auto, region or host.",
				Validators:  []validator.String{stringvalidator.OneOf("auto", "region", "host")},
			},
			"region": schema.StringAttribute{Optional: true, Description: "The region, with mode = \"region\"."},
			"host":   schema.StringAttribute{Optional: true, Description: "The server, with mode = \"host\"."},
		},
	}
}

func gpuTargetBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		Description: "A booking only: what gets the GPUs at its start, a machine (use = \"machine\") or apps and " +
			"Desktop Apps (use = \"apps\"). Fixed once booked.",
		Attributes: map[string]schema.Attribute{
			"machine": schema.StringAttribute{Optional: true, Description: "A machine of the workspace (machine use)."},
			"apps": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Apps and Desktop Apps of the workspace (apps use).",
			},
		},
	}
}

var gpuCardAttrTypes = map[string]attr.Type{
	"id":        types.StringType,
	"model":     types.StringType,
	"name":      types.StringType,
	"state":     types.StringType,
	"answering": types.BoolType,
}

func (r *gpuRentalResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	use := func(what string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true,
			Description: what + ": \"machine\" (one machine gets the GPUs) or \"apps\" (apps and Desktop Apps share them). " +
				"A change on a running rental changes its use in place (at most once an hour; nothing may use it meanwhile).",
			Validators: []validator.String{stringvalidator.OneOf("machine", "apps")},
		}
	}
	computedString := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Rents a GPU, or a cluster of GPUs on one server, by the hour; or books them for later. " +
			"Renting spends money: Terraform never rents again by itself, and an ended rental stays in state.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The rental's id (g-…): what a machine's, an app's or a Desktop App's gpu takes.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"model": schema.StringAttribute{
				Optional: true,
				Description: "One GPU of this model (\"rtx5090\"; livellm_gpu_models lists them), with use. Or write a " +
					"cluster block instead. Fixed once rented.",
			},
			"use": use("With model"),
			"starts_at": schema.StringAttribute{
				Optional: true,
				Description: "Book for later: the start, a whole UTC hour (\"2026-11-20T10:00:00Z\"), with ends_at. Left " +
					"out, the GPUs are rented now. Fixed once rented. A booking is charged every booked hour, used or not.",
				Validators: []validator.String{stringvalidator.RegexMatches(gpuHourRe, "must be a whole UTC hour, e.g. 2026-11-20T10:00:00Z")},
			},
			"ends_at": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "The end, a whole UTC hour. Left out on a rental now, it runs until released (or until a " +
					"booking ahead needs the GPU: the platform then sets the end, read here). A later value extends the " +
					"rental in place; an earlier one is refused at plan time.",
				Validators:    []validator.String{stringvalidator.RegexMatches(gpuHourRe, "must be a whole UTC hour, e.g. 2026-11-20T14:00:00Z")},
				PlanModifiers: []planmodifier.String{keepStateWhenLeftOut{}},
			},
			"allow_charged_cancel": schema.BoolAttribute{
				Optional: true,
				Description: "Let destroy (and taking booked GPUs off a cluster) go ahead when the platform charges it in " +
					"full: a booking cancelled within 24 hours of its start. Off (the default), such a destroy stops with an " +
					"error and nothing is cancelled. Apply the change first, then destroy.",
			},
			"name":       computedString("The rental's name (\"RTX 5090\", \"2 × RTX 5090\")."),
			"status":     computedString("booked, delayed (a booking waiting for a free GPU), active, ended or cancelled. A rental that ended stays in state as ended (or cancelled)."),
			"phase":      computedString("ready, waiting, switching-to-machine, switching-to-apps or failed."),
			"booked":     schema.BoolAttribute{Computed: true, Description: "Booked for later."},
			"host":       computedString("The server the GPUs are on (a booking's once picked)."),
			"region":     computedString("That server's region."),
			"started_at": computedString("When it started."),
			"ended_at":   computedString("When it ended."),
			"ended_reason": computedString("Why it ended: released, end, cancelled, freeze, workspace_deleted, card_missing, admin, " +
				"outage or compute_stop."),
			"used_by": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The resources using it now.",
			},
			"cards": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Its GPUs (one for a single GPU).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":    computedString("The GPU's id within the rental."),
					"model": computedString("Its model."),
					"name":  computedString("Its model's name."),
					"state": computedString("Its state."),
					"answering": schema.BoolAttribute{Computed: true, Description: "False while it isn't working (a machine on " +
						"the cluster waits until it is taken off; fewer cards of its model takes it off first)."},
				}},
			},
			"hours_this_month": schema.Float64Attribute{Computed: true, Description: "Its hours this month."},
			"cents_this_month": schema.Int64Attribute{Computed: true, Description: "Its charge this month in US cents " +
				"(every GPU of it); null where it isn't charged. What it is charged in all is said when it is released."},
		},
		Blocks: map[string]schema.Block{
			"location": gpuLocationBlock(),
			"target":   gpuTargetBlock(),
			"cluster": schema.SingleNestedBlock{
				Description: "Several GPUs on one server, rented together (instead of model and use). Fewer GPUs of a " +
					"model takes them off in place; more GPUs, another model or another location is refused at plan time.",
				Attributes: map[string]schema.Attribute{
					"use": use("The cluster's use"),
					"cards": schema.MapAttribute{
						Optional:    true,
						ElementType: types.Int64Type,
						Description: "How many GPUs of each model (1-8 each): { rtx5090 = 2 }.",
					},
				},
				Blocks: map[string]schema.Block{
					"location": gpuLocationBlock(),
					"target":   gpuTargetBlock(),
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Update: true}),
		},
	}
}

func (r *gpuRentalResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.data = data
}

type gpuLocationModel struct {
	Mode   types.String `tfsdk:"mode"`
	Region types.String `tfsdk:"region"`
	Host   types.String `tfsdk:"host"`
}

type gpuTargetModel struct {
	Machine types.String `tfsdk:"machine"`
	Apps    types.List   `tfsdk:"apps"`
}

type gpuClusterModel struct {
	Use      types.String      `tfsdk:"use"`
	Cards    types.Map         `tfsdk:"cards"`
	Location *gpuLocationModel `tfsdk:"location"`
	Target   *gpuTargetModel   `tfsdk:"target"`
}

type gpuRentalModel struct {
	ID                 types.String      `tfsdk:"id"`
	Model              types.String      `tfsdk:"model"`
	Use                types.String      `tfsdk:"use"`
	StartsAt           types.String      `tfsdk:"starts_at"`
	EndsAt             types.String      `tfsdk:"ends_at"`
	AllowChargedCancel types.Bool        `tfsdk:"allow_charged_cancel"`
	Location           *gpuLocationModel `tfsdk:"location"`
	Target             *gpuTargetModel   `tfsdk:"target"`
	Cluster            *gpuClusterModel  `tfsdk:"cluster"`
	Name               types.String      `tfsdk:"name"`
	Status             types.String      `tfsdk:"status"`
	Phase              types.String      `tfsdk:"phase"`
	Booked             types.Bool        `tfsdk:"booked"`
	Host               types.String      `tfsdk:"host"`
	Region             types.String      `tfsdk:"region"`
	StartedAt          types.String      `tfsdk:"started_at"`
	EndedAt            types.String      `tfsdk:"ended_at"`
	EndedReason        types.String      `tfsdk:"ended_reason"`
	UsedBy             types.List        `tfsdk:"used_by"`
	Cards              types.List        `tfsdk:"cards"`
	HoursThisMonth     types.Float64     `tfsdk:"hours_this_month"`
	CentsThisMonth     types.Int64       `tfsdk:"cents_this_month"`
	Timeouts           timeouts.Value    `tfsdk:"timeouts"`
}

// over says whether state holds a rental that is over for good.
func (m gpuRentalModel) over() bool {
	s := m.Status.ValueString()
	return s == "ended" || s == "cancelled"
}

// use is the rental's use as written (model's, or the cluster's).
func (m gpuRentalModel) use() types.String {
	if m.Cluster != nil {
		return m.Cluster.Use
	}
	return m.Use
}

// keepStateWhenLeftOut plans an optional+computed attribute the
// configuration leaves out as what state holds, null included (so a rental
// with no end plans no change), and leaves it unknown on a create, where the
// platform decides.
type keepStateWhenLeftOut struct{}

func (keepStateWhenLeftOut) Description(context.Context) string {
	return "Keeps what state holds when the configuration leaves it out."
}

func (m keepStateWhenLeftOut) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (keepStateWhenLeftOut) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() || req.State.Raw.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

// --- what is written -------------------------------------------------------

func locationOf(l *gpuLocationModel) *client.GPULocation {
	if l == nil {
		return nil
	}
	out := &client.GPULocation{Mode: l.Mode.ValueString(), Region: l.Region.ValueString(), Host: l.Host.ValueString()}
	if *out == (client.GPULocation{}) {
		return nil
	}
	return out
}

func targetOf(t *gpuTargetModel) *client.GPUTarget {
	if t == nil {
		return nil
	}
	out := &client.GPUTarget{Machine: t.Machine.ValueString(), Apps: listStrings(t.Apps)}
	if out.Machine == "" && len(out.Apps) == 0 {
		return nil
	}
	return out
}

// cardCounts is a cluster's cards as written: a count per model.
func cardCounts(m types.Map) map[string]int {
	out := map[string]int{}
	if m.IsNull() || m.IsUnknown() {
		return out
	}
	for k, v := range m.Elements() {
		if n, ok := v.(types.Int64); ok && !n.IsNull() && !n.IsUnknown() {
			out[k] = int(n.ValueInt64())
		}
	}
	return out
}

func countsMap(counts map[string]int) types.Map {
	vals := map[string]attr.Value{}
	for k, n := range counts {
		vals[k] = types.Int64Value(int64(n))
	}
	return types.MapValueMust(types.Int64Type, vals)
}

// gpuRentBody is POST /v1/gpus's body for the configuration: one GPU, or
// one cluster.
func gpuRentBody(m gpuRentalModel) client.GPURentRequest {
	b := client.GPURentRequest{StartsAt: m.StartsAt.ValueString()}
	if known(m.EndsAt) {
		b.EndsAt = m.EndsAt.ValueString()
	}
	if c := m.Cluster; c != nil {
		b.Clusters = []client.GPUClusterAsk{{
			Use: c.Use.ValueString(), Cards: cardCounts(c.Cards),
			Location: locationOf(c.Location), Target: targetOf(c.Target),
		}}
		return b
	}
	b.Model, b.Use = m.Model.ValueString(), m.Use.ValueString()
	b.Location, b.Target = locationOf(m.Location), targetOf(m.Target)
	return b
}

// --- configuration checks ---------------------------------------------------

func (r *gpuRentalResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg gpuRentalModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range gpuConfigErrors(cfg) {
		resp.Diagnostics.AddAttributeError(path.Root(e[0]), e[1], e[2])
	}
}

// gpuConfigErrors checks a configuration as written: {attribute, summary,
// detail}. Values not known yet are left alone.
func gpuConfigErrors(m gpuRentalModel) [][3]string {
	var errs [][3]string
	add := func(at, sum, det string) { errs = append(errs, [3]string{at, sum, det}) }
	single := !m.Model.IsNull()
	switch {
	case single && m.Cluster != nil:
		add("cluster", "model or cluster, not both", "Write model and use for one GPU, or a cluster block for several, not both.")
	case !single && m.Cluster == nil:
		add("model", "What to rent", "Write model and use for one GPU, or a cluster block for several.")
	case single:
		if m.Use.IsNull() {
			add("use", "use is needed", "One GPU needs use = \"machine\" or \"apps\".")
		}
	default:
		if !m.Use.IsNull() || m.Location != nil || m.Target != nil {
			add("cluster", "use, location and target go in the cluster block",
				"With a cluster, write use, location and target inside the cluster block.")
		}
		c := m.Cluster
		if c.Use.IsNull() {
			add("cluster", "use is needed", "The cluster needs use = \"machine\" or \"apps\".")
		}
		if c.Cards.IsNull() {
			add("cluster", "cards is needed", "The cluster needs cards: how many GPUs of each model, e.g. { rtx5090 = 2 }.")
		} else if !c.Cards.IsUnknown() {
			for model, v := range c.Cards.Elements() {
				n, ok := v.(types.Int64)
				if ok && known(n) && (n.ValueInt64() < 1 || n.ValueInt64() > 8) {
					add("cluster", "A count out of range", fmt.Sprintf("cards[%q] is %d: each model takes 1 to 8 GPUs (leave a model out for none).", model, n.ValueInt64()))
				}
			}
			if len(c.Cards.Elements()) == 0 {
				add("cluster", "No GPUs", "cards names no model: a cluster has at least one GPU.")
			}
		}
	}
	loc, tgt, use := m.Location, m.Target, m.Use
	if m.Cluster != nil {
		loc, tgt, use = m.Cluster.Location, m.Cluster.Target, m.Cluster.Use
	}
	if loc != nil && known(loc.Mode) {
		switch loc.Mode.ValueString() {
		case "region":
			if loc.Region.IsNull() {
				add("location", "region is needed", "location mode \"region\" needs region.")
			}
		case "host":
			if loc.Host.IsNull() {
				add("location", "host is needed", "location mode \"host\" needs host.")
			}
		}
	}
	if known(m.StartsAt) && m.EndsAt.IsNull() {
		add("ends_at", "A booking needs an end", "With starts_at (a booking), write ends_at too: whole UTC hours, at least 1 hour, at most 30 days.")
	}
	if known(m.StartsAt) && known(m.EndsAt) && m.EndsAt.ValueString() <= m.StartsAt.ValueString() {
		add("ends_at", "The end comes before the start", "ends_at must be after starts_at.")
	}
	if tgt != nil {
		if m.StartsAt.IsNull() {
			add("target", "target is for a booking", "target names what gets the GPUs at a booking's start: write starts_at too, or give the GPUs to a machine or app with its gpu attribute.")
		}
		if known(use) && use.ValueString() == "machine" && !tgt.Apps.IsNull() {
			add("target", "apps is for apps use", "A rental for a machine takes target.machine.")
		}
		if known(use) && use.ValueString() == "apps" && !tgt.Machine.IsNull() {
			add("target", "machine is for machine use", "A rental for apps takes target.apps.")
		}
	}
	return errs
}

// --- plan ------------------------------------------------------------------

// gpuFixed is the advice on a change that would need a new rental.
func gpuFixed(what, was string) string {
	keep := "keep it as it was"
	if was != "" {
		keep = fmt.Sprintf("keep it as it was (%s)", was)
	}
	return fmt.Sprintf("A rental's %s can't change in place, and Terraform never releases a rental and rents again by "+
		"itself: renting spends money. To go on with this rental, %s. To rent anew on purpose, taint it "+
		"(terraform taint or tofu taint) and apply, or remove this resource and add another; -replace alone is "+
		"refused while %s differs.", what, keep, what)
}

func locString(l *gpuLocationModel) string {
	if loc := locationOf(l); loc != nil {
		return fmt.Sprintf("%+v", *loc)
	}
	return ""
}

func tgtString(t *gpuTargetModel) string {
	if tg := targetOf(t); tg != nil {
		return fmt.Sprintf("%+v", *tg)
	}
	return ""
}

// gpuPlanErrors are the changes from state to plan the platform can't make
// in place: {attribute, summary, detail}.
func gpuPlanErrors(plan, state gpuRentalModel) [][3]string {
	var errs [][3]string
	add := func(at, sum, det string) { errs = append(errs, [3]string{at, sum, det}) }
	if (plan.Cluster == nil) != (state.Cluster == nil) {
		add("cluster", "One GPU or a cluster is fixed once rented", gpuFixed("shape (model and use, or a cluster block)", ""))
		return errs
	}
	if !plan.Model.Equal(state.Model) {
		add("model", "A rental's model is fixed", gpuFixed("model", state.Model.ValueString()))
	}
	if !plan.StartsAt.Equal(state.StartsAt) {
		add("starts_at", "A rental's start is fixed", gpuFixed("starts_at", state.StartsAt.ValueString()))
	}
	pl, sl, pt, st := plan.Location, state.Location, plan.Target, state.Target
	if plan.Cluster != nil {
		pl, sl, pt, st = plan.Cluster.Location, state.Cluster.Location, plan.Cluster.Target, state.Cluster.Target
	}
	if locString(pl) != locString(sl) {
		add("location", "A rental's location is fixed", gpuFixed("location", locString(sl)))
	}
	if tgtString(pt) != tgtString(st) {
		add("target", "A booking's target is fixed", gpuFixed("target", tgtString(st)))
	}
	if known(plan.EndsAt) && !plan.EndsAt.Equal(state.EndsAt) && !state.EndsAt.IsUnknown() {
		switch {
		case state.EndsAt.IsNull():
			add("ends_at", "This rental has no end", "It runs until it is released (destroy this resource to release it). "+
				"A rental's end can only move later; leave ends_at out.")
		case plan.EndsAt.ValueString() < state.EndsAt.ValueString():
			add("ends_at", "A rental's end can only move later",
				fmt.Sprintf("It ends at %s; ends_at may be that or later. To stop sooner, release it (destroy this resource): "+
					"charging stops then (a booking keeps its booked hours).", state.EndsAt.ValueString()))
		}
	}
	if known(plan.use()) && !plan.use().Equal(state.use()) {
		if s := state.Status.ValueString(); s == "booked" || s == "delayed" {
			add("use", "A booking's use is fixed", gpuFixed("use", state.use().ValueString())+
				" (A booking's use is changed by cancelling it and booking again.)")
		}
	}
	if plan.Cluster != nil && !plan.Cluster.Cards.IsUnknown() {
		want, have := cardCounts(plan.Cluster.Cards), cardCounts(state.Cluster.Cards)
		models := make([]string, 0, len(want))
		for m := range want {
			models = append(models, m)
		}
		sort.Strings(models)
		for _, m := range models {
			if want[m] > have[m] {
				add("cluster", "A cluster can't take more GPUs",
					fmt.Sprintf("cards[%q] is %d; the cluster has %d. A cluster only gives GPUs up (fewer of a model takes them "+
						"off in place). %s", m, want[m], have[m], gpuFixed("cards", "")))
			}
		}
	}
	return errs
}

// ModifyPlan refuses what would need a new rental, an ended rental's
// changed configuration, and warns before a destroy the platform would
// charge in full.
func (r *gpuRentalResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return // create
	}
	var state gpuRentalModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Plan.Raw.IsNull() {
		if w := chargedCancelAhead(state, gpuNow()); w != "" {
			resp.Diagnostics.AddWarning("Cancelling this booking is charged in full", w)
		}
		return
	}
	var plan gpuRentalModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.over() {
		if gpuConfigChanged(plan, state) {
			reason := state.EndedReason.ValueString()
			if reason == "" {
				reason = state.Status.ValueString()
			}
			resp.Diagnostics.AddError("This GPU rental has ended",
				fmt.Sprintf("Rental %s is over (%s). Terraform keeps it in state and never rents again by itself: renting "+
					"spends money. To rent again on purpose, taint it (terraform taint or tofu taint) and apply; with the "+
					"configuration as it was, -replace works too. Or remove this resource.", state.ID.ValueString(), reason))
		}
		return
	}
	for _, e := range gpuPlanErrors(plan, state) {
		resp.Diagnostics.AddAttributeError(path.Root(e[0]), e[1], e[2])
	}
}

// gpuConfigChanged says whether anything written differs between plan and
// state, allow_charged_cancel and the timeouts aside.
func gpuConfigChanged(plan, state gpuRentalModel) bool {
	if (plan.Cluster == nil) != (state.Cluster == nil) || !plan.Model.Equal(state.Model) ||
		!plan.StartsAt.Equal(state.StartsAt) || !plan.use().Equal(state.use()) ||
		(!plan.EndsAt.IsUnknown() && !plan.EndsAt.Equal(state.EndsAt)) {
		return true
	}
	pl, sl, pt, st := plan.Location, state.Location, plan.Target, state.Target
	if plan.Cluster != nil {
		pl, sl, pt, st = plan.Cluster.Location, state.Cluster.Location, plan.Cluster.Target, state.Cluster.Target
		if !plan.Cluster.Cards.Equal(state.Cluster.Cards) {
			return true
		}
	}
	return locString(pl) != locString(sl) || tgtString(pt) != tgtString(st)
}

// chargedCancelAhead is the warning for a destroy of a booking that starts
// within a day while allow_charged_cancel is off; "" for none.
func chargedCancelAhead(state gpuRentalModel, now time.Time) string {
	if s := state.Status.ValueString(); (s != "booked" && s != "delayed") || state.AllowChargedCancel.ValueBool() {
		return ""
	}
	start, err := time.Parse(time.RFC3339, state.StartsAt.ValueString())
	if err != nil || start.Sub(now) > 24*time.Hour {
		return ""
	}
	return fmt.Sprintf("Booking %s starts at %s, within a day, so cancelling it now is charged in full. The apply will stop "+
		"with an error and cancel nothing unless allow_charged_cancel = true is applied first.", state.ID.ValueString(), state.StartsAt.ValueString())
}

// --- read ------------------------------------------------------------------

func strOrNull(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// timeOrNull is a time the platform answers, written as a UTC RFC 3339 time
// ("2026-11-20T14:00:00Z") whatever zone it came in, so it compares with
// what the configuration writes.
func timeOrNull(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	if t, err := time.Parse(time.RFC3339, *s); err == nil {
		return types.StringValue(t.UTC().Format(time.RFC3339))
	}
	return types.StringValue(*s)
}

// readGPURental fills state from the platform's rental. The computed fields
// always; what is written (the use, the end, the cluster's counts) on a
// refresh (drift) only while the rental lives, so a change made elsewhere
// shows in the plan. An ended rental keeps what was written: it plans no
// change. After a write (drift false) only what the plan left unknown is
// filled: a value read back otherwise would fail the apply after the money
// was spent.
func readGPURental(m *gpuRentalModel, r *client.GPURental, drift bool) {
	m.ID = types.StringValue(r.ID)
	m.Name = types.StringValue(r.Name)
	m.Status = types.StringValue(r.State)
	m.Phase = types.StringValue(r.Phase)
	if r.Phase == "" {
		m.Phase = types.StringNull()
	}
	m.Booked = types.BoolValue(r.Booked)
	m.Host, m.Region = strOrNull(r.Host), strOrNull(r.Region)
	m.StartedAt, m.EndedAt, m.EndedReason = timeOrNull(r.StartedAt), timeOrNull(r.EndedAt), strOrNull(r.EndedReason)
	m.UsedBy = stringList(r.UsedBy)
	rows := make([]attr.Value, 0, len(r.Cards))
	for _, c := range r.Cards {
		rows = append(rows, types.ObjectValueMust(gpuCardAttrTypes, map[string]attr.Value{
			"id": types.StringValue(c.ID), "model": types.StringValue(c.Model), "name": types.StringValue(c.Name),
			"state": types.StringValue(c.State), "answering": types.BoolValue(c.Answering),
		}))
	}
	m.Cards = types.ListValueMust(types.ObjectType{AttrTypes: gpuCardAttrTypes}, rows)
	m.HoursThisMonth = types.Float64Value(r.HoursThisMonth)
	m.CentsThisMonth = types.Int64Null()
	if r.CentsThisMonth != nil {
		m.CentsThisMonth = types.Int64Value(*r.CentsThisMonth)
	}
	if r.Over() {
		if m.EndsAt.IsUnknown() {
			m.EndsAt = timeOrNull(r.EndsAt)
		}
		return
	}
	if !drift {
		if m.EndsAt.IsUnknown() {
			m.EndsAt = timeOrNull(r.EndsAt)
		}
		return
	}
	m.EndsAt = timeOrNull(r.EndsAt)
	if r.Use != "" {
		if m.Cluster != nil {
			m.Cluster.Use = types.StringValue(r.Use)
		} else {
			m.Use = types.StringValue(r.Use)
		}
	}
	if m.Cluster != nil && len(r.Cards) > 0 {
		m.Cluster.Cards = countsMap(liveCounts(r))
	}
}

// cardOver says whether one GPU of a rental is gone from it: the platform
// may list a GPU taken off a cluster beside the others for a while (ended,
// still coming off its machine or apps).
func cardOver(c client.GPUCard) bool { return c.State == "ended" || c.State == "cancelled" }

// liveCounts is a rental's GPUs per model, those taken off aside.
func liveCounts(r *client.GPURental) map[string]int {
	out := map[string]int{}
	for _, c := range r.Cards {
		if !cardOver(c) {
			out[c.Model]++
		}
	}
	return out
}

// settleUnread fills the computed fields a write leaves unknown when the
// rental couldn't be read back: known values, so the state is whole.
func settleUnread(m *gpuRentalModel, status string) {
	str := func(v *types.String) {
		if v.IsUnknown() {
			*v = types.StringNull()
		}
	}
	for _, v := range []*types.String{&m.ID, &m.Name, &m.Phase, &m.Host, &m.Region, &m.StartedAt, &m.EndedAt, &m.EndedReason, &m.EndsAt} {
		str(v)
	}
	if m.Status.IsUnknown() {
		m.Status = types.StringValue(status)
	}
	if m.Booked.IsUnknown() {
		m.Booked = types.BoolValue(known(m.StartsAt))
	}
	if m.UsedBy.IsUnknown() {
		m.UsedBy = stringList(nil)
	}
	if m.Cards.IsUnknown() {
		m.Cards = types.ListValueMust(types.ObjectType{AttrTypes: gpuCardAttrTypes}, []attr.Value{})
	}
	if m.HoursThisMonth.IsUnknown() {
		m.HoursThisMonth = types.Float64Value(0)
	}
	if m.CentsThisMonth.IsUnknown() {
		m.CentsThisMonth = types.Int64Null()
	}
}

func (r *gpuRentalResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state gpuRentalModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rental, err := r.data.Client.GPURental(ctx, state.ID.ValueString())
	if err != nil {
		// Never taken out of state: a rental out of view still costs money.
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			resp.Diagnostics.AddWarning("GPU renting isn't offered on this server",
				fmt.Sprintf("Rental %s couldn't be read: this LiveLLM server lists no GPUs now. It stays in state as it was.", state.ID.ValueString()))
			return
		}
		gpuDiag(&resp.Diagnostics, "Cannot read the GPU rental", err)
		return
	}
	if rental == nil {
		// Not listed: ended more than 30 days ago (or never in this
		// workspace). Kept, as ended; never rented again by a refresh.
		if !state.over() {
			resp.Diagnostics.AddWarning("GPU rental not listed",
				fmt.Sprintf("Rental %s isn't among the workspace's GPUs (they list ended rentals for 30 days). It stays in "+
					"state as ended; Terraform won't rent again unless you taint it or remove it.", state.ID.ValueString()))
			state.Status = types.StringValue("ended")
			state.UsedBy = stringList(nil)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	readGPURental(&state, rental, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// --- write -----------------------------------------------------------------

func (r *gpuRentalResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan gpuRentalModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ids, err := r.data.Client.RentGPU(ctx, gpuRentBody(plan))
	if err != nil {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) {
			resp.Diagnostics.AddError("Cannot rent the GPU",
				fmt.Sprintf("%s\n\nNo answer came back, so the GPU may have been rented anyway. Check the workspace's GPUs "+
					"(the livellm_gpus data source, or GPUs in the console) before applying again.", err))
			return
		}
		gpuDiag(&resp.Diagnostics, "Cannot rent the GPU", err)
		return
	}
	if len(ids) == 0 {
		resp.Diagnostics.AddError("Cannot rent the GPU", "The platform answered without a rental. Check the workspace's GPUs "+
			"(the livellm_gpus data source, or GPUs in the console) before applying again.")
		return
	}
	// From here on the rental exists: nothing below is an error, or the next
	// apply would release it and rent again.
	plan.ID = types.StringValue(ids[0])
	r.refresh(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// refresh reads the rental back after a write; when it can't be read, a
// warning and the fields filled with what is known.
func (r *gpuRentalResource) refresh(ctx context.Context, m *gpuRentalModel, diags *diag.Diagnostics) {
	rental, err := r.data.Client.GPURental(ctx, m.ID.ValueString())
	if err == nil && rental != nil {
		readGPURental(m, rental, false)
		return
	}
	if err == nil {
		err = fmt.Errorf("not listed yet")
	}
	diags.AddWarning("The GPU rental couldn't be read back",
		fmt.Sprintf("Rental %s: %s. The next refresh reads it.", m.ID.ValueString(), err))
	status := "active"
	if known(m.StartsAt) {
		status = "booked"
	}
	settleUnread(m, status)
}

// confirmCharged runs a write the platform may answer 428 (charged in full)
// for: again with confirm only when allow is set, else a plain error.
func confirmCharged(diags *diag.Diagnostics, summary, what string, allow bool, try func(confirm string) error, confirm string) bool {
	err := try("")
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Status == 428 {
		if !allow {
			diags.AddError("This would be charged in full",
				fmt.Sprintf("%s: %s\n\n%s starts within 24 hours, so the platform charges it in full. Nothing was cancelled. "+
					"To go ahead and pay, set allow_charged_cancel = true, apply, then do this again.", summary, apiErr.Message(), what))
			return false
		}
		err = try(confirm)
	}
	if err != nil {
		gpuDiag(diags, summary, err)
		return false
	}
	return true
}

// cardToRemove picks which GPU of a model to take off: one that isn't
// working first, else the last one listed.
func cardToRemove(cards []client.GPUCard, model string, skip map[string]bool) string {
	last := ""
	for _, c := range cards {
		if c.Model != model || skip[c.ID] || cardOver(c) {
			continue
		}
		if !c.Answering {
			return c.ID
		}
		last = c.ID
	}
	return last
}

func (r *gpuRentalResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state gpuRentalModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	plan.ID = state.ID
	ok := state.over() || r.change(ctx, plan, state, &resp.Diagnostics)
	if !ok {
		// What went through stays: state is what the platform holds now.
		state.AllowChargedCancel = plan.AllowChargedCancel
		state.Timeouts = plan.Timeouts
		if rental, err := r.data.Client.GPURental(ctx, id); err == nil && rental != nil {
			readGPURental(&state, rental, true)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	if state.over() {
		// Only allow_charged_cancel or the timeouts changed.
		state.AllowChargedCancel = plan.AllowChargedCancel
		state.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	r.refresh(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// change makes the in-place changes: GPUs taken off, a later end, the use.
func (r *gpuRentalResource) change(ctx context.Context, plan, state gpuRentalModel, diags *diag.Diagnostics) bool {
	c := r.data.Client
	id := state.ID.ValueString()
	allow := plan.AllowChargedCancel.ValueBool()
	if plan.Cluster != nil && state.Cluster != nil && !plan.Cluster.Cards.IsUnknown() {
		want, have := cardCounts(plan.Cluster.Cards), cardCounts(state.Cluster.Cards)
		var fewer []string
		for m := range have {
			if want[m] < have[m] {
				fewer = append(fewer, m)
			}
		}
		sort.Strings(fewer)
		if len(fewer) > 0 {
			rental, err := c.GPURental(ctx, id)
			if err != nil || rental == nil {
				if err == nil {
					err = fmt.Errorf("rental %s isn't listed", id)
				}
				gpuDiag(diags, "Cannot read the cluster's GPUs", err)
				return false
			}
			counts, taken := liveCounts(rental), map[string]bool{}
			for _, m := range fewer {
				for n := counts[m]; n > want[m]; n-- {
					card := cardToRemove(rental.Cards, m, taken)
					if card == "" {
						break
					}
					taken[card] = true
					what := fmt.Sprintf("The booked %s GPU %s", m, card)
					if !confirmCharged(diags, "Cannot take a GPU off the cluster", what, allow, func(confirm string) error {
						return c.RemoveGPUCard(ctx, id, card, confirm)
					}, card) {
						return false
					}
				}
			}
		}
	}
	if known(plan.EndsAt) && !plan.EndsAt.Equal(state.EndsAt) {
		if err := c.ExtendGPU(ctx, id, plan.EndsAt.ValueString()); err != nil {
			gpuDiag(diags, "Cannot extend the GPU rental", err)
			return false
		}
	}
	if use := plan.use(); known(use) && !use.Equal(state.use()) {
		switching, err := c.ChangeGPUUse(ctx, id, use.ValueString())
		if err != nil {
			gpuDiag(diags, "Cannot change the GPU's use", err)
			return false
		}
		if switching {
			wait, d := plan.Timeouts.Update(ctx, 20*time.Minute)
			diags.Append(d...)
			if err := r.waitUse(ctx, id, wait); err != nil {
				diags.AddError("The GPU didn't get ready for its new use", err.Error())
				return false
			}
		}
	}
	return true
}

// waitUse waits while a rental's GPUs get ready for a new use.
func (r *gpuRentalResource) waitUse(ctx context.Context, id string, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		rental, err := r.data.Client.GPURental(ctx, id)
		if err == nil && rental != nil {
			switch {
			case rental.Phase == "failed":
				return fmt.Errorf("rental %s: its GPU failed to change use. Look at GPUs in the console", id)
			case !strings.HasPrefix(rental.Phase, "switching-"):
				return nil
			}
		}
		t := time.NewTimer(gpuPollEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("rental %s still changing use after %s (it goes on; the next refresh reads it)", id, wait)
		case <-t.C:
		}
	}
}

// Delete releases the rental (or cancels the booking). One already over has
// nothing to release. A cancel charged in full goes ahead only with
// allow_charged_cancel.
func (r *gpuRentalResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state gpuRentalModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || state.over() {
		return
	}
	id := state.ID.ValueString()
	var charged int64
	err := func(confirm string) error {
		var e error
		charged, e = r.data.Client.ReleaseGPU(ctx, id, confirm)
		return e
	}
	var apiErr *client.APIError
	first := err("")
	switch {
	case first == nil:
	case errors.As(first, &apiErr) && apiErr.Code() == "gpu_ended":
		return // over already: nothing to release
	case errors.As(first, &apiErr) && apiErr.Code() == "gpu_unknown":
		resp.Diagnostics.AddWarning("GPU rental not found",
			fmt.Sprintf("This workspace has no rental %s, so nothing was released. If the provider's API key belongs to "+
				"another workspace than the one that rented it, the rental may still run there: check GPUs in the console.", id))
		return
	case errors.As(first, &apiErr) && apiErr.Status == 428:
		if !confirmCharged(&resp.Diagnostics, "Cannot cancel the GPU booking", fmt.Sprintf("Booking %s", id),
			state.AllowChargedCancel.ValueBool(), func(confirm string) error {
				if confirm == "" {
					return first
				}
				return err(confirm)
			}, id) {
			return
		}
	case errors.As(first, &apiErr) && apiErr.Status == 409 && apiErr.Code() == "gpu_in_use":
		resp.Diagnostics.AddError("The GPU is in use",
			fmt.Sprintf("Cannot release rental %s: %s\n\nTake it off first: gpu = \"\" (or leave the reference out) on the "+
				"machine, app or Desktop App that uses it, apply, then release it.", id, apiErr.Message()))
		return
	default:
		gpuDiag(&resp.Diagnostics, "Cannot release the GPU rental", first)
		if errors.As(first, &apiErr) && apiErr.Status == 404 {
			resp.Diagnostics.AddError("Rental kept in state",
				"To forget it here without releasing anything, run terraform state rm (or tofu state rm) on it.")
		}
		return
	}
	resp.Diagnostics.AddWarning("GPU rental released",
		fmt.Sprintf("Rental %s is released; it is charged $%d.%02d in all.", id, charged/100, charged%100))
}

// --- errors ----------------------------------------------------------------

// gpuDiag turns a GPU route's error into words: renting off (404), who may
// rent (403), the billing refusals, else the platform's words and code. A
// payment link is never printed.
func gpuDiag(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(summary, err.Error())
		return
	}
	msg, code := apiErr.Message(), apiErr.Code()
	words := msg
	if code != "" {
		words = fmt.Sprintf("%s (%s)", msg, code)
	}
	switch {
	case apiErr.Status == 404:
		diags.AddError("GPU renting isn't offered on this server",
			fmt.Sprintf("%s: this LiveLLM server doesn't rent GPUs (it answered: %s).", summary, msg))
	case apiErr.Status == 402 && code == "payg_off":
		diags.AddError("Pay as you go is off",
			fmt.Sprintf("%s: %s\n\nGPUs are pay as you go. A person turns pay as you go on on the console's Billing page "+
				"(https://cloud.live-llm.com/billing), then apply again.", summary, words))
	case (apiErr.Status == 402 || apiErr.Status == 403) && billingRefusals[code].summary != "":
		b := billingRefusals[code]
		diags.AddError(b.summary, fmt.Sprintf("%s: %s\n\n%s", summary, words, b.next))
	case apiErr.Status == 403 && code == "gpu_not_offered":
		diags.AddError("This GPU isn't offered yet",
			fmt.Sprintf("%s: %s\n\nIt has no price yet. livellm_gpu_models lists the models with their price an hour.", summary, words))
	case apiErr.Status == 403 && strings.Contains(msg, "Agents"):
		diags.AddError("An agent sign-in can't rent GPUs",
			fmt.Sprintf("%s: %s\n\nRenting needs a person, or an API key a person gave the billing permission.", summary, msg))
	case apiErr.Status == 403:
		diags.AddError("Not allowed to rent GPUs",
			fmt.Sprintf("%s: %s\n\nRenting needs the workspace's owner, or an API key a person gave the billing permission "+
				"(the workspace's API keys page). An agent sign-in can never rent.", summary, words))
	case apiErr.Status == 409 && code == "gpu_none_free":
		diags.AddError("No free GPU",
			fmt.Sprintf("%s: %s\n\nlivellm_gpu_models says what is free now and, with from and to, in each hour.", summary, words))
	case apiErr.Status == 422 && code == "gpu_clusters_off":
		diags.AddError("Clusters aren't offered here",
			fmt.Sprintf("%s: %s\n\nThis cloud rents one GPU at a time: write one GPU per livellm_gpu_rental.", summary, words))
	case apiErr.Status == 402 || apiErr.Status == 409 || apiErr.Status == 422 || apiErr.Status == 428 || apiErr.Status == 503:
		diags.AddError(summary, fmt.Sprintf("%s: %s", summary, words))
	default:
		diags.AddError(summary, err.Error())
	}
}
