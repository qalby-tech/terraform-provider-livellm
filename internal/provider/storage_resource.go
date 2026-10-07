package provider

import (
	"context"
	"fmt"
	"strings"
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

// livellm_storage — a managed database (Postgres or Redis) or an object
// storage (engine s3: an S3 server of the workspace's own). Create/update wait
// until it reports ready; the password (an object storage's secret key) is
// write-only.
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
		Description: "A managed database — Postgres or Redis — or an object storage (engine s3: S3 buckets), with " +
			"optional backups (Postgres), an optional admin console and an optional external address. Credentials " +
			"are write-only. Inside the workspace it is reached only by what links it: an app's database block or " +
			"starts_after, a machine's or Desktop App's database block. It has no reachable_from. Its external " +
			"address (expose) has its own allowlist.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the database.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"engine": schema.StringAttribute{
				Required: true,
				Description: "postgres, redis or s3 (object storage: an S3 server with buckets, one copy, no backups). " +
					"Changing it replaces the database.",
				Validators: []validator.String{stringvalidator.OneOf("postgres", "redis", "s3")},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"version": schema.StringAttribute{
				Optional:    true,
				Description: "Engine major version (e.g. \"16\" for Postgres). Object storage runs version \"1\".",
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
					"Redis and object storage run as one instance.",
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
				Optional: true,
				Computed: true,
				Description: "Application username (Postgres), or an object storage's access key. Set once at create; " +
					"left out, the platform names it `app` (an object storage gets a generated access key). Leaving it " +
					"out later keeps the name the database has.",
				// Left out, the name the database has is planned, so it is
				// never replaced for a name no one asked to change (the
				// platform's own `app`, or the one an import read).
				PlanModifiers: []planmodifier.String{
					keepSizeWhenUnset{},
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password_wo": schema.StringAttribute{
				Required:  true,
				WriteOnly: true,
				Sensitive: true,
				Description: "The database password, or an object storage's secret key (write-only: never stored in " +
					"state, never readable back). Re-sent when password_wo_version changes (a new secret key restarts the " +
					"object storage) and when admin_console is turned on.",
			},
			"password_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for password_wo.",
			},
			"expose": schema.BoolAttribute{
				Optional: true,
				Description: "Expose the database externally (TLS, SNI-routed). An object storage gets an HTTPS S3 " +
					"address, path-style.",
			},
			"allowlist": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Client source CIDRs/IPs allowed when exposed. Empty = no IP restriction. It covers the " +
					"database's exposed address only, not pgAdmin or Redis Commander; for an object storage it covers " +
					"both its S3 address and its admin console's.",
			},
			"admin_console": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Description: "The admin console on its own HTTPS address: pgAdmin for Postgres, Redis Commander for " +
					"Redis, the RustFS console for object storage (it signs in with the access key and secret key, and " +
					"its address also answers S3 requests signed with them). pgAdmin and Redis Commander answer from any " +
					"network and sign in with `admin` and the database password (allowlist covers only the object storage " +
					"console). Left out, the console keeps the state it " +
					"has, also one switched on in the dashboard. Turning it on sends password_wo in the same apply, " +
					"since the platform needs it (a plan doing it without a new password_wo_version warns); for " +
					"object storage that restarts it for a few seconds.",
				PlanModifiers: []planmodifier.Bool{keepSizeWhenUnset{}},
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the database is up."},
			"endpoints": schema.ListNestedAttribute{
				Computed: true,
				Description: "Connection endpoints as reported by the platform (in-cluster and, when exposed, external; " +
					"an object storage's are s3 and s3-external).",
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
				Description: "Backups (Postgres only; object storage keeps one copy and has none). With the block, " +
					"backups are on; without it, off. " +
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
	withPlacement(resp.Schema.Attributes)
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
	AdminConsole      types.Bool          `tfsdk:"admin_console"`
	Backup            *storageBackupModel `tfsdk:"backup"`
	Ready             types.Bool          `tfsdk:"ready"`
	Endpoints         types.List          `tfsdk:"endpoints"`
	PlacementStrategy types.String        `tfsdk:"placement_strategy"`
	PlacementHost     types.String        `tfsdk:"placement_host"`
	PlacementRegion   types.String        `tfsdk:"placement_region"`
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
// keeps backups only with enabled: true, so the block says so. On an update
// with no backup configured it says they are off, since the platform keeps
// an explicit off and the write replaces the database's settings. Redis and
// object storage have no backups, so nothing is said about them.
func storageBackup(m storageModel, update bool) map[string]any {
	switch m.Engine.ValueString() {
	case "redis", "s3":
		return nil
	}
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
	// Sent whenever it is known, off included: a write replaces the
	// database's settings, so one left out would turn a console off.
	if !m.AdminConsole.IsNull() && !m.AdminConsole.IsUnknown() {
		spec["adminConsole"] = m.AdminConsole.ValueBool()
	}
	if b := storageBackup(m, update); b != nil {
		spec["backup"] = b
	}
	if pl := placementSpec(m.PlacementStrategy, m.PlacementHost, m.PlacementRegion); pl != nil {
		spec["placement"] = pl
	}
	return spec
}

// readStorageBackup reads the database's backups back into the block.
// Backups turned off (or on) elsewhere show as a change.
func readStorageBackup(state *storageModel, raw any) {
	b, _ := raw.(map[string]any)
	on, _ := b["enabled"].(bool)
	mode, _ := b["mode"].(string)
	var keep int64
	if v, ok := b["keepDays"].(float64); ok {
		keep = int64(v)
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

func (keepSizeWhenUnset) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
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

// storageUsername is the login name the platform reports for a database
// ("" when it has none).
func storageUsername(sp map[string]any) string {
	creds, _ := sp["credentials"].(map[string]any)
	v, _ := creds["username"].(string)
	return v
}

// storageAdminConsole is whether the platform runs the admin console (false
// when it says nothing).
func storageAdminConsole(sp map[string]any) types.Bool {
	on, _ := sp["adminConsole"].(bool)
	return types.BoolValue(on)
}

// storagePassword is the password an update sends: the configured one when
// password_wo_version changed, or when the admin console is being turned on
// (the platform needs the password in the same write: the console signs in
// with it); nothing otherwise.
func storagePassword(plan, state storageModel, configured string) string {
	if !plan.PasswordWOVersion.Equal(state.PasswordWOVersion) {
		return configured
	}
	if plan.AdminConsole.ValueBool() && !state.AdminConsole.ValueBool() {
		return configured
	}
	return ""
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
	var sp map[string]any
	if ws, err := c.Workloads(ctx); err == nil {
		if w := findWorkload(ws, m.Name.ValueString()); w != nil {
			sp = w.Storage
		}
	}
	fillStorageFromSpec(m, sp)
}

// fillStorageFromSpec resolves what a write left unknown from the database's
// settings on the platform (nil when it could not be read).
func fillStorageFromSpec(m *storageModel, sp map[string]any) {
	if sp != nil {
		readStorageSizes(m, sp)
		// A console left out on create is what the platform made.
		if m.AdminConsole.IsUnknown() {
			m.AdminConsole = storageAdminConsole(sp)
		}
		// A new database left without a username gets the platform's;
		// a planned one (known) must come back as planned.
		if v := storageUsername(sp); m.Username.IsUnknown() && v != "" {
			m.Username = types.StringValue(v)
		}
	}
	if m.Username.IsUnknown() {
		m.Username = types.StringNull()
	}
	if m.AdminConsole.IsUnknown() {
		m.AdminConsole = types.BoolValue(false)
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

var _ resource.ResourceWithModifyPlan = (*storageResource)(nil)

func (r *storageResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return // create or destroy
	}
	var plan, state storageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var configConsole types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("admin_console"), &configConsole)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if w := consoleAllowlistWarning(plan, state); w != nil {
		resp.Diagnostics.AddAttributeWarning(path.Root("allowlist"), w[0], w[1])
	} else if w := consoleOpenWarning(plan, configConsole); w != nil {
		resp.Diagnostics.AddAttributeWarning(path.Root("admin_console"), w[0], w[1])
	}
	if w := consolePasswordWarning(plan, state); w != nil {
		resp.Diagnostics.AddAttributeWarning(path.Root("password_wo"), w[0], w[1])
	}
}

// consoleOpenWarning warns when an object storage's console stays on although
// the configuration leaves admin_console out (switched on in the dashboard or
// through the API, or by a line since removed), with no allowlist and no
// expose: the console's address answers S3 requests from any network
// although the configuration never asked for a public address.
func consoleOpenWarning(plan storageModel, configConsole types.Bool) *[2]string {
	if plan.Engine.ValueString() != "s3" || !configConsole.IsNull() {
		return nil
	}
	if plan.AdminConsole.IsUnknown() || !plan.AdminConsole.ValueBool() || plan.Expose.ValueBool() {
		return nil
	}
	if !plan.Allowlist.IsNull() && (plan.Allowlist.IsUnknown() || len(plan.Allowlist.Elements()) > 0) {
		return nil
	}
	return &[2]string{"The object storage's console is on and open to any network",
		"Its console is on and stays on while the configuration leaves admin_console out. " +
			"Its address also answers S3 requests signed with the keys, from any network. Set admin_console = false " +
			"to turn it off, or set allowlist to limit it."}
}

// consolePasswordWarning warns when an apply turns the console on without a
// new password_wo_version: the platform needs the password to turn it on, so
// the apply sends password_wo, and one that is not the current password
// changes it without the plan showing it.
func consolePasswordWarning(plan, state storageModel) *[2]string {
	if plan.AdminConsole.IsUnknown() || !plan.AdminConsole.ValueBool() || state.AdminConsole.ValueBool() {
		return nil
	}
	if !plan.PasswordWOVersion.Equal(state.PasswordWOVersion) {
		return nil // a new password is sent on purpose
	}
	what := "the database's password"
	after := ""
	if plan.Engine.ValueString() == "s3" {
		what = "the object storage's secret key"
		after = "A new secret key restarts the object storage for a few seconds, and linked apps read it when they restart. "
	}
	return &[2]string{"Turning the console on sends password_wo",
		"The platform needs the password to turn the console on, so this apply sends password_wo. If it is not " +
			"the current password, the apply changes " + what + ". " + after + "Keep password_wo set to the " +
			"current password."}
}

// consoleAllowlistWarning warns when an apply removes an object storage's
// allowlist while its console stays on: admin_console left out keeps the
// console, allowlist left out removes the list, and the console's address
// answers S3 requests from then on from any network.
func consoleAllowlistWarning(plan, state storageModel) *[2]string {
	if plan.Engine.ValueString() != "s3" || plan.AdminConsole.IsUnknown() || !plan.AdminConsole.ValueBool() {
		return nil
	}
	if !plan.Allowlist.IsNull() || state.Allowlist.IsNull() || state.Allowlist.IsUnknown() || len(state.Allowlist.Elements()) == 0 {
		return nil
	}
	return &[2]string{"The object storage's allowlist will be removed",
		"Its console stays on and this apply removes its allowlist, so the console's address (which also answers " +
			"S3 requests) and, when exposed, its S3 address answer from any network. Set allowlist to keep the list, " +
			"or allowlist = [] to open it on purpose."}
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
	if m.Engine.IsUnknown() || m.Engine.IsNull() {
		return errs
	}
	switch m.Engine.ValueString() {
	case "s3":
		if m.Backup != nil {
			errs = append(errs, [2]string{"Object storage has no backups yet",
				"It keeps one copy of your files. Remove the backup settings."})
		}
		if !m.Instances.IsUnknown() && m.Instances.ValueInt64() > 1 {
			errs = append(errs, [2]string{"Object storage runs as one server",
				"A second copy isn't offered yet. Set instances = 1 or leave it out."})
		}
		if v := m.Version; !v.IsUnknown() && !v.IsNull() && v.ValueString() != "1" {
			errs = append(errs, [2]string{"Object storage runs version 1",
				fmt.Sprintf("Version %q isn't offered. Set version = \"1\" or leave it out.", v.ValueString())})
		}
		if p := m.PasswordWO; !p.IsUnknown() && !p.IsNull() && p.ValueString() != strings.TrimSpace(p.ValueString()) {
			errs = append(errs, [2]string{"The secret key can't start or end with a space, tab or line break",
				"Object storage drops them from around its keys, so the server and its apps would disagree. Remove them " +
					"(a key read from a file often ends with a line break: trimspace(file(...)) drops it)."})
		}
	case "redis":
		if m.Backup != nil {
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
	imported := false
	if b, d := req.Private.GetKey(ctx, importedKey); !d.HasError() && len(b) > 0 {
		imported = true
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, nil)...)
	}
	readStorage(&state, w.Storage, imported)
	refreshStorageStatus(ctx, r.data.Client, &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// readStorage fills the state from the database's settings on the platform.
// A first read after an import takes object storage's version as the platform
// holds it, as for any database; a version it has not stored (one made
// through the API without one) reads back as left out.
func readStorage(state *storageModel, sp map[string]any, imported bool) {
	if v, ok := sp["engine"].(string); ok {
		state.Engine = types.StringValue(v)
	}
	if v, ok := sp["version"].(string); ok && v != "" {
		if state.Engine.ValueString() == "s3" && !imported {
			// Object storage's one version reads back as left out where the
			// configuration left it out.
			state.Version = readDefaulted(state.Version, v, "1")
		} else {
			state.Version = types.StringValue(v)
		}
	}
	readStorageSizes(state, sp)
	if v, ok := sp["instances"].(float64); ok && v > 0 {
		state.Instances = types.Int64Value(int64(v))
	}
	if v := storageUsername(sp); v != "" {
		state.Username = types.StringValue(v)
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
	readStorageBackup(state, sp["backup"])
	state.AdminConsole = storageAdminConsole(sp)
	refreshPlacement(sp, &state.PlacementStrategy, &state.PlacementHost, &state.PlacementRegion)
}

func (r *storageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state storageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	password := storagePassword(plan, state, cfg.PasswordWO.ValueString())
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
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, []byte(`true`))...)
}
