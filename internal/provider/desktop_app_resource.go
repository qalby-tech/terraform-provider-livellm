package provider

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_desktop_app — a Desktop App: one Linux desktop in a container that
// starts in seconds. An agent works it through the computer tool; a person
// watches it in the console.
type desktopAppResource struct {
	data *providerData
}

func NewDesktopAppResource() resource.Resource {
	return &desktopAppResource{}
}

func (r *desktopAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_desktop_app"
}

var resolutionRe = regexp.MustCompile(`^[0-9]{3,4}x[0-9]{3,4}$`)

func (r *desktopAppResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Desktop App: one Linux desktop in a container that starts in seconds. " +
			"Reach it with connect's view or computer tool; make one for each desktop you need.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the app.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image": schema.StringAttribute{
				Optional: true,
				Description: "A desktop image. Leave it out for the platform's (ghcr.io/qalby-tech/livellm-desktop:xfce); " +
					"your own works if it follows that image's contract.",
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU, e.g. \"2\" (the default).",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory, e.g. \"4Gi\" (the default).",
			},
			"resolution": schema.StringAttribute{
				Optional:    true,
				Description: "Screen size, e.g. \"1280x800\" (the default).",
				Validators: []validator.String{stringvalidator.RegexMatches(resolutionRe,
					"must look like 1280x800")},
			},
			"keep_files": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				Description: "Give the desktop a home folder that survives restarts. By default it starts clean " +
					"every time. Changing it replaces the app.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"storage_gi": schema.Int64Attribute{
				Optional: true,
				Description: "With keep_files: the home folder in GiB (10 when left out). Changing it " +
					"replaces the app.",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"stopped": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Stop the desktop without deleting the app. The home folder is kept (keep_files) and only it is billed.",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the desktop is up (false while stopped)."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *desktopAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type desktopAppModel struct {
	Name       types.String   `tfsdk:"name"`
	Image      types.String   `tfsdk:"image"`
	CPU        types.String   `tfsdk:"cpu"`
	Memory     types.String   `tfsdk:"memory"`
	Resolution types.String   `tfsdk:"resolution"`
	KeepFiles  types.Bool     `tfsdk:"keep_files"`
	StorageGi  types.Int64    `tfsdk:"storage_gi"`
	Stopped    types.Bool     `tfsdk:"stopped"`
	Ready      types.Bool     `tfsdk:"ready"`
	Timeouts   timeouts.Value `tfsdk:"timeouts"`
}

func (r *desktopAppResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg desktopAppModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !cfg.StorageGi.IsNull() && !cfg.KeepFiles.IsUnknown() && !cfg.KeepFiles.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("storage_gi"), "storage_gi needs keep_files",
			"A Desktop App keeps a home folder only with keep_files = true; without it the desktop starts clean and has none.")
	}
}

// desktopSpec is the desktop block; what is left out takes the platform's
// default.
func desktopSpec(m desktopAppModel) map[string]any {
	spec := map[string]any{}
	for key, v := range map[string]types.String{"image": m.Image, "cpu": m.CPU, "memory": m.Memory, "resolution": m.Resolution} {
		if s := v.ValueString(); s != "" {
			spec[key] = s
		}
	}
	if m.KeepFiles.ValueBool() {
		spec["keepFiles"] = true
		if !m.StorageGi.IsNull() && !m.StorageGi.IsUnknown() {
			spec["storageSize"] = fmt.Sprintf("%dGi", m.StorageGi.ValueInt64())
		}
	}
	return spec
}

// readDesktopSpec fills the model from the platform's copy, so a change made
// in the console shows in the plan and an import is complete.
func readDesktopSpec(m *desktopAppModel, w *client.Workload) {
	d := w.Desktop
	m.Stopped = types.BoolValue(w.Stopped)
	str := func(key string) types.String {
		if s, _ := d[key].(string); s != "" {
			return types.StringValue(s)
		}
		return types.StringNull()
	}
	m.Image, m.CPU, m.Memory, m.Resolution = str("image"), str("cpu"), str("memory"), str("resolution")
	keep, _ := d["keepFiles"].(bool)
	m.KeepFiles = types.BoolValue(keep)
	m.StorageGi = types.Int64Null()
	if s, _ := d["storageSize"].(string); s != "" {
		var gi int64
		fmt.Sscanf(s, "%dGi", &gi)
		if gi > 0 {
			m.StorageGi = types.Int64Value(gi)
		}
	}
}

func refreshDesktopStatus(ctx context.Context, c *client.Client, m *desktopAppModel) {
	st, _ := statusOf(ctx, c, m.Name.ValueString())
	m.Ready = types.BoolValue(st != nil && st.Ready)
}

func (r *desktopAppResource) wait(ctx context.Context, m desktopAppModel) error {
	d := 15 * time.Minute
	t, diags := m.Timeouts.Create(ctx, d)
	if !diags.HasError() {
		d = t
	}
	waitCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return waitReady(waitCtx, r.data.Client, m.Name.ValueString(), m.Stopped.ValueBool())
}

func (r *desktopAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan desktopAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := desktopSpec(plan)
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "desktop", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create Desktop App", err)
		return
	}
	if plan.Stopped.ValueBool() {
		// Born stopped: stopped lives on the workload, not the create body.
		w := client.Workload{ID: plan.Name.ValueString(), Type: "desktop", Stopped: true, Desktop: desktopSpec(plan)}
		if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
			apiDiag(&resp.Diagnostics, "Cannot stop the Desktop App after create", err)
		}
	}
	if err := r.wait(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Desktop App did not become ready", err.Error())
	}
	refreshDesktopStatus(ctx, r.data.Client, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *desktopAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state desktopAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ws, err := r.data.Client.Workloads(ctx)
	if err != nil {
		apiDiag(&resp.Diagnostics, "Cannot read workspace", err)
		return
	}
	w := findWorkload(ws, state.Name.ValueString())
	if w == nil || w.Type != "desktop" {
		resp.State.RemoveResource(ctx)
		return
	}
	readDesktopSpec(&state, w)
	refreshDesktopStatus(ctx, r.data.Client, &state)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *desktopAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan desktopAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w := client.Workload{
		ID:      plan.Name.ValueString(),
		Type:    "desktop",
		Stopped: plan.Stopped.ValueBool(),
		Desktop: desktopSpec(plan),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update Desktop App", err)
		return
	}
	if err := r.wait(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Desktop App did not become ready after update", err.Error())
	}
	refreshDesktopStatus(ctx, r.data.Client, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *desktopAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state desktopAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete Desktop App", err)
		return
	}
	deleteTimeout, d := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if err := waitGone(waitCtx, r.data.Client, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddWarning("Deletion still in progress", err.Error())
	}
}

func (r *desktopAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
