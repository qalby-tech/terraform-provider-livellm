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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_vm — a machine: an Ubuntu terminal or desktop, a Debian or Fedora
// server, or Windows (11 or Server Core), with ports, network gating,
// placement and stop-without-destroy.
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
		Description: "A machine — an Ubuntu terminal or desktop, a Debian or Fedora server, or Windows 11 or Windows Server. " +
			"The password is write-only and a Linux machine can carry SSH keys of its own; exposed ports get public HTTPS " +
			"hostnames; stopped keeps the disk while halting the machine, and stop_after has the platform do that for you.",
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
			"os": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("ubuntu"),
				Description: "The system: ubuntu (24.04, default), debian (13), fedora (44) or windows. Debian and Fedora are servers only " +
					"(desktop = false); windows installs windows_edition. Changing it replaces the VM.",
				Validators: []validator.String{stringvalidator.OneOf("ubuntu", "debian", "fedora", "windows")},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"windows_edition": schema.StringAttribute{
				Optional: true,
				Computed: true,
				Description: "With os = \"windows\": desktop (Windows 11 Pro, the default) or server (Windows Server 2025, " +
					"Server Core: a command line and no desktop). Windows installs itself on first start, which takes 15 to 35 " +
					"minutes. Changing it replaces the VM.",
				Validators: []validator.String{stringvalidator.OneOf("desktop", "server")},
				PlanModifiers: []planmodifier.String{
					windowsEditionDefault{},
					stringplanmodifier.RequiresReplace(),
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
				Description: "Root disk in GiB. Windows needs at least 64 (and gets 64 when this is left out).",
			},
			"username": schema.StringAttribute{
				Required: true,
				Description: "The login's username (SSH on Linux, the administrator on Windows, which can't be \"Administrator\"). " +
					"Changing it replaces the VM (the login is baked at first boot).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"password_wo": schema.StringAttribute{
				Required:    true,
				WriteOnly:   true,
				Sensitive:   true,
				Description: "The login's password, at least 8 characters (write-only: never stored in state). Re-sent when password_wo_version changes.",
			},
			"password_wo_version": schema.Int64Attribute{
				Required:    true,
				Description: "Rotation trigger for password_wo.",
			},
			"ssh_keys": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "SSH public keys for this machine alone, one .pub line each. They are installed for " +
					"username next to the workspace's own keys, and a change reaches a running machine within a " +
					"minute or two. Leave it out to keep whatever the machine has; the platform keeps a machine's " +
					"last key, so replace a key rather than emptying the list. Linux only.",
				PlanModifiers: []planmodifier.List{
					listplanmodifier.UseStateForUnknown(),
				},
			},
			"stopped": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "Halt the VM without destroying it — the disk is kept and billing drops to disk-only.",
			},
			"stop_after": schema.StringAttribute{
				Optional: true,
				Description: "Have the platform stop this machine after a while, so one made for a single job doesn't " +
					"run forever: a length of time such as \"4h\", \"90m\" or \"2h30m\" (a minute to 30 days). " +
					"The clock starts when Terraform creates the machine, starts it again, or when this value " +
					"changes — not on every apply. Stopping keeps the disk. Once the platform has stopped the " +
					"machine the next apply starts it for another stop_after, unless you set stopped = true.",
			},
			"expires_at": schema.StringAttribute{
				Computed:    true,
				Description: "When the platform will stop the machine (RFC 3339). Empty when it has no stop time.",
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
						"udp":  schema.BoolAttribute{Computed: true},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"backup": schema.SingleNestedBlock{
				Description: "Scheduled backups of the machine's disk. Without the block, none are scheduled " +
					"(backups taken by hand are kept either way). A backup restores in place, with the machine stopped.",
				Attributes: map[string]schema.Attribute{
					// Both are needed whenever the block is there (ValidateConfig).
					"schedule": schema.StringAttribute{
						Optional: true,
						Description: "When to back up: @hourly, @daily, @weekly, @monthly or a 5-field cron expression (UTC). " +
							"Required inside a backup block.",
					},
					"keep": schema.Int64Attribute{
						Optional: true,
						Description: "How many scheduled backups are kept, 1..100: the newest ones. A count, unlike a " +
							"database's keep_days. Required inside a backup block.",
						Validators: []validator.Int64{int64validator.Between(1, 100)},
					},
				},
			},
			"port": schema.ListNestedBlock{
				Description: "Exposed ports. HTTP ports get a public HTTPS hostname; tcp/udp ports get a raw address.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "Port name (part of the hostname for HTTP ports)."},
						"port": schema.Int64Attribute{Required: true, Description: "Listener port inside the VM."},
						"tcp":  schema.BoolAttribute{Optional: true, Description: "Expose as a raw TCP address instead of HTTPS."},
						"udp":  schema.BoolAttribute{Optional: true, Description: "Expose as a raw UDP address."},
						"internal": schema.BoolAttribute{
							Optional:    true,
							Description: "No public address and no node port: reachable from inside the workspace only, at <workspace>-<name>-internal:<port>.",
						},
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
	Name     types.String `tfsdk:"name"`
	Port     types.Int64  `tfsdk:"port"`
	TCP      types.Bool   `tfsdk:"tcp"`
	UDP      types.Bool   `tfsdk:"udp"`
	Internal types.Bool   `tfsdk:"internal"`
}

type vmResourceModel struct {
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
	Name              types.String   `tfsdk:"name"`
	Desktop           types.Bool     `tfsdk:"desktop"`
	OS                types.String   `tfsdk:"os"`
	WindowsEdition    types.String   `tfsdk:"windows_edition"`
	CPUs              types.Int64    `tfsdk:"cpus"`
	MemoryGi          types.Int64    `tfsdk:"memory_gi"`
	DiskGi            types.Int64    `tfsdk:"disk_gi"`
	Username          types.String   `tfsdk:"username"`
	PasswordWO        types.String   `tfsdk:"password_wo"`
	PasswordWOVersion types.Int64    `tfsdk:"password_wo_version"`
	SSHKeys           types.List     `tfsdk:"ssh_keys"`
	Stopped           types.Bool     `tfsdk:"stopped"`
	StopAfter         types.String   `tfsdk:"stop_after"`
	ExpiresAt         types.String   `tfsdk:"expires_at"`
	AllowCIDRs        types.List     `tfsdk:"allow_cidrs"`
	Port              types.List     `tfsdk:"port"`
	Backup            *vmBackupModel `tfsdk:"backup"`
	PlacementStrategy types.String   `tfsdk:"placement_strategy"`
	PlacementHost     types.String   `tfsdk:"placement_host"`
	PlacementRegion   types.String   `tfsdk:"placement_region"`
	Ready             types.Bool     `tfsdk:"ready"`
	SSH               types.String   `tfsdk:"ssh"`
	URL               types.String   `tfsdk:"url"`
	Endpoints         types.List     `tfsdk:"endpoints"`
}

// vmBackupModel is the backup block: a schedule and how many to keep.
type vmBackupModel struct {
	Schedule types.String `tfsdk:"schedule"`
	Keep     types.Int64  `tfsdk:"keep"`
}

func (r *vmResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg vmResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range vmConfigErrors(cfg) {
		resp.Diagnostics.AddAttributeError(path.Root("backup"), e[0], e[1])
	}
	for _, e := range windowsConfigErrors(cfg) {
		resp.Diagnostics.AddAttributeError(path.Root(e[0]), e[1], e[2])
	}
}

// windowsMinDiskGi is the smallest disk Windows installs onto.
const windowsMinDiskGi = 64

// windowsConfigErrors are the platform's rules for a Windows machine, at plan
// time: {attribute, summary, detail}.
func windowsConfigErrors(m vmResourceModel) [][3]string {
	var errs [][3]string
	win := m.OS.ValueString() == "windows"
	if !win {
		if !m.WindowsEdition.IsNull() && !m.WindowsEdition.IsUnknown() && !m.OS.IsUnknown() {
			errs = append(errs, [3]string{"windows_edition", "windows_edition is for Windows",
				"Set os = \"windows\" to install Windows, or leave windows_edition out."})
		}
		return errs
	}
	if m.Desktop.ValueBool() {
		errs = append(errs, [3]string{"desktop", "desktop is for Ubuntu",
			"A Windows machine's desktop comes from windows_edition = \"desktop\" (the default); leave desktop out."})
	}
	if !m.DiskGi.IsNull() && !m.DiskGi.IsUnknown() && m.DiskGi.ValueInt64() < windowsMinDiskGi {
		errs = append(errs, [3]string{"disk_gi", "Disk too small for Windows",
			fmt.Sprintf("Windows needs a disk of at least %d GiB; disk_gi is %d.", windowsMinDiskGi, m.DiskGi.ValueInt64())})
	}
	if strings.EqualFold(m.Username.ValueString(), "Administrator") {
		errs = append(errs, [3]string{"username", "Username taken by Windows",
			"\"Administrator\" is Windows' own built-in account. Pick another name; it becomes an administrator."})
	}
	if !m.SSHKeys.IsNull() && !m.SSHKeys.IsUnknown() {
		errs = append(errs, [3]string{"ssh_keys", "SSH keys are for Linux machines",
			"A Windows machine is opened with its username and password, in the console or with Remote Desktop. Leave ssh_keys out."})
	}
	return errs
}

// windowsEditionDefault plans the edition the platform installs when it is
// left out: desktop on Windows, and nothing on Linux.
type windowsEditionDefault struct{}

func (windowsEditionDefault) Description(context.Context) string {
	return "desktop on Windows when left out; null otherwise"
}
func (d windowsEditionDefault) MarkdownDescription(ctx context.Context) string {
	return d.Description(ctx)
}
func (windowsEditionDefault) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	var os types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("os"), &os)...)
	switch {
	case os.IsUnknown():
		resp.PlanValue = types.StringUnknown()
	case os.ValueString() == "windows":
		resp.PlanValue = types.StringValue("desktop")
	default:
		resp.PlanValue = types.StringNull()
	}
}

// vmConfigErrors are the platform's checks on the backup block, at plan time.
func vmConfigErrors(m vmResourceModel) [][2]string {
	b := m.Backup
	if b == nil {
		return nil
	}
	var errs [][2]string
	if b.Schedule.IsNull() || (!b.Schedule.IsUnknown() && strings.TrimSpace(b.Schedule.ValueString()) == "") {
		errs = append(errs, [2]string{"Backup schedule missing",
			"A backup block needs a schedule: @hourly, @daily, @weekly, @monthly or a 5-field cron expression."})
	} else if !b.Schedule.IsUnknown() && !validBackupSchedule(b.Schedule.ValueString()) {
		errs = append(errs, [2]string{"Backup schedule not understood",
			fmt.Sprintf("%q isn't @hourly, @daily, @weekly, @monthly or a 5-field cron expression.", b.Schedule.ValueString())})
	}
	if b.Keep.IsNull() {
		errs = append(errs, [2]string{"Backup keep missing", "A backup block needs keep: how many scheduled backups to keep, 1..100."})
	}
	return errs
}

// validBackupSchedule matches what the platform accepts: a macro or five
// cron fields.
func validBackupSchedule(s string) bool {
	switch s {
	case "@hourly", "@daily", "@weekly", "@monthly":
		return true
	}
	return len(strings.Fields(s)) == 5
}

// readVMBackup reads the machine's backup schedule back; one set or removed
// elsewhere shows as a change.
func readVMBackup(raw any) *vmBackupModel {
	b, _ := raw.(map[string]any)
	sched, _ := b["schedule"].(string)
	if sched == "" {
		return nil
	}
	m := &vmBackupModel{Schedule: types.StringValue(sched), Keep: types.Int64Null()}
	if v, ok := b["keep"].(float64); ok && v > 0 {
		m.Keep = types.Int64Value(int64(v))
	}
	return m
}

func (m vmResourceModel) workloadType() string {
	if m.OS.ValueString() == "windows" {
		return "vm-windows"
	}
	if m.Desktop.ValueBool() {
		return "vm-ubuntu-desktop"
	}
	return "vm-ubuntu"
}

// defaultWait is how long an apply waits for the machine when timeouts
// doesn't say: Windows installs itself on first start.
func (m vmResourceModel) defaultWait() time.Duration {
	if m.OS.ValueString() == "windows" {
		return 45 * time.Minute
	}
	return 15 * time.Minute
}

// vmWrite is what one write carries besides the machine's settings: the
// things the platform takes as instructions rather than keeps as state.
type vmWrite struct {
	// password is the login's new password; empty leaves the login alone.
	password string
	// stopAfter starts the stop clock ("4h"), or clears it ("off"); empty
	// leaves the stop time where it is.
	stopAfter string
	// create says this is the create body, where a machine's keys travel
	// inside credentials.
	create bool
}

// normalizeKey is a public key line the way the platform keeps it: single
// spaces, no padding.
func normalizeKey(line string) string { return strings.Join(strings.Fields(line), " ") }

// configuredKeys is the machine's own keys as configured, or nil when the
// configuration leaves them alone.
func configuredKeys(ctx context.Context, l types.List) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var keys []string
	l.ElementsAs(ctx, &keys, false)
	return keys
}

// stopAfterToSend decides whether this update starts the stop clock: only when
// stop_after changed or the machine is being started again, never on an apply
// that merely touches something else — that would push the stop time back
// every time. Removing stop_after clears the stop time.
func stopAfterToSend(plan, state vmResourceModel) string {
	if plan.StopAfter.IsNull() {
		if !state.StopAfter.IsNull() {
			return "off"
		}
		return ""
	}
	if plan.Stopped.ValueBool() {
		return "" // a stopped machine has no clock to start
	}
	starting := state.Stopped.ValueBool() && !plan.Stopped.ValueBool()
	if starting || !plan.StopAfter.Equal(state.StopAfter) {
		return plan.StopAfter.ValueString()
	}
	return ""
}

// vmSpec builds the vm kind block.
func vmSpec(ctx context.Context, m vmResourceModel, wr vmWrite) map[string]any {
	spec := map[string]any{}
	// Ubuntu is the default the platform assumes; only another Linux is sent.
	// Windows is a type of its own, with its edition.
	switch os := m.OS.ValueString(); os {
	case "", "ubuntu":
	case "windows":
		edition := m.WindowsEdition.ValueString()
		if edition == "" {
			edition = "desktop"
		}
		spec["windowsEdition"] = edition
	default:
		spec["os"] = os
	}
	if !m.CPUs.IsNull() {
		spec["cpus"] = m.CPUs.ValueInt64()
	}
	if !m.MemoryGi.IsNull() {
		spec["memory"] = fmt.Sprintf("%dGi", m.MemoryGi.ValueInt64())
	}
	if !m.DiskGi.IsNull() {
		spec["storageSize"] = fmt.Sprintf("%dGi", m.DiskGi.ValueInt64())
	}
	keys := configuredKeys(ctx, m.SSHKeys)
	if wr.password != "" {
		creds := map[string]any{
			"username": m.Username.ValueString(),
			"password": wr.password,
		}
		if wr.create && len(keys) > 0 {
			creds["sshKeys"] = keys
		}
		spec["credentials"] = creds
	}
	// After create the keys live on the machine itself. An empty list is never
	// sent: the platform reads that as "keep what it has".
	if !wr.create && len(keys) > 0 {
		spec["sshKeys"] = keys
	}
	if wr.stopAfter != "" {
		spec["stopAfter"] = wr.stopAfter
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
			if p.Internal.ValueBool() {
				e["internal"] = true
			}
			out = append(out, e)
		}
		spec["ports"] = out
	}
	if b := m.Backup; b != nil && b.Schedule.ValueString() != "" {
		spec["backup"] = map[string]any{"schedule": b.Schedule.ValueString(), "keep": b.Keep.ValueInt64()}
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
			UDP:  types.BoolValue(e.UDP),
		})
	}
	m.URL = types.StringValue(url)
	list, d := types.ListValueFrom(ctx, epType, eps)
	diags.Append(d...)
	m.Endpoints = list
}

// refreshVMSpec reads back what the platform decides: when the machine will
// stop, and its keys when the configuration leaves them alone.
func refreshVMSpec(ctx context.Context, c *client.Client, m *vmResourceModel) {
	var w *client.Workload
	if ws, err := c.Workloads(ctx); err == nil {
		w = findWorkload(ws, m.Name.ValueString())
	}
	if w == nil {
		m.ExpiresAt = types.StringValue("")
		if m.SSHKeys.IsUnknown() {
			m.SSHKeys = types.ListValueMust(types.StringType, []attr.Value{})
		}
		return
	}
	m.ExpiresAt = types.StringValue(w.ExpiresAt)
	if m.SSHKeys.IsUnknown() {
		m.SSHKeys = keyList(remoteKeys(w.VM))
	}
}

// remoteKeys is the machine's own keys as the platform keeps them.
func remoteKeys(vm map[string]any) []string {
	raw, _ := vm["sshKeys"].([]any)
	out := make([]string, 0, len(raw))
	for _, k := range raw {
		if s, ok := k.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func keyList(keys []string) types.List {
	vals := make([]attr.Value, 0, len(keys))
	for _, k := range keys {
		vals = append(vals, types.StringValue(k))
	}
	return types.ListValueMust(types.StringType, vals)
}

// sameKeys says whether two lists hold the same keys in the same order, however
// they are spaced.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if normalizeKey(a[i]) != normalizeKey(b[i]) {
			return false
		}
	}
	return true
}

// ModifyPlan refuses the one change the platform can't make, and keeps
// expires_at quiet on an apply that doesn't move it.
func (r *vmResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return // create or destroy
	}
	var plan, state vmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if keys := configuredKeys(ctx, plan.SSHKeys); keys != nil && len(keys) == 0 && len(state.SSHKeys.Elements()) > 0 {
		resp.Diagnostics.AddAttributeError(path.Root("ssh_keys"), "A machine's keys can't be emptied",
			"The platform keeps the last keys a machine was given, so an empty list would never take effect. "+
				"Replace the key with another one, or leave ssh_keys out to stop managing it here.")
		return
	}
	if stopAfterToSend(plan, state) == "" && plan.Stopped.Equal(state.Stopped) && !state.ExpiresAt.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("expires_at"), state.ExpiresAt)...)
	}
}

func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg vmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wr := vmWrite{password: cfg.PasswordWO.ValueString(), create: true}
	if !plan.Stopped.ValueBool() {
		wr.stopAfter = plan.StopAfter.ValueString()
	}
	body := vmSpec(ctx, plan, wr)
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, plan.workloadType(), body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create VM", err)
		return
	}
	if plan.Stopped.ValueBool() {
		// Born stopped: create, then immediately halt (stopped lives on the
		// workload, not the create body).
		w := client.Workload{ID: plan.Name.ValueString(), Type: plan.workloadType(), Stopped: true, VM: vmSpec(ctx, plan, vmWrite{})}
		if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
			apiDiag(&resp.Diagnostics, "Cannot stop VM after create", err)
		}
	}
	createTimeout, td := plan.Timeouts.Create(ctx, plan.defaultWait())
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), plan.Stopped.ValueBool()); err != nil {
		resp.Diagnostics.AddError("VM did not become ready", err.Error())
	}
	plan.PasswordWO = types.StringNull()
	refreshVMSpec(ctx, r.data.Client, &plan)
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
	state.WindowsEdition = types.StringNull()
	if w.Type == "vm-windows" {
		state.OS = types.StringValue("windows")
		edition, _ := w.VM["windowsEdition"].(string)
		if edition == "" {
			edition = "desktop"
		}
		state.WindowsEdition = types.StringValue(edition)
	} else if os, _ := w.VM["os"].(string); os != "" {
		state.OS = types.StringValue(os)
	} else {
		state.OS = types.StringValue("ubuntu")
	}
	state.Stopped = types.BoolValue(w.Stopped)
	state.ExpiresAt = types.StringValue(w.ExpiresAt)
	// Keep the keys as they were written unless the machine holds other ones.
	if remote := remoteKeys(w.VM); state.SSHKeys.IsNull() || !sameKeys(configuredKeys(ctx, state.SSHKeys), remote) {
		state.SSHKeys = keyList(remote)
	}
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
	state.Backup = readVMBackup(sp["backup"])
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
		VM:      vmSpec(ctx, plan, vmWrite{password: password, stopAfter: stopAfterToSend(plan, state)}),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update VM", err)
		return
	}
	createTimeout, td := plan.Timeouts.Create(ctx, plan.defaultWait())
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, plan.Stopped.ValueBool()); err != nil {
		resp.Diagnostics.AddError("VM did not become ready after update", err.Error())
	}
	plan.PasswordWO = types.StringNull()
	refreshVMSpec(ctx, r.data.Client, &plan)
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
