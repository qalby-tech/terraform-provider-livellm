package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_vm — an Ubuntu VM (terminal or desktop), with ports, network
// gating, placement, an optional AI daemon, and stop-without-destroy.
type vmResource struct {
	data *providerData
}

func NewVMResource() resource.Resource {
	return &vmResource{}
}

func (r *vmResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm"
}

func (r *vmResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An Ubuntu VM — terminal or desktop. SSH login is write-only; exposed ports get " +
			"public HTTPS hostnames; stopped keeps the disk while halting the machine.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the VM.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"desktop": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "GUI Linux Desktop instead of a terminal VM. Changing it replaces the VM.",
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"cpus": schema.Int64Attribute{
				Optional:    true,
				Description: "vCPU count.",
			},
			"memory_gi": schema.Int64Attribute{
				Optional:    true,
				Description: "Memory in GiB.",
			},
			"disk_gi": schema.Int64Attribute{
				Optional:    true,
				Description: "Root disk in GiB.",
			},
			"username": schema.StringAttribute{
				Required:    true,
				Description: "SSH username. Changing it replaces the VM (the login is baked at first boot).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password_wo": schema.StringAttribute{
				Required:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "SSH password (write-only: never stored in state). Re-sent when password_wo_version changes.",
			},
			"password_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for password_wo.",
			},
			"stopped": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Halt the VM without destroying it — the disk is kept and billing drops to disk-only.",
			},
			"allow_cidrs": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Source CIDRs allowed to reach SSH and any raw ports. Omit = reachable only from inside the workspace; \"0.0.0.0/0\" = public.",
			},
			"placement_strategy": schema.StringAttribute{
				Optional:    true,
				Description: "Where the VM runs: omit for automatic (the default — LiveLLM picks the host), \"region\" for any host in placement_region, \"host\" to pin placement_host.",
				Validators:  []validator.String{stringvalidator.OneOf("auto", "host", "region")},
			},
			"placement_host": schema.StringAttribute{
				Optional:    true,
				Description: "Host id to pin to (placement_strategy = \"host\"). Hosts come from the fleet endpoint.",
			},
			"placement_region": schema.StringAttribute{
				Optional:    true,
				Description: "Region to schedule into (placement_strategy = \"region\").",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the VM is up (false while stopped)."},
			"ssh":   schema.StringAttribute{Computed: true, Description: "host:port to SSH into the VM, as reported by the platform."},
			"url":   schema.StringAttribute{Computed: true, Description: "The first exposed HTTP port's public HTTPS URL."},
			"endpoints": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Every exposed port's public address.",
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
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"port": schema.ListNestedBlock{
				Description: "Exposed ports. HTTP ports get a public HTTPS hostname; tcp/udp ports get a raw address.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "Port name (part of the hostname for HTTP ports)."},
						"port": schema.Int64Attribute{Required: true, Description: "Listener port inside the VM."},
						"tcp":  schema.BoolAttribute{Optional: true, Description: "Expose as a raw TCP address instead of HTTPS."},
						"udp":  schema.BoolAttribute{Optional: true, Description: "Expose as a raw UDP address."},
					},
				},
			},
			"ai_daemon": schema.SingleNestedBlock{
				Description: "Attach an AI agent that operates this VM over SSH. The engine is selected automatically from the provider.",
				Attributes: map[string]schema.Attribute{
					"provider": schema.StringAttribute{Optional: true, Description: "A connected AI provider id (Integrations page). Required when the block is present."},
					"model":    schema.StringAttribute{Optional: true, Description: "Model id; defaults to the provider's recommendation."},
					"sudo":     schema.BoolAttribute{Optional: true, Description: "Allow the agent passwordless sudo."},
					"instructions": schema.StringAttribute{
						Optional:    true,
						Description: "Standing guidance for the agent.",
					},
				},
			},
		},
	}
}

func (r *vmResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type vmPortModel struct {
	Name types.String `tfsdk:"name"`
	Port types.Int64  `tfsdk:"port"`
	TCP  types.Bool   `tfsdk:"tcp"`
	UDP  types.Bool   `tfsdk:"udp"`
}

type aiDaemonModel struct {
	Provider     types.String `tfsdk:"provider"`
	Model        types.String `tfsdk:"model"`
	Sudo         types.Bool   `tfsdk:"sudo"`
	Instructions types.String `tfsdk:"instructions"`
}

type vmResourceModel struct {
	Timeouts timeouts.Value `tfsdk:"timeouts"`
	Name              types.String `tfsdk:"name"`
	Desktop           types.Bool   `tfsdk:"desktop"`
	CPUs              types.Int64  `tfsdk:"cpus"`
	MemoryGi          types.Int64  `tfsdk:"memory_gi"`
	DiskGi            types.Int64  `tfsdk:"disk_gi"`
	Username          types.String `tfsdk:"username"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64  `tfsdk:"password_wo_version"`
	Stopped           types.Bool   `tfsdk:"stopped"`
	AllowCIDRs        types.List   `tfsdk:"allow_cidrs"`
	Port              types.List   `tfsdk:"port"`
	PlacementStrategy types.String `tfsdk:"placement_strategy"`
	PlacementHost     types.String `tfsdk:"placement_host"`
	PlacementRegion   types.String `tfsdk:"placement_region"`
	AIDaemon          types.Object `tfsdk:"ai_daemon"`
	Ready             types.Bool   `tfsdk:"ready"`
	SSH               types.String `tfsdk:"ssh"`
	URL               types.String `tfsdk:"url"`
	Endpoints         types.List   `tfsdk:"endpoints"`
}

func (m vmResourceModel) workloadType() string {
	if m.Desktop.ValueBool() {
		return "vm-ubuntu-desktop"
	}
	return "vm-ubuntu"
}

// vmSpec builds the vm kind block; password empty = omit credentials (no change).
func vmSpec(ctx context.Context, m vmResourceModel, password string) map[string]any {
	spec := map[string]any{}
	if !m.CPUs.IsNull() {
		spec["cpus"] = m.CPUs.ValueInt64()
	}
	if !m.MemoryGi.IsNull() {
		spec["memory"] = fmt.Sprintf("%dGi", m.MemoryGi.ValueInt64())
	}
	if !m.DiskGi.IsNull() {
		spec["storageSize"] = fmt.Sprintf("%dGi", m.DiskGi.ValueInt64())
	}
	if password != "" {
		spec["credentials"] = map[string]any{
			"username": m.Username.ValueString(),
			"password": password,
		}
	}
	if !m.AllowCIDRs.IsNull() {
		var cidrs []string
		m.AllowCIDRs.ElementsAs(ctx, &cidrs, false)
		spec["allowCIDRs"] = cidrs
	}
	if !m.Port.IsNull() {
		var ports []vmPortModel
		m.Port.ElementsAs(ctx, &ports, false)
		out := make([]map[string]any, 0, len(ports))
		for _, p := range ports {
			e := map[string]any{"name": p.Name.ValueString(), "port": p.Port.ValueInt64()}
			if p.TCP.ValueBool() {
				e["tcp"] = true
			}
			if p.UDP.ValueBool() {
				e["udp"] = true
			}
			out = append(out, e)
		}
		spec["ports"] = out
	}
	strategy := m.PlacementStrategy.ValueString()
	if strategy != "" && strategy != "auto" {
		pl := map[string]any{"strategy": strategy}
		if v := m.PlacementHost.ValueString(); v != "" {
			pl["host"] = v
		}
		if v := m.PlacementRegion.ValueString(); v != "" {
			pl["region"] = v
		}
		spec["placement"] = pl
	}
	if !m.AIDaemon.IsNull() {
		var d aiDaemonModel
		m.AIDaemon.As(ctx, &d, basetypes.ObjectAsOptions{})
		daemon := map[string]any{
			"enabled":  true,
			"provider": d.Provider.ValueString(),
		}
		if v := d.Model.ValueString(); v != "" {
			daemon["model"] = v
		}
		if d.Sudo.ValueBool() {
			daemon["sudo"] = true
		}
		if v := d.Instructions.ValueString(); v != "" {
			daemon["instructions"] = v
		}
		spec["aiDaemon"] = daemon
	}
	return spec
}

func refreshVMStatus(ctx context.Context, c *client.Client, m *vmResourceModel, diags *diag.Diagnostics) {
	epType := types.ObjectType{AttrTypes: endpointAttrTypes}
	st, err := statusOf(ctx, c, m.Name.ValueString())
	if err != nil || st == nil {
		m.Ready = types.BoolValue(false)
		m.SSH = types.StringValue("")
		m.URL = types.StringValue("")
		m.Endpoints = types.ListValueMust(epType, []attr.Value{})
		return
	}
	m.Ready = types.BoolValue(st.Ready)
	m.SSH = types.StringValue(st.SSH)
	url := ""
	eps := make([]endpointModel, 0, len(st.Endpoints))
	for _, e := range st.Endpoints {
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
	m.URL = types.StringValue(url)
	list, d := types.ListValueFrom(ctx, epType, eps)
	diags.Append(d...)
	m.Endpoints = list
}

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg vmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := vmSpec(ctx, plan, cfg.PasswordWO.ValueString())
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, plan.workloadType(), body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create VM", err)
		return
	}
	if plan.Stopped.ValueBool() {
		// Born stopped: create, then immediately halt (stopped lives on the
		// workload, not the create body).
		w := client.Workload{ID: plan.Name.ValueString(), Type: plan.workloadType(), Stopped: true, VM: vmSpec(ctx, plan, "")}
		if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
			apiDiag(&resp.Diagnostics, "Cannot stop VM after create", err)
		}
	}
	createTimeout, td := plan.Timeouts.Create(ctx, 15*time.Minute)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), plan.Stopped.ValueBool()); err != nil {
		resp.Diagnostics.AddError("VM did not become ready", err.Error())
	}
	plan.PasswordWO = types.StringNull()
	refreshVMStatus(ctx, r.data.Client, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vmResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vmResourceModel
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
	if w == nil || w.VM == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Desktop = types.BoolValue(w.Type == "vm-ubuntu-desktop")
	state.Stopped = types.BoolValue(w.Stopped)
	sp := w.VM
	if v, ok := sp["cpus"].(float64); ok && v > 0 {
		state.CPUs = types.Int64Value(int64(v))
	}
	if v, ok := sp["memory"].(string); ok && v != "" {
		var gi int64
		fmt.Sscanf(v, "%dGi", &gi)
		if gi > 0 {
			state.MemoryGi = types.Int64Value(gi)
		}
	}
	if v, ok := sp["storageSize"].(string); ok && v != "" {
		var gi int64
		fmt.Sscanf(v, "%dGi", &gi)
		if gi > 0 {
			state.DiskGi = types.Int64Value(gi)
		}
	}
	if raw, ok := sp["allowCIDRs"].([]any); ok {
		vals := make([]attr.Value, 0, len(raw))
		for _, c := range raw {
			if s, ok := c.(string); ok {
				vals = append(vals, types.StringValue(s))
			}
		}
		state.AllowCIDRs = types.ListValueMust(types.StringType, vals)
	}
	if pl, ok := sp["placement"].(map[string]any); ok {
		if v, ok := pl["strategy"].(string); ok && v != "" {
			state.PlacementStrategy = types.StringValue(v)
		}
		if v, ok := pl["host"].(string); ok && v != "" {
			state.PlacementHost = types.StringValue(v)
		}
		if v, ok := pl["region"].(string); ok && v != "" {
			state.PlacementRegion = types.StringValue(v)
		}
	}
	refreshVMStatus(ctx, r.data.Client, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *vmResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state vmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	password := ""
	if !plan.PasswordWOVersion.Equal(state.PasswordWOVersion) {
		password = cfg.PasswordWO.ValueString()
	}
	w := client.Workload{
		ID:      plan.Name.ValueString(),
		Type:    plan.workloadType(),
		Stopped: plan.Stopped.ValueBool(),
		VM:      vmSpec(ctx, plan, password),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update VM", err)
		return
	}
	createTimeout, td := plan.Timeouts.Create(ctx, 15*time.Minute)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, plan.Stopped.ValueBool()); err != nil {
		resp.Diagnostics.AddError("VM did not become ready after update", err.Error())
	}
	plan.PasswordWO = types.StringNull()
	refreshVMStatus(ctx, r.data.Client, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vmResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vmResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete VM", err)
		return
	}
	deleteTimeout, td := state.Timeouts.Delete(ctx, 10*time.Minute)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if err := waitGone(waitCtx, r.data.Client, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddWarning("Deletion still in progress", err.Error())
	}
}

func (r *vmResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("password_wo_version"), int64(1))...)
}
