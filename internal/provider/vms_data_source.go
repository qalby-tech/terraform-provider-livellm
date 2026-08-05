package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// data.livellm_vms — every VM in the workspace with its live state, for
// iterating in outputs/for_each without hardcoding ids.
type vmsDataSource struct {
	data *providerData
}

func NewVMsDataSource() datasource.DataSource {
	return &vmsDataSource{}
}

func (d *vmsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vms"
}

func (d *vmsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Every VM in the workspace with its live state. For a single VM's full endpoint list, use data.livellm_vm.",
		Attributes: map[string]schema.Attribute{
			"vms": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":    schema.StringAttribute{Computed: true, Description: "Workload id."},
						"type":  schema.StringAttribute{Computed: true, Description: "vm-ubuntu, vm-ubuntu-desktop or vm-windows."},
						"ready": schema.BoolAttribute{Computed: true, Description: "Whether the VM is up."},
						"phase": schema.StringAttribute{Computed: true, Description: "Lifecycle phase."},
						"ssh":   schema.StringAttribute{Computed: true, Description: "host:port for SSH. Empty until assigned."},
						"url":   schema.StringAttribute{Computed: true, Description: "First exposed HTTP port's URL, if any."},
						"endpoints": schema.ListNestedAttribute{
							Computed:    true,
							Description: "Every exposed port (url for HTTP, addr for raw TCP/UDP).",
							NestedObject: schema.NestedAttributeObject{
								Attributes: map[string]schema.Attribute{
									"name": schema.StringAttribute{Computed: true},
									"url":  schema.StringAttribute{Computed: true},
									"addr": schema.StringAttribute{Computed: true},
									"tcp":  schema.BoolAttribute{Computed: true},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *vmsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

var vmObjAttrTypes = map[string]attr.Type{
	"id":        types.StringType,
	"type":      types.StringType,
	"ready":     types.BoolType,
	"phase":     types.StringType,
	"ssh":       types.StringType,
	"url":       types.StringType,
	"endpoints": types.ListType{ElemType: types.ObjectType{AttrTypes: endpointAttrTypes}},
}

type vmsModel struct {
	VMs types.List `tfsdk:"vms"`
}

// vmModelFrom converts one status row into the shared vm object model.
// Used by both data sources so the shapes can never drift apart.
func vmModelFrom(ctx context.Context, w client.WorkloadStatus) (vmModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	eps := make([]endpointModel, 0, len(w.Endpoints))
	url := ""
	for _, e := range w.Endpoints {
		if url == "" && e.URL != "" {
			url = e.URL
		}
		eps = append(eps, endpointModel{
			Name: types.StringValue(e.Name),
			URL:  types.StringValue(e.URL),
			Addr: types.StringValue(e.Addr),
			TCP:  types.BoolValue(e.TCP),
		})
	}
	epList, d := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: endpointAttrTypes}, eps)
	diags.Append(d...)
	return vmModel{
		ID:        types.StringValue(w.ID),
		Type:      types.StringValue(w.Type),
		Ready:     types.BoolValue(w.Ready),
		Phase:     types.StringValue(w.Phase),
		SSH:       types.StringValue(w.SSH),
		URL:       types.StringValue(url),
		Endpoints: epList,
	}, diags
}

func (d *vmsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	st, err := d.data.Client.Status(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read workspace status", err.Error())
		return
	}
	vms := make([]vmModel, 0)
	for _, w := range st.Workloads {
		if !strings.HasPrefix(w.Type, "vm-") {
			continue
		}
		m, diags := vmModelFrom(ctx, w)
		resp.Diagnostics.Append(diags.Errors()...)
		if resp.Diagnostics.HasError() {
			return
		}
		vms = append(vms, m)
	}
	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: vmObjAttrTypes}, vms)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, vmsModel{VMs: list})...)
}
