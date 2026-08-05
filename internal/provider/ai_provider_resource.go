package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// livellm_ai_provider — connects an AI provider to the workspace. The key is
// write-only on the platform AND in Terraform: it is stored for the agents'
// use and can never be read back by anyone.
type aiProviderResource struct {
	data *providerData
}

func NewAIProviderResource() resource.Resource {
	return &aiProviderResource{}
}

func (r *aiProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_provider"
}

func (r *aiProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Connects an AI provider to the workspace — the credential all agents (daemons, " +
			"masters, browser agents) use. Keys are write-only: stored for the agents, never returned. " +
			"For a Claude Pro/Max subscription use provider id claude-subscription with the token from " +
			"`claude setup-token`.",
		Attributes: map[string]schema.Attribute{
			"provider_id": schema.StringAttribute{
				Required:    true,
				Description: "A catalog id, e.g. anthropic, openai, zai-coding-plan, claude-subscription. Changing it replaces the connection.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"api_key_wo": schema.StringAttribute{
				Required:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The provider API key (or subscription token). Write-only: never stored in state. Re-sent when api_key_wo_version changes.",
			},
			"api_key_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for api_key_wo.",
			},
		},
	}
}

func (r *aiProviderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type aiProviderModel struct {
	ProviderID      types.String `tfsdk:"provider_id"`
	APIKeyWO        types.String `tfsdk:"api_key_wo"`
	APIKeyWOVersion types.Int64  `tfsdk:"api_key_wo_version"`
}

func (r *aiProviderResource) connect(ctx context.Context, cfg aiProviderModel, plan *aiProviderModel, resp *resource.CreateResponse) bool {
	key := cfg.APIKeyWO.ValueString()
	if key == "" {
		resp.Diagnostics.AddError("Empty API key", "api_key_wo must be a non-empty string.")
		return false
	}
	if err := r.data.Client.ConnectProvider(ctx, plan.ProviderID.ValueString(), key); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot connect provider", err)
		return false
	}
	plan.APIKeyWO = types.StringNull()
	return true
}

func (r *aiProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg aiProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.connect(ctx, cfg, &plan, resp) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *aiProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state aiProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	connected, err := r.data.Client.ConnectedProviders(ctx)
	if err != nil {
		apiDiag(&resp.Diagnostics, "Cannot list providers", err)
		return
	}
	for _, id := range connected {
		if id == state.ProviderID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *aiProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state aiProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.APIKeyWOVersion.Equal(state.APIKeyWOVersion) {
		key := cfg.APIKeyWO.ValueString()
		if key == "" {
			resp.Diagnostics.AddError("Empty API key", "api_key_wo must be a non-empty string.")
			return
		}
		if err := r.data.Client.ConnectProvider(ctx, plan.ProviderID.ValueString(), key); err != nil {
			apiDiag(&resp.Diagnostics, "Cannot rotate provider key", err)
			return
		}
	}
	plan.APIKeyWO = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *aiProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state aiProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DisconnectProvider(ctx, state.ProviderID.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot disconnect provider", err)
	}
}

func (r *aiProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("provider_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("api_key_wo_version"), int64(1))...)
}
