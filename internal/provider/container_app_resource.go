package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// Platform-side defaults for the optional build fields. The API may echo them
// back on read; a config that left the field unset must not see a diff.
const (
	defaultDockerfile = "Dockerfile"
	defaultContext    = "."
)

// livellm_container_app — a container app from a prebuilt image or from a Git
// repo with a Dockerfile that the platform builds into an image.
type containerAppResource struct {
	data *providerData
}

func NewContainerAppResource() resource.Resource {
	return &containerAppResource{}
}

func (r *containerAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_app"
}

func (r *containerAppResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A container app: run a prebuilt image, or give the platform a Git repo with a Dockerfile " +
			"and it builds the image for you (rebuild any time from the dashboard or the API). " +
			"Exposed ports get public HTTPS hostnames.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the app.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"image": schema.StringAttribute{
				Optional:    true,
				Description: "Prebuilt image reference. Set image or a source block, not both.",
			},
			"command": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Container entrypoint override.",
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU request, e.g. \"500m\".",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory request, e.g. \"512Mi\".",
			},
			"stack": schema.StringAttribute{
				Optional: true,
				Description: "The app this service belongs to, when an app is made of several services. Services of one " +
					"stack reach each other by hostname (\"db:5432\") on any port, and only they can; two stacks may both " +
					"have a \"db\". Lowercase letters, digits and hyphens, starting with a letter.",
			},
			"hostname": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "This service's name inside its stack. Defaults to name.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"starts_after": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Names of the apps and databases this service needs first. It starts once each one's first port " +
					"accepts a connection, and none of them can be deleted while it lists them. (depends_on is Terraform's " +
					"own word, so this is starts_after.)",
			},
			"env": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Plain environment variables. For secret values use secret_env instead.",
			},
			"secret_env": schema.MapAttribute{
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
				Description: "Environment variables with secret values. The platform stores the values " +
					"write-only: they never appear in the app's spec or in API responses, only their names do.",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the app is running."},
			"url": schema.StringAttribute{
				Computed:    true,
				Description: "The first exposed port's public HTTPS URL, as reported by the platform.",
			},
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
			"source": schema.SingleNestedBlock{
				Description: "Build the image from a Git repo instead of pulling a prebuilt one. " +
					"The platform clones the repo, builds the Dockerfile and runs the result; " +
					"trigger a rebuild from the dashboard or the API whenever the repo changes.",
				Attributes: map[string]schema.Attribute{
					"token": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "Access token for a private repo. Write-only on the platform — it is never read back.",
					},
				},
				Blocks: map[string]schema.Block{
					"git": schema.SingleNestedBlock{
						Description: "The repo to build.",
						Attributes: map[string]schema.Attribute{
							// Needed whenever the block is there (ValidateConfig). It can't
							// be Required: Terraform asks for a block's required
							// attributes even when the block itself is left out.
							"url": schema.StringAttribute{
								Optional:    true,
								Description: "HTTPS clone URL. Required inside a source block.",
							},
							"ref": schema.StringAttribute{
								Optional:    true,
								Description: "Branch, tag (\"refs/tags/v1\") or commit to build. Unset = the repo's default branch.",
							},
							"dockerfile": schema.StringAttribute{
								Optional:    true,
								Description: "Dockerfile path relative to the build context. Defaults to \"Dockerfile\".",
							},
							"context": schema.StringAttribute{
								Optional:    true,
								Description: "Build context: a subdirectory of the repo. Defaults to the repo root.",
							},
						},
					},
				},
			},
			"image_auth": schema.SingleNestedBlock{
				Description: "Credentials for pulling a private image. Only with image, not with source.",
				Attributes: map[string]schema.Attribute{
					// Both are needed whenever the block is there (ValidateConfig);
					// see source.git.url for why they aren't Required.
					"username": schema.StringAttribute{
						Optional:    true,
						Description: "Registry username. Required inside an image_auth block.",
					},
					"password": schema.StringAttribute{
						Optional:    true,
						Sensitive:   true,
						Description: "Registry password or access token. Required inside an image_auth block. Write-only on the platform — it is never read back.",
					},
				},
			},
			"port": schema.ListNestedBlock{
				Description: "Exposed ports — each HTTP port is served on its own public HTTPS hostname.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "Port name (becomes part of the hostname)."},
						"port": schema.Int64Attribute{Required: true, Description: "Container port."},
						"internal": schema.BoolAttribute{
							Optional: true,
							Description: "No public address: the port is reachable from inside the workspace only, at " +
								"<workspace>-<name>:<port> (and at <hostname>:<port> for the services of its stack), and may " +
								"speak any TCP protocol — a database, a queue.",
						},
					},
				},
			},
		},
	}
}

func (r *containerAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type appPortModel struct {
	Name     types.String `tfsdk:"name"`
	Port     types.Int64  `tfsdk:"port"`
	Internal types.Bool   `tfsdk:"internal"`
}

type gitSourceModel struct {
	URL        types.String `tfsdk:"url"`
	Ref        types.String `tfsdk:"ref"`
	Dockerfile types.String `tfsdk:"dockerfile"`
	Context    types.String `tfsdk:"context"`
}

type sourceModel struct {
	Git   *gitSourceModel `tfsdk:"git"`
	Token types.String    `tfsdk:"token"`
}

type imageAuthModel struct {
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
}

type containerAppModel struct {
	Timeouts    timeouts.Value  `tfsdk:"timeouts"`
	Name        types.String    `tfsdk:"name"`
	Image       types.String    `tfsdk:"image"`
	Source      *sourceModel    `tfsdk:"source"`
	ImageAuth   *imageAuthModel `tfsdk:"image_auth"`
	Command     types.List      `tfsdk:"command"`
	CPU         types.String    `tfsdk:"cpu"`
	Memory      types.String    `tfsdk:"memory"`
	Env         types.Map       `tfsdk:"env"`
	SecretEnv   types.Map       `tfsdk:"secret_env"`
	Stack       types.String    `tfsdk:"stack"`
	Hostname    types.String    `tfsdk:"hostname"`
	StartsAfter types.List      `tfsdk:"starts_after"`
	Port        types.List      `tfsdk:"port"`
	Ready       types.Bool      `tfsdk:"ready"`
	URL         types.String    `tfsdk:"url"`
	Endpoints   types.List      `tfsdk:"endpoints"`
}

func (m containerAppModel) hasSource() bool {
	return m.Source != nil && m.Source.Git != nil && m.Source.Git.URL.ValueString() != ""
}

func (m containerAppModel) hasImageAuth() bool {
	return m.ImageAuth != nil && m.ImageAuth.Username.ValueString() != ""
}

// appShapeError reports an invalid combination of image, source and
// image_auth as a diagnostic summary + detail; empty strings when valid.
func appShapeError(m containerAppModel) (string, string) {
	switch {
	case m.Image.ValueString() == "" && !m.hasSource():
		return "Missing image", "Set image (prebuilt) or a source block with git.url (built from the repo)."
	case m.Image.ValueString() != "" && m.hasSource():
		return "Conflicting image and source", "Set image (prebuilt) or a source block, not both."
	case m.hasImageAuth() && m.hasSource():
		return "Conflicting image_auth and source", "image_auth is only for apps that run an image; remove it or use image instead of source."
	}
	return "", ""
}

// appConfigErrors checks the configuration as written, at plan time: what a
// block needs once it is there, and which of image, source and image_auth go
// together. A value that isn't known yet counts as set.
func appConfigErrors(m containerAppModel) [][2]string {
	set := func(v types.String) bool { return v.IsUnknown() || v.ValueString() != "" }
	var out [][2]string
	if m.Source != nil && (m.Source.Git == nil || !set(m.Source.Git.URL)) {
		out = append(out, [2]string{"Missing source.git.url", "A source block needs a git block with the repo's HTTPS clone url."})
	}
	if m.ImageAuth != nil && (!set(m.ImageAuth.Username) || !set(m.ImageAuth.Password)) {
		out = append(out, [2]string{"Incomplete image_auth", "An image_auth block needs both a username and a password."})
	}
	switch {
	case !set(m.Image) && m.Source == nil:
		out = append(out, [2]string{"Missing image", "Set image (prebuilt) or a source block with git.url (built from the repo)."})
	case set(m.Image) && m.Source != nil:
		out = append(out, [2]string{"Conflicting image and source", "Set image (prebuilt) or a source block, not both."})
	case m.ImageAuth != nil && m.Source != nil:
		out = append(out, [2]string{"Conflicting image_auth and source", "image_auth is only for apps that run an image; remove it or use image instead of source."})
	}
	return out
}

func (r *containerAppResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg containerAppModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range appConfigErrors(cfg) {
		resp.Diagnostics.AddError(e[0], e[1])
	}
}

func (m containerAppModel) sourceToken() string {
	if m.Source == nil {
		return ""
	}
	return m.Source.Token.ValueString()
}

// sortedEnv turns a Terraform map into the API's ordered {name, value} list —
// deterministic order means stable diffs on the workspace spec.
func sortedEnv(ctx context.Context, m types.Map) []map[string]any {
	var env map[string]string
	m.ElementsAs(ctx, &env, false)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vars := make([]map[string]any, 0, len(env))
	for _, k := range keys {
		vars = append(vars, map[string]any{"name": k, "value": env[k]})
	}
	return vars
}

// containerAppSpec renders the API's pod block. sendToken controls whether the
// repo token rides along: a token the platform already holds is left alone so
// an unrelated update (cpu, env…) doesn't re-send it and trigger a rebuild.
func containerAppSpec(ctx context.Context, m containerAppModel, sendToken bool) map[string]any {
	spec := map[string]any{}
	if v := m.Image.ValueString(); v != "" {
		spec["image"] = v
		if m.hasImageAuth() {
			// Every apply re-sends the password from config: the platform
			// stores it write-only and never returns it.
			auth := map[string]any{"username": m.ImageAuth.Username.ValueString()}
			if pw := m.ImageAuth.Password.ValueString(); pw != "" {
				auth["password"] = pw
			}
			spec["imageAuth"] = auth
		}
	}
	if m.hasSource() {
		g := m.Source.Git
		git := map[string]any{"url": g.URL.ValueString()}
		if v := g.Ref.ValueString(); v != "" {
			git["ref"] = v
		}
		if v := g.Dockerfile.ValueString(); v != "" {
			git["dockerfile"] = v
		}
		if v := g.Context.ValueString(); v != "" {
			git["context"] = v
		}
		src := map[string]any{"git": git}
		if tok := m.sourceToken(); sendToken && tok != "" {
			src["gitAuth"] = map[string]any{"token": tok}
		}
		spec["source"] = src
	}
	if !m.Command.IsNull() {
		var cmd []string
		m.Command.ElementsAs(ctx, &cmd, false)
		spec["command"] = cmd
	}
	if v := m.CPU.ValueString(); v != "" {
		spec["cpu"] = v
	}
	if v := m.Memory.ValueString(); v != "" {
		spec["memory"] = v
	}
	if !m.Env.IsNull() {
		spec["env"] = sortedEnv(ctx, m.Env)
	}
	if !m.SecretEnv.IsNull() {
		// Every value from config, every time — the platform stores what it
		// gets and never returns it, so config is the only source of truth.
		spec["secretEnv"] = sortedEnv(ctx, m.SecretEnv)
	}
	if !m.Port.IsNull() {
		var ports []appPortModel
		m.Port.ElementsAs(ctx, &ports, false)
		out := make([]map[string]any, 0, len(ports))
		for _, p := range ports {
			e := map[string]any{"name": p.Name.ValueString(), "port": p.Port.ValueInt64()}
			if p.Internal.ValueBool() {
				e["internal"] = true
			}
			out = append(out, e)
		}
		spec["ports"] = out
	}
	if v := m.Stack.ValueString(); v != "" {
		spec["stack"] = v
		if h := m.Hostname.ValueString(); h != "" {
			spec["hostname"] = h
		}
	}
	if !m.StartsAfter.IsNull() && !m.StartsAfter.IsUnknown() {
		var after []string
		m.StartsAfter.ElementsAs(ctx, &after, false)
		spec["dependsOn"] = after
	}
	return spec
}

// settleHostname is the platform's own rule for a service with a stack and
// no hostname of its own: it answers to its name. Without a stack there is
// no hostname.
func settleHostname(m *containerAppModel) {
	if !m.Hostname.IsUnknown() {
		return
	}
	if m.Stack.ValueString() != "" {
		m.Hostname = types.StringValue(m.Name.ValueString())
	} else {
		m.Hostname = types.StringNull()
	}
}

// readImageAuth maps the API's imageAuth (username only) back into state,
// keeping the password from state — the platform never returns it.
func readImageAuth(prev *imageAuthModel, raw any) *imageAuthModel {
	auth, _ := raw.(map[string]any)
	username, _ := auth["username"].(string)
	if username == "" {
		return nil
	}
	password := types.StringNull()
	if prev != nil {
		password = prev.Password
	}
	return &imageAuthModel{Username: types.StringValue(username), Password: password}
}

// readDefaulted reconciles an optional build field against what the API
// reports. A field the config never set stays unset when the API merely
// echoes the platform default; a real change made elsewhere shows as drift.
func readDefaulted(prev types.String, api, def string) types.String {
	switch {
	case api == "" && (prev.IsNull() || prev.ValueString() == def):
		return prev
	case api == "":
		return types.StringNull()
	case api == def && prev.IsNull():
		return prev
	default:
		return types.StringValue(api)
	}
}

// readEnvMap reconciles a map attribute against the API's {name[, value]}
// list. Names absent from state get `fallback` (secret values are never
// returned, so a var added outside Terraform shows up as drift with an empty
// value and the next apply re-asserts the config).
func readEnvMap(prev types.Map, raw []any, valueFromAPI bool) types.Map {
	if len(raw) == 0 {
		if prev.IsNull() || len(prev.Elements()) == 0 {
			return prev // null vs {} — keep whatever the config said
		}
		return types.MapNull(types.StringType)
	}
	prevVals := prev.Elements()
	kv := map[string]attr.Value{}
	for _, e := range raw {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := em["name"].(string)
		if name == "" {
			continue
		}
		switch {
		case valueFromAPI:
			value, _ := em["value"].(string)
			kv[name] = types.StringValue(value)
		default:
			if pv, ok := prevVals[name]; ok {
				kv[name] = pv
			} else {
				kv[name] = types.StringValue("")
			}
		}
	}
	return types.MapValueMust(types.StringType, kv)
}

func refreshAppStatus(ctx context.Context, c *client.Client, name string, m *containerAppModel, diags *diag.Diagnostics) {
	epType := types.ObjectType{AttrTypes: endpointAttrTypes}
	st, err := statusOf(ctx, c, name)
	if err != nil || st == nil {
		m.Ready = types.BoolValue(false)
		m.URL = types.StringValue("")
		m.Endpoints = types.ListValueMust(epType, []attr.Value{})
		return
	}
	m.Ready = types.BoolValue(st.Ready)
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

func (r *containerAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if summary, detail := appShapeError(plan); summary != "" {
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	body := containerAppSpec(ctx, plan, true)
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "pod", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create container app", err)
		return
	}
	// Apps built from a repo aren't ready until their first build lands —
	// give them longer than plain image pulls (both overridable via timeouts).
	def := 10 * time.Minute
	if plan.hasSource() {
		def = 20 * time.Minute
	}
	createTimeout, td := plan.Timeouts.Create(ctx, def)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("App did not become ready", err.Error())
	}
	settleHostname(&plan)
	refreshAppStatus(ctx, r.data.Client, plan.Name.ValueString(), &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *containerAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state containerAppModel
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
	if w == nil || w.Pod == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	sp := w.Pod
	if v, ok := sp["image"].(string); ok && v != "" {
		state.Image = types.StringValue(v)
	} else {
		state.Image = types.StringNull()
	}
	var git map[string]any
	if src, ok := sp["source"].(map[string]any); ok {
		git, _ = src["git"].(map[string]any)
	}
	if url, _ := git["url"].(string); url != "" {
		prev := gitSourceModel{}
		token := types.StringNull()
		if state.Source != nil {
			token = state.Source.Token // write-only on the platform: state keeps the config's value
			if state.Source.Git != nil {
				prev = *state.Source.Git
			}
		}
		ref, _ := git["ref"].(string)
		dockerfile, _ := git["dockerfile"].(string)
		buildCtx, _ := git["context"].(string)
		state.Source = &sourceModel{
			Token: token,
			Git: &gitSourceModel{
				URL:        types.StringValue(url),
				Ref:        readDefaulted(prev.Ref, ref, ""),
				Dockerfile: readDefaulted(prev.Dockerfile, dockerfile, defaultDockerfile),
				Context:    readDefaulted(prev.Context, buildCtx, defaultContext),
			},
		}
	} else {
		state.Source = nil
	}
	state.ImageAuth = readImageAuth(state.ImageAuth, sp["imageAuth"])
	if v, ok := sp["cpu"].(string); ok && v != "" {
		state.CPU = types.StringValue(v)
	}
	if v, ok := sp["memory"].(string); ok && v != "" {
		state.Memory = types.StringValue(v)
	}
	env, _ := sp["env"].([]any)
	state.Env = readEnvMap(state.Env, env, true)
	secretEnv, _ := sp["secretEnv"].([]any)
	state.SecretEnv = readEnvMap(state.SecretEnv, secretEnv, false)
	if v, _ := sp["stack"].(string); v != "" {
		state.Stack = types.StringValue(v)
		h, _ := sp["hostname"].(string)
		state.Hostname = types.StringValue(h)
	} else {
		state.Stack = types.StringNull()
		state.Hostname = types.StringNull()
	}
	if raw, ok := sp["dependsOn"].([]any); ok && len(raw) > 0 {
		vals := make([]attr.Value, 0, len(raw))
		for _, d := range raw {
			if s, ok := d.(string); ok {
				vals = append(vals, types.StringValue(s))
			}
		}
		state.StartsAfter = types.ListValueMust(types.StringType, vals)
	} else {
		state.StartsAfter = types.ListNull(types.StringType)
	}
	refreshAppStatus(ctx, r.data.Client, state.Name.ValueString(), &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *containerAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if summary, detail := appShapeError(plan); summary != "" {
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	// Re-send the repo token only when it changed (or the app just gained a
	// source): the platform keeps the stored one otherwise.
	sendToken := plan.sourceToken() != state.sourceToken() || !state.hasSource()
	w := client.Workload{
		ID:   plan.Name.ValueString(),
		Type: "pod",
		Pod:  containerAppSpec(ctx, plan, sendToken),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update container app", err)
		return
	}
	def := 10 * time.Minute
	if plan.hasSource() {
		def = 20 * time.Minute // a source change rebuilds before the roll
	}
	createTimeout, td := plan.Timeouts.Create(ctx, def)
	resp.Diagnostics.Append(td...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, false); err != nil {
		resp.Diagnostics.AddError("App did not become ready after update", err.Error())
	}
	settleHostname(&plan)
	refreshAppStatus(ctx, r.data.Client, w.ID, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *containerAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state containerAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete container app", err)
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

func (r *containerAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
