package provider

import (
	"context"
	"fmt"
	"net"
	pathpkg "path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
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
			"HTTP ports get public HTTPS hostnames, tcp/udp ports a raw address; volumes keep data " +
			"across restarts, and stopped keeps them while the app runs nothing.",
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
					"stack are one resource: they reach each other by hostname (\"db:5432\") on any port, and the rest of " +
					"the workspace only as reachable_from lets it; two stacks may both have a \"db\". Through an API key, " +
					"adding a service to a stack whose services the key didn't all make needs the Network permission. " +
					"Lowercase letters, digits and hyphens, starting with a letter.",
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
					"accepts a connection, it reaches each of them whatever their reachable_from says, and none of them can be " +
					"deleted while it lists them. (depends_on is Terraform's " +
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
			"stopped": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				Description: "Stop the app without deleting it: it runs nothing, its volumes are kept and billing " +
					"drops to their disk. Set it back to false to start the app again.",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the app is running (false while stopped)."},
			"url": schema.StringAttribute{
				Computed:    true,
				Description: "The first HTTP port's public HTTPS URL, as reported by the platform.",
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
						"udp":  schema.BoolAttribute{Computed: true},
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
				Description: "Exposed ports. Each HTTP port is served on its own public HTTPS hostname; a tcp or udp " +
					"port gets a raw public address (host:port, see endpoints); an internal port has no public address.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, Description: "Port name (becomes part of the hostname). Lowercase letters, digits and hyphens, at most 15 characters."},
						"port": schema.Int64Attribute{Required: true, Description: "Container port."},
						"tcp": schema.BoolAttribute{
							Optional: true,
							Description: "A raw TCP port instead of HTTP: it gets a public host:port address (in endpoints) " +
								"rather than an HTTPS hostname — a game server, a mail server, anything that isn't HTTP. " +
								"Not with udp or internal.",
						},
						"udp": schema.BoolAttribute{
							Optional:    true,
							Description: "A raw UDP port with a public host:port address (in endpoints) — a VPN, DNS, voice. Not with tcp or internal.",
						},
						"internal": schema.BoolAttribute{
							Optional: true,
							Description: "No public address: the port is only an address inside the workspace, " +
								"<workspace>-<name>:<port> (and <hostname>:<port> for the services of its stack), for the " +
								"resources reachable_from lets in, and may speak any TCP protocol — a database, a queue.",
						},
						"allow_cidrs": schema.ListAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "Source addresses allowed to reach this port, as CIDRs (\"203.0.113.0/24\"). " +
								"Unset = anyone. On a tcp or udp port this is the only protection there is. Not on an internal port.",
						},
					},
				},
			},
			"database": schema.ListNestedBlock{
				Description: "A database of the workspace this app uses (at most 8). The block lets the app, with every " +
					"service of its stack, reach the database inside the workspace: a database is reached only by what links " +
					"it. With env, its connection details become the app's environment variables under those names; the " +
					"password, and the url, which holds it, are read from the database's own stored login: they are never in " +
					"the app's settings, in state or in an API answer, and the app starts once the database accepts " +
					"connections (no starts_after needed). Without env the link is reach only: no variables, no wait, and " +
					"adding or removing the block never restarts the app (for start order use starts_after, which reaches " +
					"too). A database can't be deleted while an app links it. Linking a database this API key didn't make " +
					"needs a key with the Network permission.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:    true,
							Description: "The database's name, e.g. livellm_storage.db.name.",
						},
						"env": schema.MapAttribute{
							Optional:    true,
							ElementType: types.StringType,
							Description: "Environment variable name → the detail it carries: host, port, database, username, " +
								"password or url (postgres://user:password@host:5432/app, redis://:password@host:6379). A Redis " +
								"database gives host, port, url and password — no database or username. 0 to 12 variables; left out " +
								"(or {}), the link is reach only. A name is used once in the app, across env, secret_env and every " +
								"database block.",
						},
					},
				},
			},
			"volume": schema.ListNestedBlock{
				Description: "Disks that keep their data when the app restarts, is redeployed or is stopped. At most 8. " +
					"Removing a volume deletes its data. An app with volumes runs one copy.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required: true,
							Description: "Volume name: lowercase letters, digits and hyphens, at most 15 characters. " +
								"A new name is a new, empty volume.",
						},
						"size_gi": schema.Int64Attribute{
							Required:    true,
							Description: "Size in GiB. It can grow in place but never shrink.",
						},
						"mount_path": schema.StringAttribute{
							Required: true,
							Description: "Where the volume appears in the container: an absolute path such as /data (letters, digits and . _ @ + - in folder names, " +
								"at most 200 characters, no trailing slash). Not /, not in /proc, /sys or /dev, and not inside another volume's path.",
						},
					},
				},
			},
		},
	}
	withPlacement(resp.Schema.Attributes)
	withReach(resp.Schema.Attributes, "An app made of several services (stack) is one resource: its services "+
		"always reach each other and share one value, so set it on one service, or the same on each. A new service "+
		"of an app that is already there takes the app's value when this is left out. Naming another service of "+
		"its own stack is refused at apply. Public ports and their allow_cidrs open nothing inside the workspace.", true)
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
	Name       types.String `tfsdk:"name"`
	Port       types.Int64  `tfsdk:"port"`
	TCP        types.Bool   `tfsdk:"tcp"`
	UDP        types.Bool   `tfsdk:"udp"`
	Internal   types.Bool   `tfsdk:"internal"`
	AllowCIDRs types.List   `tfsdk:"allow_cidrs"`
}

var appPortAttrTypes = map[string]attr.Type{
	"name":        types.StringType,
	"port":        types.Int64Type,
	"tcp":         types.BoolType,
	"udp":         types.BoolType,
	"internal":    types.BoolType,
	"allow_cidrs": types.ListType{ElemType: types.StringType},
}

type appDatabaseModel struct {
	Name types.String `tfsdk:"name"`
	Env  types.Map    `tfsdk:"env"`
}

var appDatabaseAttrTypes = map[string]attr.Type{
	"name": types.StringType,
	"env":  types.MapType{ElemType: types.StringType},
}

type appVolumeModel struct {
	Name      types.String `tfsdk:"name"`
	SizeGi    types.Int64  `tfsdk:"size_gi"`
	MountPath types.String `tfsdk:"mount_path"`
}

var appVolumeAttrTypes = map[string]attr.Type{
	"name":       types.StringType,
	"size_gi":    types.Int64Type,
	"mount_path": types.StringType,
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
	Timeouts          timeouts.Value  `tfsdk:"timeouts"`
	Name              types.String    `tfsdk:"name"`
	Image             types.String    `tfsdk:"image"`
	Source            *sourceModel    `tfsdk:"source"`
	ImageAuth         *imageAuthModel `tfsdk:"image_auth"`
	Command           types.List      `tfsdk:"command"`
	CPU               types.String    `tfsdk:"cpu"`
	Memory            types.String    `tfsdk:"memory"`
	Env               types.Map       `tfsdk:"env"`
	SecretEnv         types.Map       `tfsdk:"secret_env"`
	Stack             types.String    `tfsdk:"stack"`
	Hostname          types.String    `tfsdk:"hostname"`
	StartsAfter       types.List      `tfsdk:"starts_after"`
	Port              types.List      `tfsdk:"port"`
	Database          types.List      `tfsdk:"database"`
	Volume            types.List      `tfsdk:"volume"`
	Stopped           types.Bool      `tfsdk:"stopped"`
	Ready             types.Bool      `tfsdk:"ready"`
	URL               types.String    `tfsdk:"url"`
	Endpoints         types.List      `tfsdk:"endpoints"`
	PlacementStrategy types.String    `tfsdk:"placement_strategy"`
	PlacementHost     types.String    `tfsdk:"placement_host"`
	PlacementRegion   types.String    `tfsdk:"placement_region"`
	ReachableFrom     types.List      `tfsdk:"reachable_from"`
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

// The platform's own rules for port and volume names, and how many volumes
// an app may have.
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

const maxVolumes = 8

// The platform's rules for database links: how many databases an app links,
// how many variables each gives, what a variable is called and what it may
// carry.
const (
	maxDatabaseLinks = 8
	maxLinkVars      = 12
)

var (
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	linkFields = []string{"host", "port", "database", "username", "password", "url"}
)

// databaseErrors checks the database blocks as written: at most 8, each
// database once, at most 12 variables each (none is a reach-only link), a
// detail the platform knows, and
// every variable name valid and used once in the app (env and secret_env
// too). Values not known yet are left for the platform to check.
func databaseErrors(dbs []appDatabaseModel, env, secretEnv types.Map) [][2]string {
	var out [][2]string
	if len(dbs) > maxDatabaseLinks {
		out = append(out, [2]string{"Too many databases", fmt.Sprintf("An app uses at most %d databases; this one has %d.", maxDatabaseLinks, len(dbs))})
	}
	taken := map[string]string{}
	for _, m := range []struct {
		attr string
		v    types.Map
	}{{"env", env}, {"secret_env", secretEnv}} {
		if m.v.IsNull() || m.v.IsUnknown() {
			continue
		}
		for name := range m.v.Elements() {
			taken[name] = m.attr
		}
	}
	seen := map[string]bool{}
	for _, d := range dbs {
		name := d.Name.ValueString()
		if !d.Name.IsUnknown() && !d.Name.IsNull() {
			if seen[name] {
				out = append(out, [2]string{"Duplicate database", fmt.Sprintf("The database %q is linked twice: put all its variables in one block.", name)})
			}
			seen[name] = true
		}
		if d.Env.IsNull() || d.Env.IsUnknown() {
			continue
		}
		vars := d.Env.Elements()
		if len(vars) > maxLinkVars {
			out = append(out, [2]string{"Too many variables", fmt.Sprintf("Database %q: at most %d variables per database; this block has %d.", name, maxLinkVars, len(vars))})
		}
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !envNameRe.MatchString(k) {
				out = append(out, [2]string{"Invalid variable name", fmt.Sprintf("Database %q: %q isn't an environment variable name (letters, digits and _, not starting with a digit).", name, k)})
			}
			if where, ok := taken[k]; ok {
				out = append(out, [2]string{"Variable set twice", fmt.Sprintf("Database %q: %s is already set in this app's %s.", name, k, where)})
			}
			taken[k] = fmt.Sprintf("database %q block", name)
			f, _ := vars[k].(types.String)
			if f.IsUnknown() || f.IsNull() {
				continue
			}
			if !slices.Contains(linkFields, f.ValueString()) {
				out = append(out, [2]string{"Unknown database detail", fmt.Sprintf("Database %q: %s = %q must be one of %s.", name, k, f.ValueString(), strings.Join(linkFields, ", "))})
			}
		}
	}
	return out
}

// portErrors checks the port blocks as written: a raw port is tcp or udp,
// never internal, and allow_cidrs are real CIDRs on a port that has a public
// address. Values not known yet are left for the platform to check.
func portErrors(ctx context.Context, ports []appPortModel) [][2]string {
	var out [][2]string
	seen := map[string]bool{}
	seenRaw := map[string]bool{}
	for _, p := range ports {
		name := p.Name.ValueString()
		if !p.Name.IsUnknown() {
			if !dnsLabelRe.MatchString(name) || len(name) > 15 {
				out = append(out, [2]string{"Invalid port name", fmt.Sprintf("Port name %q: lowercase letters, digits and hyphens, starting and ending with a letter or digit, at most 15 characters.", name)})
			}
			if seen[name] {
				out = append(out, [2]string{"Duplicate port name", fmt.Sprintf("Port name %q is listed twice.", name)})
			}
			seen[name] = true
		}
		if p.TCP.ValueBool() && p.UDP.ValueBool() {
			out = append(out, [2]string{"Conflicting tcp and udp", fmt.Sprintf("Port %q: a raw port is either tcp or udp, not both. Add a second port for the other protocol.", name)})
		}
		if p.Internal.ValueBool() && (p.TCP.ValueBool() || p.UDP.ValueBool()) {
			out = append(out, [2]string{"Conflicting internal and tcp/udp", fmt.Sprintf("Port %q: tcp and udp give a port a public address, internal gives it only an address inside the workspace (where it speaks any TCP protocol). Pick one.", name)})
		}
		if raw := p.TCP.ValueBool() || p.UDP.ValueBool(); raw && !p.Port.IsUnknown() && !p.Port.IsNull() && !p.Internal.ValueBool() {
			proto := "tcp"
			if p.UDP.ValueBool() {
				proto = "udp"
			}
			k := fmt.Sprintf("%d/%s", p.Port.ValueInt64(), proto)
			if seenRaw[k] {
				out = append(out, [2]string{"Duplicate raw port", fmt.Sprintf("Port %q: %d is already a %s port of this app.", name, p.Port.ValueInt64(), proto)})
			}
			seenRaw[k] = true
		}
		if p.AllowCIDRs.IsNull() || p.AllowCIDRs.IsUnknown() {
			continue
		}
		if p.Internal.ValueBool() && len(p.AllowCIDRs.Elements()) > 0 {
			out = append(out, [2]string{"allow_cidrs on an internal port", fmt.Sprintf("Port %q has no public address, so there is nothing to limit. Remove allow_cidrs or internal.", name)})
		}
		var cidrs []types.String
		p.AllowCIDRs.ElementsAs(ctx, &cidrs, false)
		for _, c := range cidrs {
			if c.IsUnknown() || c.IsNull() {
				continue
			}
			if _, _, err := net.ParseCIDR(c.ValueString()); err != nil {
				out = append(out, [2]string{"Invalid allow_cidrs", fmt.Sprintf("Port %q: %q is not a CIDR, like 203.0.113.0/24 (one address: 203.0.113.7/32).", name, c.ValueString())})
			}
		}
	}
	return out
}

// volumeErrors checks the volume blocks as written: at most 8, unique DNS-label
// names, a size, and absolute mount paths that are neither / nor inside one
// another.
func volumeErrors(vols []appVolumeModel) [][2]string {
	var out [][2]string
	if len(vols) > maxVolumes {
		out = append(out, [2]string{"Too many volumes", fmt.Sprintf("An app has at most %d volumes; this one has %d.", maxVolumes, len(vols))})
	}
	names := map[string]bool{}
	type mount struct{ name, path string }
	var mounts []mount
	for _, v := range vols {
		name := v.Name.ValueString()
		if !v.Name.IsUnknown() {
			if !dnsLabelRe.MatchString(name) || len(name) > 15 {
				out = append(out, [2]string{"Invalid volume name", fmt.Sprintf("Volume name %q: lowercase letters, digits and hyphens, starting and ending with a letter or digit, at most 15 characters.", name)})
			}
			if names[name] {
				out = append(out, [2]string{"Duplicate volume name", fmt.Sprintf("Volume name %q is listed twice.", name)})
			}
			names[name] = true
		}
		if !v.SizeGi.IsUnknown() && !v.SizeGi.IsNull() && v.SizeGi.ValueInt64() < 1 {
			out = append(out, [2]string{"Invalid volume size", fmt.Sprintf("Volume %q: size_gi must be at least 1.", name)})
		}
		if v.MountPath.IsUnknown() || v.MountPath.IsNull() {
			continue
		}
		mp := v.MountPath.ValueString()
		if msg := mountPathError(mp); msg != "" {
			out = append(out, [2]string{"Invalid mount_path", fmt.Sprintf("Volume %q: %s", name, msg)})
			continue
		}
		for _, m := range mounts {
			switch {
			case m.path == mp:
				out = append(out, [2]string{"Duplicate mount_path", fmt.Sprintf("Volumes %q and %q both mount at %s.", m.name, name, mp)})
			case strings.HasPrefix(mp, m.path+"/"), strings.HasPrefix(m.path, mp+"/"):
				out = append(out, [2]string{"Nested mount_path", fmt.Sprintf("Volumes %q (%s) and %q (%s): one mounts inside the other. Give each its own folder.", m.name, m.path, name, mp)})
			}
		}
		mounts = append(mounts, mount{name, mp})
	}
	return out
}

// mountPathSegment is one folder of a mount path, as the platform allows it.
var mountPathSegment = regexp.MustCompile(`^[A-Za-z0-9._@+-]+$`)

// mountPathError is why the platform would refuse a volume's mount path, or
// "": absolute and written plainly (no doubled or trailing slash, no . or ..),
// folder names of letters, digits and . _ @ + -, at most 200 characters, and
// neither / nor in /proc, /sys or /dev.
func mountPathError(mp string) string {
	switch {
	case mp == "/":
		return "it can't be mounted at /: pick a folder, like /data."
	case !strings.HasPrefix(mp, "/") || pathpkg.Clean(mp) != mp:
		return fmt.Sprintf("mount_path %q must be an absolute path written plainly, like /data (no trailing slash, // or . and ..).", mp)
	case len(mp) > 200:
		return "mount_path is longer than 200 characters."
	}
	for _, seg := range strings.Split(mp[1:], "/") {
		if !mountPathSegment.MatchString(seg) || seg == "." || seg == ".." {
			return fmt.Sprintf("mount_path %q may use only letters, digits and . _ @ + - in its folder names.", mp)
		}
	}
	for _, sys := range []string{"/proc", "/sys", "/dev"} {
		if mp == sys || strings.HasPrefix(mp, sys+"/") {
			return fmt.Sprintf("mount_path can't be %s or a folder in it: the system uses it.", sys)
		}
	}
	return ""
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
	if !cfg.Port.IsNull() && !cfg.Port.IsUnknown() {
		var ports []appPortModel
		resp.Diagnostics.Append(cfg.Port.ElementsAs(ctx, &ports, false)...)
		for _, e := range portErrors(ctx, ports) {
			resp.Diagnostics.AddAttributeError(path.Root("port"), e[0], e[1])
		}
	}
	if !cfg.Volume.IsNull() && !cfg.Volume.IsUnknown() {
		var vols []appVolumeModel
		resp.Diagnostics.Append(cfg.Volume.ElementsAs(ctx, &vols, false)...)
		for _, e := range volumeErrors(vols) {
			resp.Diagnostics.AddAttributeError(path.Root("volume"), e[0], e[1])
		}
	}
	if !cfg.Database.IsNull() && !cfg.Database.IsUnknown() {
		var dbs []appDatabaseModel
		resp.Diagnostics.Append(cfg.Database.ElementsAs(ctx, &dbs, false)...)
		for _, e := range databaseErrors(dbs, cfg.Env, cfg.SecretEnv) {
			resp.Diagnostics.AddAttributeError(path.Root("database"), e[0], e[1])
		}
	}
}

// volumePlanChecks compares the planned volumes with the ones the app has:
// a volume can't shrink (an error), and one that leaves the configuration
// takes its data with it (a warning, so the plan says so before the apply).
func volumePlanChecks(plan, state []appVolumeModel) (errs, warns [][2]string) {
	had := map[string]appVolumeModel{}
	for _, v := range state {
		had[v.Name.ValueString()] = v
	}
	keep := map[string]bool{}
	for _, v := range plan {
		if v.Name.IsUnknown() {
			return errs, nil // can't tell which volumes stay yet
		}
		name := v.Name.ValueString()
		keep[name] = true
		old, ok := had[name]
		if !ok || v.SizeGi.IsUnknown() || v.SizeGi.IsNull() {
			continue
		}
		if v.SizeGi.ValueInt64() < old.SizeGi.ValueInt64() {
			errs = append(errs, [2]string{"A volume can't shrink", fmt.Sprintf(
				"Volume %q is %d GiB; it can grow but never shrink. Keep size_gi at %d or more, or use a new name for a new, empty volume.",
				name, old.SizeGi.ValueInt64(), old.SizeGi.ValueInt64())})
		}
	}
	for _, v := range state {
		if name := v.Name.ValueString(); !keep[name] {
			warns = append(warns, [2]string{"A volume will be deleted", fmt.Sprintf(
				"Volume %q (%s, %d GiB) is no longer in the configuration: this apply deletes it and everything on it.",
				name, v.MountPath.ValueString(), v.SizeGi.ValueInt64())})
		}
	}
	return errs, warns
}

// ModifyPlan refuses shrinking a volume and warns before one is deleted.
func (r *containerAppResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	warnReachReplace(ctx, r.data, req, resp)
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return // create or destroy
	}
	var plan, state containerAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Volume.IsUnknown() {
		return
	}
	var planned, had []appVolumeModel
	if !plan.Volume.IsNull() {
		resp.Diagnostics.Append(plan.Volume.ElementsAs(ctx, &planned, false)...)
	}
	if !state.Volume.IsNull() && !state.Volume.IsUnknown() {
		resp.Diagnostics.Append(state.Volume.ElementsAs(ctx, &had, false)...)
	}
	errs, warns := volumePlanChecks(planned, had)
	for _, e := range errs {
		resp.Diagnostics.AddAttributeError(path.Root("volume"), e[0], e[1])
	}
	for _, w := range warns {
		resp.Diagnostics.AddAttributeWarning(path.Root("volume"), w[0], w[1])
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
			if p.TCP.ValueBool() {
				e["tcp"] = true
			}
			if p.UDP.ValueBool() {
				e["udp"] = true
			}
			if p.Internal.ValueBool() {
				e["internal"] = true
			}
			if !p.AllowCIDRs.IsNull() && !p.AllowCIDRs.IsUnknown() && len(p.AllowCIDRs.Elements()) > 0 {
				var cidrs []string
				p.AllowCIDRs.ElementsAs(ctx, &cidrs, false)
				e["access"] = map[string]any{"allowCIDRs": cidrs}
			}
			out = append(out, e)
		}
		spec["ports"] = out
	}
	if !m.Volume.IsNull() && !m.Volume.IsUnknown() {
		var vols []appVolumeModel
		m.Volume.ElementsAs(ctx, &vols, false)
		out := make([]map[string]any, 0, len(vols))
		for _, v := range vols {
			out = append(out, map[string]any{
				"name":      v.Name.ValueString(),
				"size":      fmt.Sprintf("%dGi", v.SizeGi.ValueInt64()),
				"mountPath": v.MountPath.ValueString(),
			})
		}
		spec["volumes"] = out
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
	if !m.Database.IsNull() && !m.Database.IsUnknown() {
		var dbs []appDatabaseModel
		m.Database.ElementsAs(ctx, &dbs, false)
		if len(dbs) > 0 {
			links := make([]map[string]any, 0, len(dbs))
			for _, d := range dbs {
				link := map[string]any{"id": d.Name.ValueString()}
				// A link without variables is reach only: it is sent with no
				// env at all (never env: null, which a platform refuses).
				if !d.Env.IsNull() && !d.Env.IsUnknown() && len(d.Env.Elements()) > 0 {
					var env map[string]string
					d.Env.ElementsAs(ctx, &env, false)
					link["env"] = env
				}
				links = append(links, link)
			}
			spec["databases"] = links
		}
	}
	if pl := placementSpec(m.PlacementStrategy, m.PlacementHost, m.PlacementRegion); pl != nil {
		spec["placement"] = pl
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

// readDatabases maps the app's database links back into state, in the
// platform's order, so a link changed outside Terraform shows as drift and an
// import fills the blocks. Nothing in a link is write-only. A link without
// variables (reach only) reads as env = {} where the state wrote it so for
// that database, and as no env otherwise.
func readDatabases(was types.List, sp map[string]any) types.List {
	objType := types.ObjectType{AttrTypes: appDatabaseAttrTypes}
	emptyWritten := map[string]bool{}
	if !was.IsNull() && !was.IsUnknown() {
		for _, e := range was.Elements() {
			o, ok := e.(types.Object)
			if !ok {
				continue
			}
			n, _ := o.Attributes()["name"].(types.String)
			m, _ := o.Attributes()["env"].(types.Map)
			if !m.IsNull() && !m.IsUnknown() && len(m.Elements()) == 0 {
				emptyWritten[n.ValueString()] = true
			}
		}
	}
	raw, _ := sp["databases"].([]any)
	vals := make([]attr.Value, 0, len(raw))
	for _, e := range raw {
		l, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := l["id"].(string)
		env := map[string]attr.Value{}
		if m, ok := l["env"].(map[string]any); ok {
			for k, v := range m {
				s, _ := v.(string)
				env[k] = types.StringValue(s)
			}
		}
		envVal := types.MapValueMust(types.StringType, env)
		if len(env) == 0 && !emptyWritten[id] {
			envVal = types.MapNull(types.StringType)
		}
		vals = append(vals, types.ObjectValueMust(appDatabaseAttrTypes, map[string]attr.Value{
			"name": types.StringValue(id),
			"env":  envVal,
		}))
	}
	return types.ListValueMust(objType, vals)
}

// sizeGi reads a volume size the platform reports ("10Gi", "1Ti") as GiB.
func sizeGi(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	for suffix, mul := range map[string]int64{"Gi": 1, "Ti": 1024} {
		if n, err := strconv.ParseInt(strings.TrimSuffix(s, suffix), 10, 64); err == nil && strings.HasSuffix(s, suffix) {
			return n * mul, true
		}
	}
	return 0, false
}

// readVolumes maps the app's volumes back into state, in the platform's order.
// A size that isn't in GiB keeps what state had, rather than reading as zero.
func readVolumes(prev types.List, sp map[string]any) types.List {
	objType := types.ObjectType{AttrTypes: appVolumeAttrTypes}
	raw, _ := sp["volumes"].([]any)
	prevSize := map[string]types.Int64{}
	if !prev.IsNull() && !prev.IsUnknown() {
		for _, e := range prev.Elements() {
			if o, ok := e.(types.Object); ok {
				a := o.Attributes()
				n, _ := a["name"].(types.String)
				sz, _ := a["size_gi"].(types.Int64)
				prevSize[n.ValueString()] = sz
			}
		}
	}
	vals := make([]attr.Value, 0, len(raw))
	for _, e := range raw {
		v, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := v["name"].(string)
		mp, _ := v["mountPath"].(string)
		size := types.Int64Null()
		if str, _ := v["size"].(string); str != "" {
			if gi, ok := sizeGi(str); ok {
				size = types.Int64Value(gi)
			} else if p, ok := prevSize[name]; ok {
				size = p
			}
		}
		vals = append(vals, types.ObjectValueMust(appVolumeAttrTypes, map[string]attr.Value{
			"name":       types.StringValue(name),
			"size_gi":    size,
			"mount_path": types.StringValue(mp),
		}))
	}
	return types.ListValueMust(objType, vals)
}

// readPorts maps the app's ports back into state, in the platform's order, so
// a change made outside Terraform (an allow-list lifted in the console) shows
// as drift and an import fills the port blocks. A flag the platform reports
// as off keeps how the configuration wrote it (false, or left out), and so
// does an empty allow-list.
func readPorts(prev types.List, sp map[string]any) types.List {
	objType := types.ObjectType{AttrTypes: appPortAttrTypes}
	prevAttrs := map[string]map[string]attr.Value{}
	if !prev.IsNull() && !prev.IsUnknown() {
		for _, e := range prev.Elements() {
			if o, ok := e.(types.Object); ok {
				a := o.Attributes()
				if n, ok := a["name"].(types.String); ok {
					prevAttrs[n.ValueString()] = a
				}
			}
		}
	}
	raw, _ := sp["ports"].([]any)
	vals := make([]attr.Value, 0, len(raw))
	for _, e := range raw {
		p, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := p["name"].(string)
		before := prevAttrs[name]
		flag := func(key string) types.Bool {
			if on, _ := p[key].(bool); on {
				return types.BoolValue(true)
			}
			if b, ok := before[key].(types.Bool); ok && !b.IsNull() && !b.IsUnknown() && !b.ValueBool() {
				return b
			}
			return types.BoolNull()
		}
		num := types.Int64Null()
		if n, ok := p["port"].(float64); ok {
			num = types.Int64Value(int64(n))
		}
		cidrs := types.ListNull(types.StringType)
		access, _ := p["access"].(map[string]any)
		if list, _ := access["allowCIDRs"].([]any); len(list) > 0 {
			cv := make([]attr.Value, 0, len(list))
			for _, c := range list {
				if s, ok := c.(string); ok {
					cv = append(cv, types.StringValue(s))
				}
			}
			cidrs = types.ListValueMust(types.StringType, cv)
		} else if l, ok := before["allow_cidrs"].(types.List); ok && !l.IsNull() && !l.IsUnknown() && len(l.Elements()) == 0 {
			cidrs = l
		}
		vals = append(vals, types.ObjectValueMust(appPortAttrTypes, map[string]attr.Value{
			"name":        types.StringValue(name),
			"port":        num,
			"tcp":         flag("tcp"),
			"udp":         flag("udp"),
			"internal":    flag("internal"),
			"allow_cidrs": cidrs,
		}))
	}
	return types.ListValueMust(objType, vals)
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
			UDP:  types.BoolValue(e.UDP),
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
	reach := configuredReach(ctx, req.Config, &resp.Diagnostics)
	reachBody(body, reach)
	if err := r.data.Client.CreateWorkload(ctx, "pod", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create container app", err)
		return
	}
	if w := stopAfterCreateBody(ctx, plan); w != nil {
		// Born stopped: create, then stop, the same way a machine is.
		if err := r.data.Client.UpdateWorkload(ctx, w.ID, *w); err != nil {
			apiDiag(&resp.Diagnostics, "Cannot stop container app after create", err)
		}
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
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), plan.Stopped.ValueBool()); err != nil {
		resp.Diagnostics.AddError("App did not become ready", err.Error())
	}
	settleHostname(&plan)
	refreshAppStatus(ctx, r.data.Client, plan.Name.ValueString(), &plan, &resp.Diagnostics)
	plan.ReachableFrom = settleReach(ctx, r.data.Client, plan.Name.ValueString(), plan.ReachableFrom, reach, &resp.Diagnostics)
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
	state.Port = readPorts(state.Port, sp)
	state.Database = readDatabases(state.Database, sp)
	state.Volume = readVolumes(state.Volume, sp)
	state.Stopped = types.BoolValue(w.Stopped)
	state.ReachableFrom = readReach(ctx, state.ReachableFrom, w.ReachableFrom)
	refreshPlacement(sp, &state.PlacementStrategy, &state.PlacementHost, &state.PlacementRegion)
	refreshAppStatus(ctx, r.data.Client, state.Name.ValueString(), &state, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// updateWorkloadBody is what an apply writes for an existing app: the whole
// app as configured, and whether it is stopped. The platform keeps an app's
// volumes when a save leaves them out, so a configuration with no volume
// blocks sends an empty list — that is what removes the last one. While the
// volumes aren't known yet nothing is said about them.
func updateWorkloadBody(ctx context.Context, plan containerAppModel, sendToken bool) client.Workload {
	w := client.Workload{
		ID:      plan.Name.ValueString(),
		Type:    "pod",
		Stopped: plan.Stopped.ValueBool(),
		Pod:     containerAppSpec(ctx, plan, sendToken),
	}
	if _, ok := w.Pod["volumes"]; !ok && !plan.Volume.IsUnknown() {
		w.Pod["volumes"] = []map[string]any{}
	}
	return w
}

// stopAfterCreateBody is the write that stops an app configured as stopped
// right after it is created (stopped lives on the workload, not the create
// body); nil when it should run.
func stopAfterCreateBody(ctx context.Context, plan containerAppModel) *client.Workload {
	if !plan.Stopped.ValueBool() {
		return nil
	}
	return &client.Workload{ID: plan.Name.ValueString(), Type: "pod", Stopped: true, Pod: containerAppSpec(ctx, plan, false)}
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
	w := updateWorkloadBody(ctx, plan, sendToken)
	reach := configuredReach(ctx, req.Config, &resp.Diagnostics)
	w.ReachableFrom = reach
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
	if err := waitReady(waitCtx, r.data.Client, w.ID, w.Stopped); err != nil {
		resp.Diagnostics.AddError("App did not become ready after update", err.Error())
	}
	settleHostname(&plan)
	refreshAppStatus(ctx, r.data.Client, w.ID, &plan, &resp.Diagnostics)
	plan.ReachableFrom = settleReach(ctx, r.data.Client, w.ID, plan.ReachableFrom, reach, &resp.Diagnostics)
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
