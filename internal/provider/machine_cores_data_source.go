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

// data.livellm_machine_cores — what a new machine with dedicated cores can
// get on the servers the workspace may run on: the servers' clock speeds, the
// values core_speed takes, and how many cores of each are free on one server.
// No arguments: the key already pins the workspace.
type machineCoresDataSource struct {
	data *providerData
}

func NewMachineCoresDataSource() datasource.DataSource {
	return &machineCoresDataSource{}
}

func (d *machineCoresDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_machine_cores"
}

func (d *machineCoresDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The cores a new machine with dedicated cores can get: the servers' clock speeds, the values " +
			"core_speed takes, and how many cores of each are free on one server.",
		Attributes: map[string]schema.Attribute{
			"show_speed": schema.BoolAttribute{
				Computed: true,
				Description: "Whether a core speed can be asked for: the servers' cores run at more than one clock " +
					"speed. False where they all run at one speed: leave core_speed out.",
			},
			"speeds": schema.ListNestedAttribute{
				Computed:    true,
				Description: "One entry per clock speed the servers have, fastest first; empty where show_speed is false.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"speed": schema.Int64Attribute{Computed: true, Description: "The clock in MHz every core of this speed reaches, for core_speed."},
						"fit": schema.Int64Attribute{Computed: true, Description: "The most cores of this speed a new machine can get on " +
							"one server: free, and no more than the server has room for. More at this speed are refused."},
					},
				},
			},
			"dedicated_fit": schema.Int64Attribute{
				Computed: true,
				Description: "The most cores of one speed a new machine can get on one server, any speed. More are " +
					"refused as mixed core speeds unless mixed_speeds = true.",
			},
		},
	}
}

func (d *machineCoresDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

var coreSpeedAttrTypes = map[string]attr.Type{
	"speed": types.Int64Type,
	"fit":   types.Int64Type,
}

type coreSpeedModel struct {
	Speed types.Int64 `tfsdk:"speed"`
	Fit   types.Int64 `tfsdk:"fit"`
}

type machineCoresModel struct {
	ShowSpeed    types.Bool  `tfsdk:"show_speed"`
	Speeds       types.List  `tfsdk:"speeds"`
	DedicatedFit types.Int64 `tfsdk:"dedicated_fit"`
}

// coreSpeedList turns the platform's speeds into the speeds attribute: an
// empty list, never null, where it lists none.
func coreSpeedList(ctx context.Context, ss []client.CoreSpeedRoom) (types.List, error) {
	rows := make([]coreSpeedModel, 0, len(ss))
	for _, s := range ss {
		rows = append(rows, coreSpeedModel{
			Speed: types.Int64Value(int64(s.SpeedMHz)),
			Fit:   types.Int64Value(int64(s.Fit)),
		})
	}
	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: coreSpeedAttrTypes}, rows)
	if diags.HasError() {
		return list, fmt.Errorf("%v", diags)
	}
	return list, nil
}

func (d *machineCoresDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	mc, err := d.data.Client.MachineCores(ctx)
	var apiErr *client.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		resp.Diagnostics.AddError("Cannot read the cores",
			"The platform doesn't list its cores: this needs a LiveLLM API with dedicated cores.")
		return
	case err != nil:
		resp.Diagnostics.AddError("Cannot read the cores", err.Error())
		return
	}
	list, err := coreSpeedList(ctx, mc.Speeds)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the cores", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, machineCoresModel{
		ShowSpeed:    types.BoolValue(mc.ShowSpeed),
		Speeds:       list,
		DedicatedFit: types.Int64Value(int64(mc.DedicatedFit)),
	})...)
}
