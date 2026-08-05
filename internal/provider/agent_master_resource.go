package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// livellm_agent_master — the workspace's single orchestrator agent. It plans
// across workloads, drives every daemon-enabled resource, and can provision
// through the same API Terraform uses. One per workspace.
type agentMasterResource struct {
	data *providerData
}

func NewAgentMasterResource() resource.Resource {
	return &agentMasterResource{}
}

func (r *agentMasterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent_master"
}

func (r *agentMasterResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The workspace's AI Master — a single orchestrator agent that plans across your " +
			"workloads and drives every agent-enabled resource. One per workspace; the engine is selected " +
			"automatically from the provider.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("master-1"),
				Description: "Master id. Changing it replaces the master.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"display_name": schema.StringAttribute{
				Optional:    true,
				Description: "Display name shown in the console.",
			},
			"provider_id": schema.StringAttribute{
				Required:    true,
				Description: "A connected AI provider id (Integrations page).",
			},
			"model": schema.StringAttribute{
				Optional:    true,
				Description: "Model id; defaults to the provider's recommendation.",
			},
			"instructions": schema.StringAttribute{
				Optional:    true,
				Description: "Global standing guidance for the master.",
			},
			"autonomous": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Let the master act without per-plan confirmation.",
			},
			"auto_provision": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Allow the master to provision new resources on its own.",
			},
		},
	}
}

func (r *agentMasterResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type agentMasterModel struct {
	ID            types.String `tfsdk:"id"`
	DisplayName   types.String `tfsdk:"display_name"`
	ProviderID    types.String `tfsdk:"provider_id"`
	Model         types.String `tfsdk:"model"`
	Instructions  types.String `tfsdk:"instructions"`
	Autonomous    types.Bool   `tfsdk:"autonomous"`
	AutoProvision types.Bool   `tfsdk:"auto_provision"`
}

func (m agentMasterModel) spec(enabled bool) map[string]any {
	spec := map[string]any{
		"id":      m.ID.ValueString(),
		"enabled": enabled,
	}
	if v := m.DisplayName.ValueString(); v != "" {
		spec["name"] = v
	}
	if v := m.ProviderID.ValueString(); v != "" {
		spec["provider"] = v
	}
	if v := m.Model.ValueString(); v != "" {
		spec["model"] = v
	}
	if v := m.Instructions.ValueString(); v != "" {
		spec["instructions"] = v
	}
	if m.Autonomous.ValueBool() {
		spec["autonomous"] = true
	}
	if m.AutoProvision.ValueBool() {
		spec["autoProvision"] = true
	}
	return spec
}

func (r *agentMasterResource) apply(ctx context.Context, plan agentMasterModel, enabled bool) error {
	return r.data.Client.SetMaster(ctx, plan.spec(enabled))
}

func (r *agentMasterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentMasterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, true); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create AI master", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *agentMasterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentMasterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.data.Client.Master(ctx)
	if err != nil {
		apiDiag(&resp.Diagnostics, "Cannot read AI master", err)
		return
	}
	enabled, _ := m["enabled"].(bool)
	id, _ := m["id"].(string)
	if m == nil || !enabled || id != state.ID.ValueString() {
		resp.State.RemoveResource(ctx)
		return
	}
	if v, ok := m["name"].(string); ok && v != "" {
		state.DisplayName = types.StringValue(v)
	}
	if v, ok := m["provider"].(string); ok && v != "" {
		state.ProviderID = types.StringValue(v)
	}
	if v, ok := m["model"].(string); ok && v != "" {
		state.Model = types.StringValue(v)
	}
	if v, ok := m["instructions"].(string); ok && v != "" {
		state.Instructions = types.StringValue(v)
	}
	v, _ := m["autonomous"].(bool)
	state.Autonomous = types.BoolValue(v)
	v, _ = m["autoProvision"].(bool)
	state.AutoProvision = types.BoolValue(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *agentMasterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan agentMasterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, plan, true); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update AI master", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *agentMasterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentMasterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Disable rather than null the block: the platform tears the master pod
	// down when enabled=false, and the id stays free to recreate.
	if err := r.apply(ctx, state, false); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot disable AI master", err)
	}
}

func (r *agentMasterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
