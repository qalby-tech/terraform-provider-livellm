package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_browser_api — one address for many browsers. A call that names no
// browser goes to the one with the fewest open tabs, a session stays on the
// browser it started on, and /browsers/<name>/… or X-Browser-Id picks one.
// On the platform it is a workload of type "controller".
type browserAPIResource struct {
	data *providerData
}

func NewBrowserAPIResource() resource.Resource {
	return &browserAPIResource{}
}

func (r *browserAPIResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_browser_api"
}

func (r *browserAPIResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Browser API: one address that drives several browsers. A call that names no browser goes to the " +
			"one with the fewest open tabs; a session stays on its browser; /browsers/<name>/… or the X-Browser-Id " +
			"header picks one. A browser belongs to at most one Browser API.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Resource id; it is part of the address. Changing it replaces the Browser API.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"browsers": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "The workspace browsers it drives, by name (livellm_browser.x.name). A browser can be in " +
					"one Browser API only. Leave it out with all_browsers = true.",
			},
			"all_browsers": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				Description: "Drive every browser in the workspace, including ones made later. No other Browser API " +
					"can then drive a workspace browser. Can't be combined with browsers.",
			},
			"remote_auth_version": schema.Int64Attribute{
				Optional: true,
				Description: "Change it to send the remote_browser auth_wo values again when nothing else " +
					"changed (they are write-only, so a new value alone makes no plan).",
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU request, e.g. \"500m\".",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory request, e.g. \"1Gi\".",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the Browser API is up."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"remote_browser": schema.ListNestedBlock{
				Description: "A browser running somewhere else, reached at its CDP websocket address. " +
					"A remote browser may be in several Browser APIs.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						// Needed whenever the block is there (ValidateConfig); see
						// container_app's source.git.url for why they aren't Required.
						"id": schema.StringAttribute{
							Optional:    true,
							Description: "The name calls use for it (X-Browser-Id, /browsers/<id>/…). Lowercase letters, digits and dashes; not the name of a workspace browser.",
						},
						"ws_url": schema.StringAttribute{
							Optional:    true,
							Description: "Its CDP websocket address, ws:// or wss://.",
						},
						"auth_wo": schema.StringAttribute{
							Optional:  true,
							WriteOnly: true,
							Sensitive: true,
							Description: "A header the remote browser needs, write-only: never stored in state. " +
								"\"Name: value\" sends that header; anything else (\"Bearer abc\") is sent as Authorization. " +
								"Sent on every apply, and leaving it out removes the header; bump remote_auth_version to send a new value on its own.",
						},
					},
				},
			},
		},
	}
}

func (r *browserAPIResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type remoteBrowserModel struct {
	ID     types.String `tfsdk:"id"`
	WsURL  types.String `tfsdk:"ws_url"`
	AuthWO types.String `tfsdk:"auth_wo"`
}

var remoteBrowserAttrTypes = map[string]attr.Type{
	"id":      types.StringType,
	"ws_url":  types.StringType,
	"auth_wo": types.StringType,
}

type browserAPIModel struct {
	Name              types.String   `tfsdk:"name"`
	Browsers          types.Set      `tfsdk:"browsers"`
	AllBrowsers       types.Bool     `tfsdk:"all_browsers"`
	RemoteBrowser     types.List     `tfsdk:"remote_browser"`
	RemoteAuthVersion types.Int64    `tfsdk:"remote_auth_version"`
	CPU               types.String   `tfsdk:"cpu"`
	Memory            types.String   `tfsdk:"memory"`
	Ready             types.Bool     `tfsdk:"ready"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
}

func (m browserAPIModel) browserNames(ctx context.Context) []string {
	if m.Browsers.IsNull() || m.Browsers.IsUnknown() {
		return nil
	}
	var names []string
	m.Browsers.ElementsAs(ctx, &names, false)
	sort.Strings(names)
	return names
}

func (m browserAPIModel) remotes(ctx context.Context) []remoteBrowserModel {
	if m.RemoteBrowser.IsNull() || m.RemoteBrowser.IsUnknown() {
		return nil
	}
	var out []remoteBrowserModel
	m.RemoteBrowser.ElementsAs(ctx, &out, false)
	return out
}

// browserAPISpec is the controller block the API takes. auth holds the
// write-only headers by remote id, read from the configuration: they are
// never in the plan or the state, and the platform never sends them back
// (it answers hasAuth instead).
func browserAPISpec(ctx context.Context, m browserAPIModel, auth map[string]string) map[string]any {
	spec := map[string]any{}
	if m.AllBrowsers.ValueBool() {
		// autodiscover is always sent, false included: a Browser API that
		// names its browsers must not fall back to every browser.
		spec["autodiscover"] = true
	} else {
		spec["autodiscover"] = false
		names := m.browserNames(ctx)
		if names == nil {
			names = []string{}
		}
		spec["browsers"] = names
	}
	remotes := []map[string]any{}
	for _, rb := range m.remotes(ctx) {
		e := map[string]any{"id": rb.ID.ValueString(), "wsUrl": rb.WsURL.ValueString()}
		if a := auth[rb.ID.ValueString()]; a != "" {
			e["authHeader"] = a
		} else {
			// The platform keeps a stored header unless told otherwise; the
			// configuration has none for this remote, so it has none.
			e["hasAuth"] = false
		}
		remotes = append(remotes, e)
	}
	spec["externalBrowsers"] = remotes
	if v := m.CPU.ValueString(); v != "" {
		spec["cpu"] = v
	}
	if v := m.Memory.ValueString(); v != "" {
		spec["memory"] = v
	}
	return spec
}

// remoteAuth reads the write-only auth_wo values from the configuration.
func remoteAuth(ctx context.Context, cfg browserAPIModel) map[string]string {
	out := map[string]string{}
	for _, rb := range cfg.remotes(ctx) {
		if v := rb.AuthWO.ValueString(); v != "" {
			out[rb.ID.ValueString()] = v
		}
	}
	return out
}

var browserNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var wsURLRe = regexp.MustCompile(`^wss?://[^\s/]+`)

// browserAPIConfigErrors: what the configuration needs before any call.
// Unknown values (names from resources not created yet) are left to apply.
func browserAPIConfigErrors(ctx context.Context, m browserAPIModel) [][2]string {
	var out [][2]string
	all := !m.AllBrowsers.IsNull() && !m.AllBrowsers.IsUnknown() && m.AllBrowsers.ValueBool()
	hasBrowsers := !m.Browsers.IsNull() && (m.Browsers.IsUnknown() || len(m.Browsers.Elements()) > 0)
	hasRemotes := !m.RemoteBrowser.IsNull() && (m.RemoteBrowser.IsUnknown() || len(m.RemoteBrowser.Elements()) > 0)
	if all && hasBrowsers {
		out = append(out, [2]string{"Conflicting all_browsers and browsers",
			"all_browsers = true already drives every browser in the workspace; remove browsers, or set all_browsers = false."})
	}
	if !all && !hasBrowsers && !hasRemotes && !m.AllBrowsers.IsUnknown() {
		out = append(out, [2]string{"A Browser API needs browsers",
			"Name its browsers in browsers, set all_browsers = true, or add a remote_browser block."})
	}
	local := map[string]bool{}
	for _, n := range m.browserNames(ctx) {
		local[n] = true
	}
	seen := map[string]bool{}
	for _, rb := range m.remotes(ctx) {
		if rb.ID.IsUnknown() || rb.WsURL.IsUnknown() {
			continue
		}
		id := rb.ID.ValueString()
		switch {
		case id == "" || rb.WsURL.ValueString() == "":
			out = append(out, [2]string{"Incomplete remote_browser", "A remote_browser block needs an id and a ws_url."})
			continue
		case !browserNameRe.MatchString(id) || len(id) > 63:
			out = append(out, [2]string{"Bad remote_browser id", fmt.Sprintf("%q: use lowercase letters, digits and dashes, starting and ending with a letter or digit.", id)})
		case local[id]:
			out = append(out, [2]string{"Remote browser named like a workspace browser", fmt.Sprintf("%q is already a browser in browsers; give the remote one another id.", id)})
		case seen[id]:
			out = append(out, [2]string{"Remote browser id used twice", fmt.Sprintf("Two remote_browser blocks are called %q.", id)})
		}
		seen[id] = true
		if !wsURLRe.MatchString(rb.WsURL.ValueString()) {
			out = append(out, [2]string{"Bad remote_browser ws_url", fmt.Sprintf("%q should start with ws:// or wss://.", rb.WsURL.ValueString())})
		}
	}
	return out
}

func (r *browserAPIResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg browserAPIModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range browserAPIConfigErrors(ctx, cfg) {
		resp.Diagnostics.AddError(e[0], e[1])
	}
}

// readBrowserAPI maps the platform's controller block back into the model.
// A remote browser's header is write-only and never read back.
func readBrowserAPI(prev browserAPIModel, sp map[string]any) browserAPIModel {
	m := prev
	all, _ := sp["autodiscover"].(bool)
	m.AllBrowsers = types.BoolValue(all)
	raw, _ := sp["browsers"].([]any)
	if len(raw) > 0 {
		vals := make([]attr.Value, 0, len(raw))
		for _, b := range raw {
			if s, ok := b.(string); ok {
				vals = append(vals, types.StringValue(s))
			}
		}
		m.Browsers = types.SetValueMust(types.StringType, vals)
	} else if prev.Browsers.IsNull() || prev.Browsers.IsUnknown() {
		m.Browsers = types.SetNull(types.StringType)
	} else {
		m.Browsers = types.SetValueMust(types.StringType, []attr.Value{})
	}
	ext, _ := sp["externalBrowsers"].([]any)
	objType := types.ObjectType{AttrTypes: remoteBrowserAttrTypes}
	if len(ext) == 0 && (prev.RemoteBrowser.IsNull() || prev.RemoteBrowser.IsUnknown()) {
		m.RemoteBrowser = types.ListNull(objType)
	} else {
		vals := make([]attr.Value, 0, len(ext))
		for _, e := range ext {
			em, ok := e.(map[string]any)
			if !ok {
				continue
			}
			id, _ := em["id"].(string)
			ws, _ := em["wsUrl"].(string)
			vals = append(vals, types.ObjectValueMust(remoteBrowserAttrTypes, map[string]attr.Value{
				"id":      types.StringValue(id),
				"ws_url":  types.StringValue(ws),
				"auth_wo": types.StringNull(),
			}))
		}
		m.RemoteBrowser = types.ListValueMust(objType, vals)
	}
	m.CPU = readOptional(prev.CPU, sp["cpu"])
	m.Memory = readOptional(prev.Memory, sp["memory"])
	return m
}

// readOptional keeps an attribute the configuration left out unset when the
// platform has nothing for it.
func readOptional(prev types.String, raw any) types.String {
	if s, _ := raw.(string); s != "" {
		return types.StringValue(s)
	}
	if prev.IsNull() || prev.IsUnknown() {
		return types.StringNull()
	}
	return types.StringValue("")
}

// nullAuth clears the write-only values before the model goes into state.
func nullAuth(ctx context.Context, m *browserAPIModel) {
	remotes := m.remotes(ctx)
	if remotes == nil {
		return
	}
	vals := make([]attr.Value, 0, len(remotes))
	for _, rb := range remotes {
		vals = append(vals, types.ObjectValueMust(remoteBrowserAttrTypes, map[string]attr.Value{
			"id": rb.ID, "ws_url": rb.WsURL, "auth_wo": types.StringNull(),
		}))
	}
	m.RemoteBrowser = types.ListValueMust(types.ObjectType{AttrTypes: remoteBrowserAttrTypes}, vals)
}

func (r *browserAPIResource) waitAndRefresh(ctx context.Context, m *browserAPIModel, timeout time.Duration, what string, diags interface {
	AddError(string, string)
}) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, m.Name.ValueString(), false); err != nil {
		diags.AddError(what, err.Error())
	}
	st, _ := statusOf(ctx, r.data.Client, m.Name.ValueString())
	m.Ready = types.BoolValue(st != nil && st.Ready)
}

func (r *browserAPIResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg browserAPIModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := browserAPISpec(ctx, plan, remoteAuth(ctx, cfg))
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "controller", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create Browser API", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	r.waitAndRefresh(ctx, &plan, createTimeout, "Browser API did not become ready", &resp.Diagnostics)
	nullAuth(ctx, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserAPIResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state browserAPIModel
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
	if w == nil || w.Type != "controller" {
		resp.State.RemoveResource(ctx)
		return
	}
	state = readBrowserAPI(state, w.Controller)
	st, _ := statusOf(ctx, r.data.Client, state.Name.ValueString())
	state.Ready = types.BoolValue(st != nil && st.Ready)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *browserAPIResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg browserAPIModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w := client.Workload{
		ID:         plan.Name.ValueString(),
		Type:       "controller",
		Controller: browserAPISpec(ctx, plan, remoteAuth(ctx, cfg)),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update Browser API", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	r.waitAndRefresh(ctx, &plan, createTimeout, "Browser API did not become ready after update", &resp.Diagnostics)
	nullAuth(ctx, &plan)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserAPIResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state browserAPIModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete Browser API", err)
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

func (r *browserAPIResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
