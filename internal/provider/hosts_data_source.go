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

// data.livellm_hosts — the hosts and regions a resource's placement can name.
type hostsDataSource struct {
	data *providerData
}

func NewHostsDataSource() datasource.DataSource {
	return &hostsDataSource{}
}

func (d *hostsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_hosts"
}

func (d *hostsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The hosts resources can run on, with their region: the values placement_host and placement_region take.",
		Attributes: map[string]schema.Attribute{
			"hosts": schema.ListNestedAttribute{
				Computed:    true,
				Description: "One entry per host.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":     schema.StringAttribute{Computed: true, Description: "Host id, for placement_host."},
						"region": schema.StringAttribute{Computed: true, Description: "Its region, for placement_region; null if it has none."},
						"zone":   schema.StringAttribute{Computed: true, Description: "Its zone within the region; null if it has none."},
						"ready":  schema.BoolAttribute{Computed: true, Description: "Whether it is up."},
						"schedulable": schema.BoolAttribute{Computed: true, Description: "Whether it takes new resources: a host set aside " +
							"is up but takes none. A placement is accepted only on a host that is ready and schedulable."},
					},
				},
			},
		},
	}
}

func (d *hostsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

var hostAttrTypes = map[string]attr.Type{
	"id":          types.StringType,
	"region":      types.StringType,
	"zone":        types.StringType,
	"ready":       types.BoolType,
	"schedulable": types.BoolType,
}

type hostModel struct {
	ID          types.String `tfsdk:"id"`
	Region      types.String `tfsdk:"region"`
	Zone        types.String `tfsdk:"zone"`
	Ready       types.Bool   `tfsdk:"ready"`
	Schedulable types.Bool   `tfsdk:"schedulable"`
}

type hostsModel struct {
	Hosts types.List `tfsdk:"hosts"`
}

// optString is null for "" (a host without a region or zone label).
func optString(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// hostList turns the platform's host list into the hosts attribute.
func hostList(ctx context.Context, hs []client.Host) (types.List, error) {
	rows := make([]hostModel, 0, len(hs))
	for _, h := range hs {
		rows = append(rows, hostModel{
			ID:          types.StringValue(h.ID),
			Region:      optString(h.Region),
			Zone:        optString(h.Zone),
			Ready:       types.BoolValue(h.Ready),
			Schedulable: types.BoolValue(h.Schedulable == nil || *h.Schedulable),
		})
	}
	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: hostAttrTypes}, rows)
	if diags.HasError() {
		return list, fmt.Errorf("%v", diags)
	}
	return list, nil
}

func (d *hostsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	hs, err := d.data.Client.FleetHosts(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the hosts", err.Error())
		return
	}
	list, err := hostList(ctx, hs)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the hosts", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, hostsModel{Hosts: list})...)
}
