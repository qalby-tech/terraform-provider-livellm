package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_browser — a headless Chromium browser, optionally driven by a
// built-in AI agent (give it goals from the console or the task API).
type browserResource struct {
	data *providerData
}

func NewBrowserResource() resource.Resource {
	return &browserResource{}
}

func (r *browserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_browser"
}

func (r *browserResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A headless Chromium browser. Add the ai_agent block and it becomes an autonomous " +
			"browser agent — hand it goals from the console or the task API and review its trajectory step by step.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the browser.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU request, e.g. \"1\".",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory request, e.g. \"2Gi\".",
			},
			"ready":    schema.BoolAttribute{Computed: true, Description: "Whether the browser is up."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"ai_agent": schema.SingleNestedBlock{
				Description: "The browser's built-in AI driver. The engine and vision handling are managed by the platform.",
				Attributes: map[string]schema.Attribute{
					"provider":     schema.StringAttribute{Optional: true, Description: "A connected AI provider id. Defaults to any connected provider."},
					"model":        schema.StringAttribute{Optional: true, Description: "Model id; defaults to the provider's recommendation."},
					"instructions": schema.StringAttribute{Optional: true, Description: "Standing guidance for the agent."},
				},
			},
		},
	}
}

func (r *browserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type browserAgentModel struct {
	Provider     types.String `tfsdk:"provider"`
	Model        types.String `tfsdk:"model"`
	Instructions types.String `tfsdk:"instructions"`
}

type browserModel struct {
	Name     types.String   `tfsdk:"name"`
	CPU      types.String   `tfsdk:"cpu"`
	Memory   types.String   `tfsdk:"memory"`
	AIAgent  types.Object   `tfsdk:"ai_agent"`
	Ready    types.Bool     `tfsdk:"ready"`
	Timeouts timeouts.Value `tfsdk:"timeouts"`
}

func browserSpec(ctx context.Context, m browserModel) map[string]any {
	spec := map[string]any{}
	if v := m.CPU.ValueString(); v != "" {
		spec["cpu"] = v
	}
	if v := m.Memory.ValueString(); v != "" {
		spec["memory"] = v
	}
	if !m.AIAgent.IsNull() {
		var a browserAgentModel
		m.AIAgent.As(ctx, &a, basetypes.ObjectAsOptions{})
		agent := map[string]any{"enabled": true}
		if v := a.Provider.ValueString(); v != "" {
			agent["provider"] = v
		}
		if v := a.Model.ValueString(); v != "" {
			agent["model"] = v
		}
		if v := a.Instructions.ValueString(); v != "" {
			agent["instructions"] = v
		}
		spec["aiAgent"] = agent
	}
	return spec
}

func (r *browserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan browserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := browserSpec(ctx, plan)
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "browser", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create browser", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("Browser did not become ready", err.Error())
	}
	st, _ := statusOf(ctx, r.data.Client, plan.Name.ValueString())
	plan.Ready = types.BoolValue(st != nil && st.Ready)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state browserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ws, err := r.data.Client.Workloads(ctx)
	if err != nil {
		apiDiag(&resp.Diagnostics, "Cannot read workspace", err)
		return
	}
	w := findWorkload(ws, state.Name.ValueString())
	if w == nil || w.Type != "browser" {
		resp.State.RemoveResource(ctx)
		return
	}
	st, _ := statusOf(ctx, r.data.Client, state.Name.ValueString())
	state.Ready = types.BoolValue(st != nil && st.Ready)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *browserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan browserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w := client.Workload{
		ID:      plan.Name.ValueString(),
		Type:    "browser",
		Browser: browserSpec(ctx, plan),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update browser", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, false); err != nil {
		resp.Diagnostics.AddError("Browser did not become ready after update", err.Error())
	}
	st, _ := statusOf(ctx, r.data.Client, w.ID)
	plan.Ready = types.BoolValue(st != nil && st.Ready)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state browserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete browser", err)
		return
	}
	deleteTimeout, d := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if err := waitGone(waitCtx, r.data.Client, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddWarning("Deletion still in progress", err.Error())
	}
}

func (r *browserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
