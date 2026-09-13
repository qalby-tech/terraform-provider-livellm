package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellmProvider is workspace-scoped in v1: the llc_ API key pins exactly one
// workspace (resolved once via /v1/me/tenant) and every resource lives inside
// it. Managing two workspaces = two provider aliases. See the cluster repo's
// docs/terraform-provider.md for the full design.
type livellmProvider struct {
	version string
}

// Data handed to every resource/data source after Configure.
type providerData struct {
	Client *client.Client
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &livellmProvider{version: version}
	}
}

func (p *livellmProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "livellm"
	resp.Version = p.version
}

func (p *livellmProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage LiveLLM Cloud workspace resources. The API key is " +
			"workspace-scoped: one provider block manages exactly one workspace.",
		Attributes: map[string]schema.Attribute{
			"api_key": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Workspace API key (llc_…). Falls back to the LIVELLM_API_KEY environment variable. Mint keys on your workspace's API keys page.",
			},
			"endpoint": schema.StringAttribute{
				Optional:    true,
				Description: "API endpoint. Defaults to " + client.DefaultEndpoint + ".",
			},
		},
	}
}

type providerModel struct {
	APIKey   types.String `tfsdk:"api_key"`
	Endpoint types.String `tfsdk:"endpoint"`
}

func (p *livellmProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	key := cfg.APIKey.ValueString()
	if key == "" {
		key = os.Getenv("LIVELLM_API_KEY")
	}
	if key == "" {
		resp.Diagnostics.AddError(
			"Missing API key",
			"Set the provider's api_key attribute or the LIVELLM_API_KEY environment variable. "+
				"Mint a key on your workspace's API keys page at cloud.live-llm.com/api-keys.",
		)
		return
	}

	c := client.New(cfg.Endpoint.ValueString(), key)
	// Resolve the key's workspace once — this is also the auth check, so a bad
	// key fails at plan time with a clear message instead of on first apply.
	if _, err := c.MyWorkspace(ctx); err != nil {
		resp.Diagnostics.AddError("Cannot resolve workspace", "GET /v1/me/tenant failed: "+err.Error())
		return
	}

	data := &providerData{Client: c}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *livellmProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSecretResource,
		NewStorageResource,
		NewContainerAppResource,
		NewVMResource,
		NewBrowserResource,
	}
}

func (p *livellmProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewWorkspaceDataSource,
		NewVMDataSource,
		NewVMsDataSource,
	}
}
