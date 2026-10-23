package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// data.livellm_machine_devices — the cluster's devices a machine of the
// workspace may be given (a GPU, a USB controller): what livellm_vm's devices
// names, how many the cluster has and how many are free. No arguments: the key
// already pins the workspace.
type machineDevicesDataSource struct {
	data *providerData
}

func NewMachineDevicesDataSource() datasource.DataSource {
	return &machineDevicesDataSource{}
}

func (d *machineDevicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_devices"
}

func (d *machineDevicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The cluster's devices a machine of this workspace may be given (a GPU, a USB controller), " +
			"by the id livellm_vm's devices takes, with how many the cluster has and how many are free.",
		Attributes: map[string]schema.Attribute{
			"devices": schema.ListNestedAttribute{
				Computed:    true,
				Description: "One entry per device; empty where the cluster offers none to this workspace.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":    schema.StringAttribute{Computed: true, Description: "What livellm_vm's devices names (\"rtx5090\")."},
						"label": schema.StringAttribute{Computed: true, Description: "The device's name (\"NVIDIA RTX 5090\")."},
						"kind":  schema.StringAttribute{Computed: true, Description: "gpu or usb."},
						"total": schema.Int64Attribute{Computed: true, Description: "How many the cluster has."},
						"free": schema.Int64Attribute{Computed: true, Description: "How many no machine has. A device is one " +
							"machine's at a time: giving a machine one that isn't free is refused."},
						"machines": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "This workspace's machines that have it.",
						},
					},
				},
			},
		},
	}
}

func (d *machineDevicesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

var machineDeviceAttrTypes = map[string]attr.Type{
	"id":       types.StringType,
	"label":    types.StringType,
	"kind":     types.StringType,
	"total":    types.Int64Type,
	"free":     types.Int64Type,
	"machines": types.ListType{ElemType: types.StringType},
}

type machineDevicesModel struct {
	Devices types.List `tfsdk:"devices"`
}

// machineDeviceList turns the platform's devices into the devices attribute:
// an empty list, never null, where it lists none.
func machineDeviceList(ds []client.MachineDevice) types.List {
	rows := make([]attr.Value, 0, len(ds))
	for _, d := range ds {
		rows = append(rows, types.ObjectValueMust(machineDeviceAttrTypes, map[string]attr.Value{
			"id":       types.StringValue(d.ID),
			"label":    types.StringValue(d.Label),
			"kind":     types.StringValue(d.Kind),
			"total":    types.Int64Value(int64(d.Total)),
			"free":     types.Int64Value(int64(d.Free)),
			"machines": stringList(d.Here),
		}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: machineDeviceAttrTypes}, rows)
}

// stringList is a list of strings, empty (never null) for none.
func stringList(ss []string) types.List {
	vals := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

func (d *machineDevicesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	ds, err := d.data.Client.MachineDevices(ctx)
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		resp.Diagnostics.AddError("Cannot read the devices",
			"The platform doesn't list devices for machines: this needs a LiveLLM API with machine devices.")
		return
	case err != nil:
		resp.Diagnostics.AddError("Cannot read the devices", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, machineDevicesModel{Devices: machineDeviceList(ds)})...)
}
