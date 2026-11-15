package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// data.livellm_gpu_driver — a machine's NVIDIA driver. Every machine made on
// LiveLLM API 0.101.9 or newer installs it by itself when it has an NVIDIA
// card; this reads where that is. Installing on a machine made before it is
// the console's (Install NVIDIA driver) or the CLI's (livellm driver install).
type gpuDriverDataSource struct {
	data *providerData
}

func NewGPUDriverDataSource() datasource.DataSource {
	return &gpuDriverDataSource{}
}

func (d *gpuDriverDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_gpu_driver"
}

func (d *gpuDriverDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A machine's NVIDIA driver: whether it works, is installing, needs a restart or failed. " +
			"Machines install it by themselves when they have an NVIDIA card.",
		Attributes: map[string]schema.Attribute{
			"vm": schema.StringAttribute{
				Required:    true,
				Description: "The machine's id (livellm_vm's id).",
			},
			"state": schema.StringAttribute{
				Computed: true,
				Description: "none (no NVIDIA card), off (turned off inside the machine), working, restart-needed " +
					"(installed: restart the machine to finish), installing, pending (not run yet), failed, " +
					"unsupported (its system gets no automatic driver), not-installed (a machine made before the " +
					"driver program: install it from the console or with livellm driver install), unknown; or " +
					"machine-stopped and machine-unreachable when it can't be read now (detail says why).",
			},
			"detail": schema.StringAttribute{
				Computed:    true,
				Description: "More about the state: the driver's version when working, the reason when failed. Empty when there is none.",
			},
			"log": schema.ListAttribute{
				Computed:    true,
				ElementType: types.StringType,
				Description: "The last lines of the driver program's log in the machine.",
			},
		},
	}
}

func (d *gpuDriverDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

type gpuDriverModel struct {
	VM     types.String `tfsdk:"vm"`
	State  types.String `tfsdk:"state"`
	Detail types.String `tfsdk:"detail"`
	Log    types.List   `tfsdk:"log"`
}

func (d *gpuDriverDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m gpuDriverModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := m.VM.ValueString()
	drv, err := d.data.Client.GPUDriver(ctx, id)
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.RouteMissing():
		resp.Diagnostics.AddError("Cannot read the NVIDIA driver",
			"The platform has no NVIDIA driver route: this needs LiveLLM API 0.101.9 or newer.")
		return
	case errors.As(err, &apiErr) && (apiErr.Status == http.StatusConflict || apiErr.Status == http.StatusGatewayTimeout):
		// A stopped machine, or one the platform can't get onto now: a
		// state, so a plan doesn't fail for it.
		state := "machine-unreachable"
		if apiErr.Code() == "machine_stopped" {
			state = "machine-stopped"
		}
		drv = &client.GPUDriver{State: state, Detail: apiErr.Message()}
	case err != nil:
		resp.Diagnostics.AddError("Cannot read the NVIDIA driver of "+id, err.Error())
		return
	}
	logs := drv.Log
	if logs == nil {
		logs = []string{}
	}
	list, diags := types.ListValueFrom(ctx, types.StringType, logs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	m.State = types.StringValue(drv.State)
	m.Detail = types.StringValue(drv.Detail)
	m.Log = list
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}
