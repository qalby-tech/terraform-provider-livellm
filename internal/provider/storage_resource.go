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

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
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
		Description: "A managed database — Postgres or Redis — with optional backups (Postgres) and " +
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
				Optional:      true,
				Computed:      true,
				Description:   "Disk size in GiB (5 when unset). Growing is an in-place update; shrinking is not supported.",
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{keepSizeWhenUnset{}},
			},
			"instances": schema.Int64Attribute{
				Optional: true,
				Description: "1, or 3 for Postgres: two standby copies, one of which takes over if the main one fails. " +
					"Redis runs as one instance.",
				Validators: []validator.Int64{int64validator.OneOf(1, 3)},
			},
			"cpu": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "CPU, e.g. \"1\" or \"500m\" (1 when unset).",
				PlanModifiers: []planmodifier.String{keepSizeWhenUnset{}},
			},
			"memory": schema.StringAttribute{
				Optional:      true,
				Computed:      true,
				Description:   "Memory, e.g. \"1Gi\" (1Gi when unset).",
				PlanModifiers: []planmodifier.String{keepSizeWhenUnset{}},
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
				Optional: true,
				Description: "Deprecated: use the backup block. Any schedule turns on daily backups " +
					"(a full copy each night); the value itself is no longer used.",
				DeprecationMessage: "Use backup { mode = \"daily\", keep_days = N } instead. " +
					"backup_schedule still turns on daily backups.",
			},
			"backup_keep": schema.Int64Attribute{
				Optional: true,
				Description: "Deprecated: use backup.keep_days. How many DAYS backups are kept (1..365) — " +
					"it was always days, not a number of backups. Only with backup_schedule.",
				DeprecationMessage: "Use backup { keep_days = N } instead; it is the same number of days.",
				Validators:         []validator.Int64{int64validator.Between(1, 365)},
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
						"udp":  schema.BoolAttribute{Computed: true},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"backup": schema.SingleNestedBlock{
				Description: "Backups (Postgres only). With the block, backups are on; without it, off. " +
					"A backup restores into a new database; the one it came from keeps running.",
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Optional: true,
						Description: "daily (default): a full copy each night. continuous: the nightly copy plus every " +
							"change in between, so the database can be restored to any minute inside keep_days. " +
							"manual: nothing is scheduled; a backup is taken only when asked for.",
						Validators: []validator.String{stringvalidator.OneOf("daily", "continuous", "manual")},
					},
					"keep_days": schema.Int64Attribute{
						Optional:    true,
						Description: "How many days backups are kept, 1..365 (10 when unset). Days, not a number of backups.",
						Validators:  []validator.Int64{int64validator.Between(1, 365)},
					},
				},
			},
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
	Timeouts          timeouts.Value      `tfsdk:"timeouts"`
	Name              types.String        `tfsdk:"name"`
	Engine            types.String        `tfsdk:"engine"`
	Version           types.String        `tfsdk:"version"`
	DiskGi            types.Int64         `tfsdk:"disk_gi"`
	Instances         types.Int64         `tfsdk:"instances"`
	CPU               types.String        `tfsdk:"cpu"`
	Memory            types.String        `tfsdk:"memory"`
	Username          types.String        `tfsdk:"username"`
	PasswordWO        types.String        `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64         `tfsdk:"password_wo_version"`
	Expose            types.Bool          `tfsdk:"expose"`
	Allowlist         types.List          `tfsdk:"allowlist"`
	BackupSchedule    types.String        `tfsdk:"backup_schedule"`
	BackupKeep        types.Int64         `tfsdk:"backup_keep"`
	Backup            *storageBackupModel `tfsdk:"backup"`
	Ready             types.Bool          `tfsdk:"ready"`
	Endpoints         types.List          `tfsdk:"endpoints"`
}

// storageBackupModel is the backup block: on when present.
type storageBackupModel struct {
	Mode     types.String `tfsdk:"mode"`
	KeepDays types.Int64  `tfsdk:"keep_days"`
}

// The platform's backup defaults: a backup block without them reads back as
// written, so an unset mode or keep_days never shows as a change.
const (
	defaultBackupMode     = "daily"
	defaultBackupKeepDays = 10
)

// storageBackup is the backup the database is written with. The platform
// keeps backups only with enabled: true — a schedule alone once meant none
// were taken — so every form that asks for backups says so. On an update
// with no backup configured it says they are off, since the platform keeps
// an explicit off and the write replaces the database's settings. Redis has
// no backups, so nothing is said about them.
func storageBackup(m storageModel, update bool) map[string]any {
	switch {
	case m.Backup != nil:
		// Both are always sent: a saved database keeps what the block leaves
		// out, so leaving the defaults to the platform would never put back a
		// mode or keep changed elsewhere.
		b := map[string]any{"enabled": true, "mode": defaultBackupMode, "keepDays": int64(defaultBackupKeepDays)}
		if v := m.Backup.Mode.ValueString(); v != "" {
			b["mode"] = v
		}
		if !m.Backup.KeepDays.IsNull() && !m.Backup.KeepDays.IsUnknown() {
			b["keepDays"] = m.Backup.KeepDays.ValueInt64()
		}
		return b
	case m.BackupSchedule.ValueString() != "":
		// The deprecated form says nothing about the mode, so none is sent:
		// the platform makes a new database daily and keeps a saved one's
		// mode, so a database made continuous elsewhere stays continuous.
		b := map[string]any{"enabled": true, "schedule": m.BackupSchedule.ValueString(),
			"keepDays": int64(defaultBackupKeepDays)}
		if !m.BackupKeep.IsNull() && !m.BackupKeep.IsUnknown() {
			b["keepDays"] = m.BackupKeep.ValueInt64()
		}
		return b
	case update && m.Engine.ValueString() == "postgres":
		return map[string]any{"enabled": false}
	}
	return nil
}

// storageSpec builds the kind block (also the flat create body without id).
func storageSpec(ctx context.Context, m storageModel, password string, update bool) map[string]any {
	spec := map[string]any{"engine": m.Engine.ValueString()}
	if v := m.Version.ValueString(); v != "" {
		spec["version"] = v
	}
	if !m.DiskGi.IsNull() && !m.DiskGi.IsUnknown() {
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
	if b := storageBackup(m, update); b != nil {
		spec["backup"] = b
	}
	return spec
}

// readStorageBackup reads the database's backups back in the form the
// configuration uses: the deprecated attributes when it uses them, the block
// otherwise. Backups turned off (or on) elsewhere show as a change.
func readStorageBackup(state *storageModel, raw any) {
	b, _ := raw.(map[string]any)
	on, _ := b["enabled"].(bool)
	mode, _ := b["mode"].(string)
	var keep int64
	if v, ok := b["keepDays"].(float64); ok {
		keep = int64(v)
	} else if v, ok := b["maxBackups"].(float64); ok {
		keep = int64(v)
	}
	if !state.BackupSchedule.IsNull() {
		if !on {
			state.BackupSchedule, state.BackupKeep = types.StringNull(), types.Int64Null()
			return
		}
		if keep > 0 && !(state.BackupKeep.IsNull() && keep == defaultBackupKeepDays) {
			state.BackupKeep = types.Int64Value(keep)
		}
		return
	}
	if !on {
		state.Backup = nil
		return
	}
	prev := state.Backup
	if prev == nil {
		prev = &storageBackupModel{Mode: types.StringNull(), KeepDays: types.Int64Null()}
	}
	next := &storageBackupModel{Mode: prev.Mode, KeepDays: prev.KeepDays}
	if mode != "" && !(prev.Mode.IsNull() && mode == defaultBackupMode) {
		next.Mode = types.StringValue(mode)
	}
	if keep > 0 && !(prev.KeepDays.IsNull() && keep == defaultBackupKeepDays) {
		next.KeepDays = types.Int64Value(keep)
	}
	state.Backup = next
}

// keepSizeWhenUnset plans a size the configuration leaves out as what the
// database already has — including nothing, for a database saved before the
// platform filled sizes in. UseStateForUnknown skips a null prior value, which
// left such a database "(known after apply)" and updated on every plan; and
// sending the platform's CPU or memory to it would restart it with resources
// it never had.
type keepSizeWhenUnset struct{}

func (keepSizeWhenUnset) Description(context.Context) string {
	return "Keeps the database's current value when the configuration leaves it out."
}

func (m keepSizeWhenUnset) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (keepSizeWhenUnset) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

func (keepSizeWhenUnset) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.State.Raw.IsNull() || !req.ConfigValue.IsNull() {
		return
	}
	resp.PlanValue = req.StateValue
}

// readStorageSizes fills the sizes the platform decided when the
// configuration left them out, so the next write sends them back unchanged.
func readStorageSizes(state *storageModel, sp map[string]any) {
	state.DiskGi = types.Int64Null()
	if v, ok := sp["storageSize"].(string); ok && v != "" {
		var gi int64
		fmt.Sscanf(v, "%dGi", &gi)
		if gi > 0 {
			state.DiskGi = types.Int64Value(gi)
		}
	}
	state.CPU, state.Memory = types.StringNull(), types.StringNull()
	if v, ok := sp["cpu"].(string); ok && v != "" {
		state.CPU = types.StringValue(v)
	}
	if v, ok := sp["memory"].(string); ok && v != "" {
		state.Memory = types.StringValue(v)
	}
}

// fillStorageComputed reads the sizes back after a write.
func fillStorageComputed(ctx context.Context, c *client.Client, m *storageModel, diags *diag.Diagnostics) {
	ws, err := c.Workloads(ctx)
	if err == nil {
		if w := findWorkload(ws, m.Name.ValueString()); w != nil && w.Storage != nil {
			readStorageSizes(m, w.Storage)
			return
		}
	}
	if m.DiskGi.IsUnknown() {
		m.DiskGi = types.Int64Null()
	}
	if m.CPU.IsUnknown() {
		m.CPU = types.StringNull()
	}
	if m.Memory.IsUnknown() {
		m.Memory = types.StringNull()
	}
}

func (r *storageResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg storageModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range storageConfigErrors(cfg) {
		resp.Diagnostics.AddError(e[0], e[1])
	}
}

// storageConfigErrors are the checks the platform makes, at plan time.
func storageConfigErrors(m storageModel) [][2]string {
	var errs [][2]string
	legacy := !m.BackupSchedule.IsNull() || !m.BackupKeep.IsNull()
	if m.Backup != nil && legacy {
		errs = append(errs, [2]string{"Two ways to set backups",
			"Use the backup block alone; backup_schedule and backup_keep are its deprecated form."})
	}
	if m.Backup == nil && m.BackupSchedule.IsNull() && !m.BackupKeep.IsNull() {
		errs = append(errs, [2]string{"backup_keep without backup_schedule",
			"backup_keep does nothing on its own. Use backup { keep_days = N }."})
	}
	if m.Engine.IsUnknown() || m.Engine.IsNull() {
		return errs
	}
	if m.Engine.ValueString() == "redis" {
		if m.Backup != nil || !m.BackupSchedule.IsNull() {
			errs = append(errs, [2]string{"Backups are for Postgres only",
				"Redis keeps its keys on disk across restarts, but has no backups. Remove the backup settings."})
		}
		if !m.Instances.IsUnknown() && m.Instances.ValueInt64() > 1 {
			errs = append(errs, [2]string{"Redis runs as one instance",
				"Three instances are for Postgres, where a copy takes over when the main one fails. Set instances = 1 or leave it out."})
		}
	}
	return errs
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
			UDP:  types.BoolValue(e.UDP),
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
	body := storageSpec(ctx, plan, cfg.PasswordWO.ValueString(), false)
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
	fillStorageComputed(ctx, r.data.Client, &plan, &resp.Diagnostics)
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
	readStorageSizes(&state, sp)
	if v, ok := sp["instances"].(float64); ok && v > 0 {
		state.Instances = types.Int64Value(int64(v))
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
	readStorageBackup(&state, sp["backup"])
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
		Storage: storageSpec(ctx, plan, password, true),
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
	fillStorageComputed(ctx, r.data.Client, &plan, &resp.Diagnostics)
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
