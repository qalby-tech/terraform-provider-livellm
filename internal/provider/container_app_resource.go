package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

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

// livellm_container_app — a container app from a prebuilt image or a
// build-from-source repo in the workspace's Git org.
type containerAppResource struct {
	data *providerData
}

func NewContainerAppResource() resource.Resource {
	return &containerAppResource{}
}

func (r *containerAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_app"
}

func (r *containerAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A container app: run a prebuilt image, or point at a repo in the workspace's Git org " +
			"and the platform builds and rolls it on every push. Exposed ports get public HTTPS hostnames.",
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
				Description: "Prebuilt image reference. Set image or source_repo, not both.",
			},
			"source_repo": schema.StringAttribute{
				Optional:    true,
				Description: "Build-from-source: a repo URL in the workspace Git org; every push builds and deploys.",
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
			"env": schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Plain environment variables. For secrets use secret_env instead.",
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
			"secret_env": schema.ListNestedBlock{
				Description: "Environment variables backed by workspace secrets — the value comes from the secret store at run time, never through Terraform.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "The env var name inside the container."},
						"path": schema.StringAttribute{Required: true, Description: "The workspace secret path providing the value."},
					},
				},
			},
			"port": schema.ListNestedBlock{
				Description: "Exposed ports — each HTTP port is served on its own public HTTPS hostname.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "Port name (becomes part of the hostname)."},
						"port": schema.Int64Attribute{Required: true, Description: "Container port."},
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
	Name types.String `tfsdk:"name"`
	Port types.Int64  `tfsdk:"port"`
}

type secretEnvModel struct {
	Name types.String `tfsdk:"name"`
	Path types.String `tfsdk:"path"`
}

type containerAppModel struct {
	Name      types.String `tfsdk:"name"`
	Image     types.String `tfsdk:"image"`
	SourceRepo types.String `tfsdk:"source_repo"`
	Command   types.List   `tfsdk:"command"`
	CPU       types.String `tfsdk:"cpu"`
	Memory    types.String `tfsdk:"memory"`
	Env       types.Map    `tfsdk:"env"`
	SecretEnv types.List   `tfsdk:"secret_env"`
	Port      types.List   `tfsdk:"port"`
	Ready     types.Bool   `tfsdk:"ready"`
	URL       types.String `tfsdk:"url"`
	Endpoints types.List   `tfsdk:"endpoints"`
}

func containerAppSpec(ctx context.Context, m containerAppModel) map[string]any {
	spec := map[string]any{}
	if v := m.Image.ValueString(); v != "" {
		spec["image"] = v
	}
	if v := m.SourceRepo.ValueString(); v != "" {
		spec["source"] = map[string]any{"repo": v}
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
		var env map[string]string
		m.Env.ElementsAs(ctx, &env, false)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic spec — stable diffs on the CR
		vars := make([]map[string]any, 0, len(env))
		for _, k := range keys {
			vars = append(vars, map[string]any{"name": k, "value": env[k]})
		}
		spec["env"] = vars
	}
	if !m.SecretEnv.IsNull() {
		var ses []secretEnvModel
		m.SecretEnv.ElementsAs(ctx, &ses, false)
		out := make([]map[string]any, 0, len(ses))
		for _, se := range ses {
			out = append(out, map[string]any{"name": se.Name.ValueString(), "path": se.Path.ValueString()})
		}
		spec["secretEnv"] = out
	}
	if !m.Port.IsNull() {
		var ports []appPortModel
		m.Port.ElementsAs(ctx, &ports, false)
		out := make([]map[string]any, 0, len(ports))
		for _, p := range ports {
			out = append(out, map[string]any{"name": p.Name.ValueString(), "port": p.Port.ValueInt64()})
		}
		spec["ports"] = out
	}
	return spec
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
	if plan.Image.ValueString() == "" && plan.SourceRepo.ValueString() == "" {
		resp.Diagnostics.AddError("Missing image", "Set image (prebuilt) or source_repo (build from source).")
		return
	}
	body := containerAppSpec(ctx, plan)
	body["id"] = plan.Name.ValueString()
	if err := r.data.Client.CreateWorkload(ctx, "pod", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create container app", err)
		return
	}
	// Build-from-source apps aren't ready until their first build lands —
	// give them longer than plain image pulls.
	timeout := 10 * time.Minute
	if plan.SourceRepo.ValueString() != "" {
		timeout = 20 * time.Minute
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("App did not become ready", err.Error())
	}
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
	}
	if src, ok := sp["source"].(map[string]any); ok {
		if v, ok := src["repo"].(string); ok && v != "" {
			state.SourceRepo = types.StringValue(v)
		}
	}
	if v, ok := sp["cpu"].(string); ok && v != "" {
		state.CPU = types.StringValue(v)
	}
	if v, ok := sp["memory"].(string); ok && v != "" {
		state.Memory = types.StringValue(v)
	}
	if raw, ok := sp["env"].([]any); ok {
		kv := map[string]attr.Value{}
		for _, e := range raw {
			if em, ok := e.(map[string]any); ok {
				name, _ := em["name"].(string)
				value, _ := em["value"].(string)
				if name != "" {
					kv[name] = types.StringValue(value)
				}
			}
		}
		state.Env = types.MapValueMust(types.StringType, kv)
	}
	refreshAppStatus(ctx, r.data.Client, state.Name.ValueString(), &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *containerAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	w := client.Workload{
		ID:   plan.Name.ValueString(),
		Type: "pod",
		Pod:  containerAppSpec(ctx, plan),
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update container app", err)
		return
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, false); err != nil {
		resp.Diagnostics.AddError("App did not become ready after update", err.Error())
	}
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
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := waitGone(waitCtx, r.data.Client, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddWarning("Deletion still in progress", err.Error())
	}
}

func (r *containerAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
