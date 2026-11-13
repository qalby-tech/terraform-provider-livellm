package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// data.livellm_gpu_models — the GPU models this workspace can rent, with
// their price an hour and what is free. Keys and agents may read it.
type gpuModelsDataSource struct {
	data *providerData
}

func NewGPUModelsDataSource() datasource.DataSource { return &gpuModelsDataSource{} }

func (d *gpuModelsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gpu_models"
}

var gpuModelAttrTypes = map[string]attr.Type{
	"model":              types.StringType,
	"name":               types.StringType,
	"cents_hour":         types.Int64Type,
	"total":              types.Int64Type,
	"free_now_machine":   types.Int64Type,
	"free_now_apps":      types.Int64Type,
	"free_until_machine": types.StringType,
	"free_until_apps":    types.StringType,
	"slots":              types.ListType{ElemType: types.Int64Type},
}

func (d *gpuModelsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	hour := []validator.String{stringvalidator.RegexMatches(gpuHourRe, "must be a whole UTC hour, e.g. 2026-11-20T10:00:00Z")}
	c := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	n := func(desc string) schema.Int64Attribute {
		return schema.Int64Attribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "The GPU models this workspace can rent: the price an hour, how many there are, how many are free " +
			"now and, with from and to, in each hour.",
		Attributes: map[string]schema.Attribute{
			"use": schema.StringAttribute{
				Optional:    true,
				Description: "Which use slots counts free GPUs for: \"apps\" (the default) or \"machine\".",
				Validators:  []validator.String{stringvalidator.OneOf("machine", "apps")},
			},
			"from": schema.StringAttribute{Optional: true, Validators: hour,
				Description: "With to: the first hour slots counts (a whole UTC hour; at most 14 days before to)."},
			"to": schema.StringAttribute{Optional: true, Validators: hour, Description: "With from: the hour slots ends at."},
			"models": schema.ListNestedAttribute{
				Computed:    true,
				Description: "One entry per model.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"model":              c("What livellm_gpu_rental's model takes (\"rtx5090\")."),
					"name":               c("Its name (\"RTX 5090\")."),
					"cents_hour":         n("Its price an hour in US cents; null while it has none (it can't be rented then)."),
					"total":              n("How many there are."),
					"free_now_machine":   n("Free now for a machine."),
					"free_now_apps":      n("Free now for apps."),
					"free_until_machine": c("How long a rental for a machine starting now could last; null for no limit."),
					"free_until_apps":    c("How long a rental for apps starting now could last; null for no limit."),
					"slots": schema.ListAttribute{Computed: true, ElementType: types.Int64Type,
						Description: "With from and to: free GPUs in each hour, for use."},
				}},
			},
		},
	}
}

func (d *gpuModelsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataOf(req.ProviderData, &resp.Diagnostics)
}

// providerDataOf is the provider's data handed to a data source.
func providerDataOf(pd any, diags interface{ AddError(string, string) }) *providerData {
	if pd == nil {
		return nil
	}
	data, ok := pd.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("got %T", pd))
		return nil
	}
	return data
}

type gpuModelsModel struct {
	Use    types.String `tfsdk:"use"`
	From   types.String `tfsdk:"from"`
	To     types.String `tfsdk:"to"`
	Models types.List   `tfsdk:"models"`
}

func gpuModelList(ms []client.GPUModel) types.List {
	rows := make([]attr.Value, 0, len(ms))
	for _, m := range ms {
		cents := types.Int64Null()
		if m.CentsHour != nil {
			cents = types.Int64Value(int64(*m.CentsHour))
		}
		slots := make([]attr.Value, 0, len(m.Slots))
		for _, s := range m.Slots {
			slots = append(slots, types.Int64Value(int64(s)))
		}
		rows = append(rows, types.ObjectValueMust(gpuModelAttrTypes, map[string]attr.Value{
			"model":              types.StringValue(m.Model),
			"name":               types.StringValue(m.Name),
			"cents_hour":         cents,
			"total":              types.Int64Value(int64(m.Total)),
			"free_now_machine":   types.Int64Value(int64(m.FreeNow["machine"])),
			"free_now_apps":      types.Int64Value(int64(m.FreeNow["apps"])),
			"free_until_machine": timeOrNull(m.FreeUntil["machine"]),
			"free_until_apps":    timeOrNull(m.FreeUntil["apps"]),
			"slots":              types.ListValueMust(types.Int64Type, slots),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: gpuModelAttrTypes}, rows)
}

func (d *gpuModelsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m gpuModelsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.From.IsNull() != m.To.IsNull() {
		resp.Diagnostics.AddError("from and to go together", "Write both from and to (whole UTC hours, at most 14 days apart), or neither.")
		return
	}
	ms, err := d.data.Client.GPUModels(ctx, m.Use.ValueString(), m.From.ValueString(), m.To.ValueString())
	if err != nil {
		gpuDiag(&resp.Diagnostics, "Cannot read the GPU models", err)
		return
	}
	m.Models = gpuModelList(ms)
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// data.livellm_gpus — the workspace's GPUs: rentals booked, waiting and
// running, those that ended in the last 30 days, and each GPU an
// administrator set for it.
type gpusDataSource struct {
	data *providerData
}

func NewGPUsDataSource() datasource.DataSource { return &gpusDataSource{} }

func (d *gpusDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gpus"
}

var gpuListAttrTypes = map[string]attr.Type{
	"id":               types.StringType,
	"name":             types.StringType,
	"model":            types.StringType,
	"use":              types.StringType,
	"status":           types.StringType,
	"phase":            types.StringType,
	"starts_at":        types.StringType,
	"ends_at":          types.StringType,
	"ended_reason":     types.StringType,
	"booked":           types.BoolType,
	"no_end":           types.BoolType,
	"host":             types.StringType,
	"region":           types.StringType,
	"used_by":          types.ListType{ElemType: types.StringType},
	"cards":            types.ListType{ElemType: types.ObjectType{AttrTypes: gpuCardAttrTypes}},
	"hours_this_month": types.Float64Type,
	"cents_this_month": types.Int64Type,
}

func (d *gpusDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	c := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "The workspace's GPUs: rentals booked, waiting and running, those that ended in the last 30 days, " +
			"and each GPU an administrator set for the workspace.",
		Attributes: map[string]schema.Attribute{
			"rentals": schema.ListNestedAttribute{
				Computed:    true,
				Description: "One entry per rental (a cluster of one or more GPUs).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":           c("What a machine's, an app's or a Desktop App's gpu takes."),
					"name":         c("\"RTX 5090\", \"2 × RTX 5090\"."),
					"model":        c("Its (first) model."),
					"use":          c("machine or apps."),
					"status":       c("booked, delayed, active, ended or cancelled."),
					"phase":        c("ready, waiting, switching-to-machine, switching-to-apps or failed."),
					"starts_at":    c("Its start."),
					"ends_at":      c("Its end; null for none."),
					"ended_reason": c("Why it ended."),
					"booked":       schema.BoolAttribute{Computed: true, Description: "Booked for later."},
					"no_end":       schema.BoolAttribute{Computed: true, Description: "A GPU an administrator set for the workspace, with no end."},
					"host":         c("The server its GPUs are on."),
					"region":       c("That server's region."),
					"used_by": schema.ListAttribute{Computed: true, ElementType: types.StringType,
						Description: "The resources using it now."},
					"cards": schema.ListNestedAttribute{Computed: true, Description: "Its GPUs.",
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"id": c("The GPU's id."), "model": c("Its model."), "name": c("Its model's name."), "state": c("Its state."),
							"answering": schema.BoolAttribute{Computed: true, Description: "False while it isn't working."},
						}}},
					"hours_this_month": schema.Float64Attribute{Computed: true, Description: "Its hours this month."},
					"cents_this_month": schema.Int64Attribute{Computed: true, Description: "Its charge this month in US cents; null where it isn't charged."},
				}},
			},
		},
	}
}

func (d *gpusDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataOf(req.ProviderData, &resp.Diagnostics)
}

type gpusModel struct {
	Rentals types.List `tfsdk:"rentals"`
}

func gpuRentalList(rs []client.GPURental) types.List {
	rows := make([]attr.Value, 0, len(rs))
	for i := range rs {
		r := &rs[i]
		var m gpuRentalModel
		readGPURental(&m, r, false)
		starts := r.StartsAt
		rows = append(rows, types.ObjectValueMust(gpuListAttrTypes, map[string]attr.Value{
			"id": m.ID, "name": m.Name, "model": types.StringValue(r.Model), "use": types.StringValue(r.Use),
			"status": m.Status, "phase": m.Phase, "starts_at": timeOrNull(&starts), "ends_at": timeOrNull(r.EndsAt),
			"ended_reason": m.EndedReason, "booked": m.Booked, "no_end": types.BoolValue(r.NoEnd),
			"host": m.Host, "region": m.Region, "used_by": m.UsedBy, "cards": m.Cards,
			"hours_this_month": m.HoursThisMonth, "cents_this_month": m.CentsThisMonth,
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: gpuListAttrTypes}, rows)
}

func (d *gpusDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	rs, err := d.data.Client.GPURentals(ctx)
	if err != nil {
		gpuDiag(&resp.Diagnostics, "Cannot read the workspace's GPUs", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, gpusModel{Rentals: gpuRentalList(rs)})...)
}
