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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_storage — a managed database (Postgres or Redis). Create/update
// wait until the database reports ready; the admin password is write-only.
type storageResource struct {
	data *providerData
}

func NewStorageResource() resource.Resource {
	return &storageResource{}
}

func (r *storageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage"
}

func (r *storageResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A managed database — Postgres or Redis — with optional scheduled backups and " +
			"external TLS exposure. Credentials are write-only.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the database.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine": schema.StringAttribute{
				Required:    true,
				Description: "postgres or redis. Changing it replaces the database.",
				Validators:  []validator.String{stringvalidator.OneOf("postgres", "redis")},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"version": schema.StringAttribute{
				Optional:    true,
				Description: "Engine major version (e.g. \"16\" for Postgres).",
			},
			"disk_gi": schema.Int64Attribute{
				Optional:    true,
				Description: "Disk size in GiB. Growing is an in-place update; shrinking is not supported.",
			},
			"instances": schema.Int64Attribute{
				Optional:    true,
				Description: "Instance count (Postgres HA).",
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU request, e.g. \"500m\".",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory request, e.g. \"1Gi\".",
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "Application username (Postgres). Set once at create.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password_wo": schema.StringAttribute{
				Required:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The database password (write-only: never stored in state, never readable back). Re-sent when password_wo_version changes.",
			},
			"password_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for password_wo.",
			},
			"expose": schema.BoolAttribute{
				Optional:    true,
				Description: "Expose the database externally (TLS, SNI-routed).",
			},
			"allowlist": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Client source CIDRs/IPs allowed when exposed. Empty = no IP restriction.",
			},
			"backup_schedule": schema.StringAttribute{
				Optional:    true,
				Description: "Backup schedule (Postgres): @hourly/@daily/@weekly/@monthly or 5-field cron.",
			},
			"backup_keep": schema.Int64Attribute{
				Optional:    true,
				Description: "How many scheduled backups to retain.",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the database is up."},
			"endpoints": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Connection endpoints as reported by the platform (in-cluster and, when exposed, external).",
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
		},
	}
}

func (r *storageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type storageModel struct {
	Timeouts timeouts.Value `tfsdk:"timeouts"`
	Name              types.String `tfsdk:"name"`
	Engine            types.String `tfsdk:"engine"`
	Version           types.String `tfsdk:"version"`
	DiskGi            types.Int64  `tfsdk:"disk_gi"`
	Instances         types.Int64  `tfsdk:"instances"`
	CPU               types.String `tfsdk:"cpu"`
	Memory            types.String `tfsdk:"memory"`
	Username          types.String `tfsdk:"username"`
	PasswordWO        types.String `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64  `tfsdk:"password_wo_version"`
	Expose            types.Bool   `tfsdk:"expose"`
	Allowlist         types.List   `tfsdk:"allowlist"`
	BackupSchedule    types.String `tfsdk:"backup_schedule"`
	BackupKeep        types.Int64  `tfsdk:"backup_keep"`
	Ready             types.Bool   `tfsdk:"ready"`
	Endpoints         types.List   `tfsdk:"endpoints"`
}

// storageSpec builds the kind block (also the flat create body without id).
func storageSpec(ctx context.Context, m storageModel, password string) map[string]any {
	spec := map[string]any{"engine": m.Engine.ValueString()}
	if v := m.Version.ValueString(); v != "" {
		spec["version"] = v
	}
	if !m.DiskGi.IsNull() {
		spec["storageSize"] = fmt.Sprintf("%dGi", m.DiskGi.ValueInt64())
	}
	if !m.Instances.IsNull() {
		spec["instances"] = m.Instances.ValueInt64()
	}
	if v := m.CPU.ValueString(); v != "" {
		spec["cpu"] = v
	}
	if v := m.Memory.ValueString(); v != "" {
		spec["memory"] = v
	}
	creds := map[string]any{}
	if v := m.Username.ValueString(); v != "" {
		creds["username"] = v
	}
	if password != "" {
		creds["password"] = password
	}
	if len(creds) > 0 {
		spec["credentials"] = creds
	}
	network := map[string]any{}
	if !m.Expose.IsNull() {
		network["expose"] = m.Expose.ValueBool()
	}
	if !m.Allowlist.IsNull() {
		var cidrs []string
		m.Allowlist.ElementsAs(ctx, &cidrs, false)
		network["allowlist"] = cidrs
	}
	if len(network) > 0 {
		spec["network"] = network
	}
	backup := map[string]any{}
	if v := m.BackupSchedule.ValueString(); v != "" {
		backup["schedule"] = v
	}
	if !m.BackupKeep.IsNull() {
		backup["maxBackups"] = m.BackupKeep.ValueInt64()
	}
	if len(backup) > 0 {
		spec["backup"] = backup
	}
	return spec
}

// refreshStorageStatus fills the computed ready/endpoints attributes.
func refreshStorageStatus(ctx context.Context, c *client.Client, m *storageModel, diags *diag.Diagnostics) {
	epType := types.ObjectType{AttrTypes: endpointAttrTypes}
	st, err := statusOf(ctx, c, m.Name.ValueString())
	if err != nil || st == nil {
		m.Ready = types.BoolValue(false)
		m.Endpoints = types.ListValueMust(epType, []attr.Value{})
		return
	}
	m.Ready = types.BoolValue(st.Ready)
	eps := make([]endpointModel, 0, len(st.Endpoints))
	for _, e := range st.Endpoints {
		eps = append(eps, endpointModel{
			Name: types.StringValue(e.Name),
			URL:  types.StringValue(e.URL),
			Addr: types.StringValue(e.Addr),
			TCP:  types.BoolValue(e.TCP),
		})
	}
	list, d := types.ListValueFrom(ctx, epType, eps)
	diags.Append(d...)
	m.Endpoints = list
}

func (r *storageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg storageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := storageSpec(ctx, plan, cfg.PasswordWO.ValueString())
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "storage", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create database", err)
		return
	}
	createTimeout, td := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("Database did not become ready", err.Error())
		// fall through: record what exists so destroy/retry work
	}
	plan.PasswordWO = types.StringNull()
	refreshStorageStatus(ctx, r.data.Client, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *storageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state storageModel
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
	if w == nil || w.Storage == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	sp := w.Storage
	if v, ok := sp["engine"].(string); ok {
		state.Engine = types.StringValue(v)
	}
	if v, ok := sp["version"].(string); ok && v != "" {
		state.Version = types.StringValue(v)
	}
	if v, ok := sp["storageSize"].(string); ok && v != "" {
		var gi int64
		fmt.Sscanf(v, "%dGi", &gi)
		if gi > 0 {
			state.DiskGi = types.Int64Value(gi)
		}
	}
	if v, ok := sp["instances"].(float64); ok && v > 0 {
		state.Instances = types.Int64Value(int64(v))
	}
	if v, ok := sp["cpu"].(string); ok && v != "" {
		state.CPU = types.StringValue(v)
	}
	if v, ok := sp["memory"].(string); ok && v != "" {
		state.Memory = types.StringValue(v)
	}
	if creds, ok := sp["credentials"].(map[string]any); ok {
		if v, ok := creds["username"].(string); ok && v != "" {
			state.Username = types.StringValue(v)
		}
	}
	if network, ok := sp["network"].(map[string]any); ok {
		if v, ok := network["expose"].(bool); ok {
			state.Expose = types.BoolValue(v)
		}
		if raw, ok := network["allowlist"].([]any); ok {
			vals := make([]attr.Value, 0, len(raw))
			for _, c := range raw {
				if s, ok := c.(string); ok {
					vals = append(vals, types.StringValue(s))
				}
			}
			state.Allowlist = types.ListValueMust(types.StringType, vals)
		}
	}
	if backup, ok := sp["backup"].(map[string]any); ok {
		if v, ok := backup["schedule"].(string); ok && v != "" {
			state.BackupSchedule = types.StringValue(v)
		}
		if v, ok := backup["maxBackups"].(float64); ok && v > 0 {
			state.BackupKeep = types.Int64Value(int64(v))
		}
	}
	refreshStorageStatus(ctx, r.data.Client, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *storageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state storageModel
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
		Type:    "storage",
		Storage: storageSpec(ctx, plan, password),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update database", err)
		return
	}
	createTimeout, td := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, false); err != nil {
		resp.Diagnostics.AddError("Database did not become ready after update", err.Error())
	}
	plan.PasswordWO = types.StringNull()
	refreshStorageStatus(ctx, r.data.Client, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *storageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state storageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete database", err)
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

func (r *storageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("password_wo_version"), int64(1))...)
}
