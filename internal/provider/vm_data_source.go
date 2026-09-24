package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// data.livellm_vm — one VM's live state: readiness, its SSH address and the
// real URL of every exposed port, straight from the platform. This is the
// supported way to reference a VM's addresses in outputs and other resources
// — never string-build hostnames from naming conventions.
type vmDataSource struct {
	data *providerData
}

func NewVMDataSource() datasource.DataSource {
	return &vmDataSource{}
}

func (d *vmDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

var endpointAttrTypes = map[string]attr.Type{
	"name": types.StringType,
	"url":  types.StringType,
	"addr": types.StringType,
	"tcp":  types.BoolType,
	"udp":  types.BoolType,
}

func (d *vmDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Live state of one VM in the workspace: readiness, SSH address and the URL of every exposed port.",
		Attributes: map[string]schema.Attribute{
			"id":    schema.StringAttribute{Required: true, Description: "The VM's workload id."},
			"type":  schema.StringAttribute{Computed: true, Description: "Workload type (vm-ubuntu, vm-ubuntu-desktop, vm-windows)."},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the VM is up and reachable."},
			"phase": schema.StringAttribute{Computed: true, Description: "Lifecycle phase (Pending, Running, …)."},
			"ssh":   schema.StringAttribute{Computed: true, Description: "host:port to SSH into the VM. Empty until assigned."},
			"url":   schema.StringAttribute{Computed: true, Description: "The first exposed HTTP port's public HTTPS URL. Empty when no HTTP port is exposed."},
			"endpoints": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every exposed port. HTTP ports carry url; raw TCP/UDP ports carry addr (host:port) instead.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Computed: true, Description: "Port name."},
						"url":  schema.StringAttribute{Computed: true, Description: "Public HTTPS URL (HTTP ports)."},
						"addr": schema.StringAttribute{Computed: true, Description: "host:port (raw TCP/UDP ports)."},
						"tcp":  schema.BoolAttribute{Computed: true, Description: "True for a raw TCP port (addr is host:port)."},
						"udp":  schema.BoolAttribute{Computed: true, Description: "True for a raw UDP port (addr is host:port)."},
					},
				},
			},
		},
	}
}

func (d *vmDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

type vmModel struct {
	ID        types.String `tfsdk:"id"`
	Type      types.String `tfsdk:"type"`
	Ready     types.Bool   `tfsdk:"ready"`
	Phase     types.String `tfsdk:"phase"`
	SSH       types.String `tfsdk:"ssh"`
	URL       types.String `tfsdk:"url"`
	Endpoints types.List   `tfsdk:"endpoints"`
}

type endpointModel struct {
	Name types.String `tfsdk:"name"`
	URL  types.String `tfsdk:"url"`
	Addr types.String `tfsdk:"addr"`
	TCP  types.Bool   `tfsdk:"tcp"`
	UDP  types.Bool   `tfsdk:"udp"`
}

func (d *vmDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vmModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	st, err := d.data.Client.Status(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read workspace status", err.Error())
		return
	}
	id := cfg.ID.ValueString()
	for _, w := range st.Workloads {
		if w.ID != id {
			continue
		}
		if !strings.HasPrefix(w.Type, "vm-") {
			resp.Diagnostics.AddError(
				"Not a VM",
				fmt.Sprintf("workload %q is type %q — data.livellm_vm reads VMs only", id, w.Type),
			)
			return
		}
		state, diags := vmModelFrom(ctx, w)
		resp.Diagnostics.Append(diags.Errors()...)
		if resp.Diagnostics.HasError() {
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
		return
	}
	resp.Diagnostics.AddError(
		"VM not found",
		fmt.Sprintf("no workload %q in this workspace — data.livellm_vms lists what exists", id),
	)
}
