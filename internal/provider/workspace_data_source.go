package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// data.livellm_workspace — the workspace the provider's key is scoped to.
// No arguments: the key already pins it.
type workspaceDataSource struct {
	data *providerData
}

func NewWorkspaceDataSource() datasource.DataSource {
	return &workspaceDataSource{}
}

func (d *workspaceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

func (d *workspaceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The workspace this provider's API key is scoped to.",
		Attributes: map[string]schema.Attribute{
			"name":  schema.StringAttribute{Computed: true, Description: "Workspace (tenant) name."},
			"plan":  schema.StringAttribute{Computed: true, Description: "Plan the workspace runs on."},
			"owner": schema.StringAttribute{Computed: true, Description: "Owning user id."},
		},
	}
}

func (d *workspaceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	d.data = data
}

type workspaceModel struct {
	Name  types.String `tfsdk:"name"`
	Plan  types.String `tfsdk:"plan"`
	Owner types.String `tfsdk:"owner"`
}

func (d *workspaceDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ws, err := d.data.Client.MyWorkspace(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read workspace", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, workspaceModel{
		Name:  types.StringValue(ws.Name),
		Plan:  types.StringValue(ws.Plan),
		Owner: types.StringValue(ws.Owner),
	})...)
}
