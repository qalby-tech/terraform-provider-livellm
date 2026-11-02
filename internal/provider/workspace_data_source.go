package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
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
			"owner": schema.StringAttribute{Computed: true, Description: "Id of the person who created the workspace."},
			"private_network": schema.SingleNestedAttribute{
				Computed: true,
				Description: "The private network the workspace's machines with a private address share: its range " +
					"and its gateway. The range's first ten addresses and its last are the network's; machines take " +
					"the others. Null where private addresses aren't offered.",
				Attributes: map[string]schema.Attribute{
					"range":   schema.StringAttribute{Computed: true, Description: "The range, such as 172.16.0.0/16."},
					"gateway": schema.StringAttribute{Computed: true, Description: "The network's gateway, such as 172.16.0.1."},
				},
			},
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
	Name           types.String `tfsdk:"name"`
	Plan           types.String `tfsdk:"plan"`
	Owner          types.String `tfsdk:"owner"`
	PrivateNetwork types.Object `tfsdk:"private_network"`
}

var privateNetworkAttrTypes = map[string]attr.Type{"range": types.StringType, "gateway": types.StringType}

// privateNetworkObject is the workspace's private network, or null where the
// platform offers none.
func privateNetworkObject(pn *client.PrivateNetwork) types.Object {
	if pn == nil || pn.Range == "" {
		return types.ObjectNull(privateNetworkAttrTypes)
	}
	return types.ObjectValueMust(privateNetworkAttrTypes, map[string]attr.Value{
		"range": types.StringValue(pn.Range), "gateway": types.StringValue(pn.Gateway),
	})
}

func (d *workspaceDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ws, err := d.data.Client.MyWorkspace(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read workspace", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, workspaceModel{
		Name:           types.StringValue(ws.Name),
		Plan:           types.StringValue(ws.Plan),
		Owner:          types.StringValue(ws.Owner),
		PrivateNetwork: privateNetworkObject(ws.PrivateNetwork),
	})...)
}
