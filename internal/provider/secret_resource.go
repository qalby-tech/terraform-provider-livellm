package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// livellm_secret — a write-only workspace secret under a Vault-like path.
// The value never lands in Terraform state (write-only argument), mirroring
// the platform's write-only store: once set it is never read back, by anyone.
// Rotation is explicit: bump value_wo_version to re-send the value.
type secretResource struct {
	data *providerData
}

func NewSecretResource() resource.Resource {
	return &secretResource{}
}

func (r *secretResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (r *secretResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A workspace secret under a Vault-like path. The value is write-only — it never " +
			"lands in Terraform state and can never be read back from the platform. Workloads consume " +
			"it via their secret_env; rotation = bump value_wo_version.",
		Attributes: map[string]schema.Attribute{
			"path": schema.StringAttribute{
				Required:    true,
				Description: "Vault-like secret path, e.g. \"/prod/tg-bot-token\". Changing it replaces the secret.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"value_wo": schema.StringAttribute{
				Required:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The secret value (write-only: not stored in state). Re-sent only when value_wo_version changes.",
			},
			"value_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for value_wo — bump it to write a new version of the value.",
				PlanModifiers: []planmodifier.Int64{
					// Version changes rewrite the value in place (a new
					// platform-side version), never replace the resource.
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"current_version": schema.Int64Attribute{
				Computed:    true,
				Description: "The platform-side version currently serving this path (every write increments it).",
			},
		},
	}
}

func (r *secretResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type secretModel struct {
	Path           types.String `tfsdk:"path"`
	ValueWO        types.String `tfsdk:"value_wo"`
	ValueWOVersion types.Int64  `tfsdk:"value_wo_version"`
	CurrentVersion types.Int64  `tfsdk:"current_version"`
}

// write sends the value from CONFIG (write-only args are absent from plan and
// state) and refreshes current_version from the list endpoint.
func (r *secretResource) write(ctx context.Context, cfg secretModel, plan *secretModel, diags *diag.Diagnostics) {
	value := cfg.ValueWO.ValueString()
	if value == "" {
		diags.AddError("Empty secret value", "value_wo must be a non-empty string.")
		return
	}
	if err := r.data.Client.SetSecret(ctx, plan.Path.ValueString(), value, "terraform"); err != nil {
		apiDiag(diags, "Cannot write secret", err)
		return
	}
	plan.ValueWO = types.StringNull() // write-only: never persisted
	metas, err := r.data.Client.ListSecrets(ctx)
	if err == nil {
		for _, m := range metas {
			if m.Path == plan.Path.ValueString() {
				plan.CurrentVersion = types.Int64Value(int64(m.CurrentVersion))
				return
			}
		}
	}
	plan.CurrentVersion = types.Int64Value(1)
}

func (r *secretResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, cfg, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *secretResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	metas, err := r.data.Client.ListSecrets(ctx)
	if err != nil {
		apiDiag(&resp.Diagnostics, "Cannot list secrets", err)
		return
	}
	for _, m := range metas {
		if m.Path == state.Path.ValueString() {
			state.CurrentVersion = types.Int64Value(int64(m.CurrentVersion))
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx) // deleted out from under us
}

func (r *secretResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state secretModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.ValueWOVersion.Equal(state.ValueWOVersion) {
		r.write(ctx, cfg, &plan, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	} else {
		plan.CurrentVersion = state.CurrentVersion
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *secretResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteSecret(ctx, state.Path.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete secret", err)
	}
}

func (r *secretResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// terraform import livellm_secret.x /prod/token — the value cannot be
	// imported (write-only store); version trigger starts at 1.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("path"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("value_wo_version"), int64(1))...)
}
