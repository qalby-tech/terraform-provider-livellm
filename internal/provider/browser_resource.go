package provider

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"golang.org/x/text/language"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_browser — a headless Chromium browser with a live view and a CDP
// endpoint, ready to be driven by your own automation. It can speak a
// language and live in a time zone of its own, and go out through proxies
// that rotate.
type browserResource struct {
	data *providerData
}

func NewBrowserResource() resource.Resource {
	return &browserResource{}
}

func (r *browserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_browser"
}

var (
	localeRe   = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2}|-[0-9]{3})?$`)
	languageRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z][a-z]{3})?(-[A-Z]{2}|-[0-9]{3})?$`)
	timezoneRe = regexp.MustCompile(`^(UTC|[A-Za-z_]+(/[A-Za-z0-9_+\-]+){1,2})$`)
	proxyRe    = regexp.MustCompile(`^(http|https|socks5)://(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?):[0-9]{1,5}$`)
	httpURLRe  = regexp.MustCompile(`^https?://[^\s/?#]+\S*$`)
	// The platform refuses a check address whose query matches this (its
	// own pattern), as well as the keys below with no value.
	checkURLSecretRe = regexp.MustCompile(`(?i)[?&](token|key|apikey|api_key|access_token|secret|password|pass|auth|sig|signature)=`)
)

// checkURLSecretKeys are the query keys the platform takes for a credential.
var checkURLSecretKeys = map[string]bool{
	"token": true, "key": true, "apikey": true, "api_key": true, "access_token": true, "secret": true,
	"password": true, "pass": true, "auth": true, "sig": true, "signature": true,
}

// checkURLCarriesCredential says a check address has a login, or a query key
// that carries one (token, key, password, signature, ..., in any case, with
// or without a value), as the platform decides it: the address is shown to
// everyone in the workspace, so it refuses both.
func checkURLCarriesCredential(s string) bool {
	if checkURLSecretRe.MatchString(s) {
		return true
	}
	u, err := url.Parse(s)
	if err != nil {
		return true
	}
	if u.User != nil {
		return true
	}
	for _, pair := range strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == ';' }) {
		k, _, _ := strings.Cut(pair, "=")
		if dk, err := url.QueryUnescape(k); err == nil {
			k = dk
		}
		if checkURLSecretKeys[strings.ToLower(strings.TrimSpace(k))] {
			return true
		}
	}
	return false
}

// proxyHostIsLocal says a proxy server names this machine or the link, which
// the platform refuses.
func proxyHostIsLocal(server string) bool {
	u, err := url.Parse(server)
	if err != nil {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified())
}

// The platform's defaults: a value the configuration left out reads back as
// left out when the platform holds just its default.
const (
	defaultChangeIPMethod  = "GET"
	defaultMinChangeIPSecs = 60
	defaultRotationMode    = "off"
	defaultRotationOrder   = "sequential"
	defaultGeoAccuracy     = 100
)

var objectAsOpts = basetypes.ObjectAsOptions{UnhandledNullAsEmpty: true, UnhandledUnknownAsEmpty: true}

func (r *browserResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A browser with a live view and an automation endpoint for your own automation: Chrome (CDP) " +
			"or Camoufox (Firefox, Playwright).",
		Attributes: map[string]schema.Attribute{
			"engine": engineAttribute("The browser engine: \"chrome\" (the default; driven over CDP) or \"camoufox\" " +
				"(Firefox-based; driven with Playwright 1.62, firefox.connect). Fixed at creation: changing it replaces the " +
				"browser. Camoufox takes no extensions."),
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Workload id. Changing it replaces the browser.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cpu": schema.StringAttribute{
				Optional:    true,
				Description: "CPU request, e.g. \"1\".",
			},
			"memory": schema.StringAttribute{
				Optional:    true,
				Description: "Memory request, e.g. \"2Gi\".",
			},
			"locale": schema.StringAttribute{
				Optional: true,
				Description: "The browser's language and region, e.g. \"ru-RU\": its pages, navigator.language and " +
					"Intl. One of the locales GET /v1/browsers/locales lists, in its current spelling (\"he-IL\", not \"iw-IL\"). " +
					"Changing it restarts the browser; " +
					"the profile is kept. Removing it puts the browser back to its default.",
				Validators: []validator.String{stringvalidator.RegexMatches(localeRe,
					"a language and region such as \"ru-RU\" or \"en-US\" (lowercase language, uppercase region)")},
			},
			"timezone": schema.StringAttribute{
				Optional: true,
				Description: "The browser's time zone, an IANA name such as \"Europe/Moscow\", or \"UTC\". Changing " +
					"it restarts the browser.",
				Validators: []validator.String{stringvalidator.RegexMatches(timezoneRe,
					"an IANA time zone such as \"Europe/Moscow\", or \"UTC\"")},
			},
			"languages": schema.ListAttribute{
				Optional:    true,
				Computed:    true,
				ElementType: types.StringType,
				Description: "The languages pages are asked for (Accept-Language, navigator.languages), in order; " +
					"the first is locale, and they need locale (the platform refuses them without it). Left out, it follows locale (\"ru-RU\" gives " +
					"ru-RU, ru, en-US, en). Removing it from the configuration keeps the current list until locale changes.",
				Validators: []validator.List{
					listvalidator.SizeBetween(1, 6),
					listvalidator.UniqueValues(),
					listvalidator.ValueStringsAre(stringvalidator.RegexMatches(languageRe,
						"a language tag such as \"ru-RU\", \"ru\" or \"zh-Hant-TW\"")),
				},
			},
			"profiles_ready": schema.BoolAttribute{
				Computed: true,
				Description: "Whether the browser's profile can be saved, restored, exported and imported. A " +
					"browser made before profiles existed turns it on at its next restart.",
			},
			"ready": schema.BoolAttribute{Computed: true, Description: "Whether the browser is up."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
			"geolocation": schema.SingleNestedBlock{
				Description: "What pages get when they ask for a location. Left out, the browser asks as Chrome " +
					"does. Changing it restarts the browser.",
				Attributes: map[string]schema.Attribute{
					// Needed whenever the block is there (ValidateConfig).
					"mode": schema.StringAttribute{
						Optional:    true,
						Description: "\"off\" refuses every page; \"fixed\" answers latitude and longitude.",
						Validators:  []validator.String{stringvalidator.OneOf("off", "fixed")},
					},
					"latitude": schema.Float64Attribute{
						Optional:    true,
						Description: "-90 to 90 (mode \"fixed\").",
						Validators:  []validator.Float64{float64validator.Between(-90, 90)},
					},
					"longitude": schema.Float64Attribute{
						Optional:    true,
						Description: "-180 to 180 (mode \"fixed\").",
						Validators:  []validator.Float64{float64validator.Between(-180, 180)},
					},
					"accuracy": schema.Int64Attribute{
						Optional:    true,
						Description: "Meters, 1 to 10000 (mode \"fixed\"; default 100).",
						Validators:  []validator.Int64{int64validator.Between(1, 10000)},
					},
				},
			},
			"proxy": schema.SingleNestedBlock{
				Description: "Send the browser's traffic through proxies. Adding the block restarts the browser " +
					"once; changing it later does not. With no upstream the browser goes out directly. " +
					"Removing the block restarts it. The proxy applies to the browser as LiveLLM starts it: " +
					"anything that connects to it (CDP) can open a context that goes around it. Changing it " +
					"changes where the browser's traffic goes and the address sites see: before you change it, " +
					"ask the user and wait for their agreement.",
				Attributes: map[string]schema.Attribute{
					"auth_version": schema.Int64Attribute{
						Optional: true,
						Description: "Change it to send the upstreams' username_wo, password_wo and " +
							"change_ip_url_wo again (they are write-only, so a new value alone makes no plan).",
					},
					"check_url": schema.StringAttribute{
						Optional: true,
						Description: "An http(s) address that answers the caller's IP, as text or JSON with \"ip\", " +
							"fetched through the proxy to see the exit address. Left out, LiveLLM's own. Everyone in the " +
							"workspace sees it, so it can't carry a login, or a token, key, password or signature in its query.",
						Validators: []validator.String{stringvalidator.RegexMatches(httpURLRe, "an http:// or https:// address")},
					},
				},
				Blocks: map[string]schema.Block{
					"upstream": schema.ListNestedBlock{
						Description: "A proxy to go out through, at most 20. Rotation moves along them in order.",
						Validators:  []validator.List{listvalidator.SizeAtMost(20)},
						NestedObject: schema.NestedBlockObject{
							Attributes: map[string]schema.Attribute{
								// Needed whenever the block is there (ValidateConfig).
								"name": schema.StringAttribute{
									Optional:    true,
									Description: "Its name: lowercase letters, digits and dashes, unique in the block.",
								},
								"server": schema.StringAttribute{
									Optional:    true,
									Description: "scheme://host:port, scheme http, https or socks5; no login, path or query in it, and not this machine (localhost, a loopback or link-local address).",
								},
								"username_wo": schema.StringAttribute{
									Optional:    true,
									WriteOnly:   true,
									Sensitive:   true,
									Description: "The proxy's username (one line, at most 255 characters), write-only: never stored in state. Set with password_wo.",
								},
								"password_wo": schema.StringAttribute{
									Optional:    true,
									WriteOnly:   true,
									Sensitive:   true,
									Description: "The proxy's password (one line, at most 255 characters), write-only: never stored in state. Set with username_wo.",
								},
								"change_ip_url_wo": schema.StringAttribute{
									Optional:  true,
									WriteOnly: true,
									Sensitive: true,
									Description: "A mobile proxy's change-IP address, called before rotating to this " +
										"upstream, write-only: never stored in state.",
								},
								"change_ip_method": schema.StringAttribute{
									Optional:    true,
									Description: "\"GET\" (the default) or \"POST\", with change_ip_url_wo only.",
									Validators:  []validator.String{stringvalidator.OneOf("GET", "POST")},
								},
								"min_change_ip_seconds": schema.Int64Attribute{
									Optional:    true,
									Description: "The shortest time between two change-IP calls, 10 to 3600 seconds (default 60), with change_ip_url_wo only.",
									Validators:  []validator.Int64{int64validator.Between(10, 3600)},
								},
								"has_auth": schema.BoolAttribute{
									Computed: true,
									Description: "Whether a login is stored for it (never read back). Planned from the " +
										"configuration: true with username_wo and password_wo, false without.",
								},
								"has_change_ip": schema.BoolAttribute{
									Computed: true,
									Description: "Whether a change-IP address is stored for it (never read back). " +
										"Planned from the configuration: true with change_ip_url_wo.",
								},
							},
						},
					},
					"rotation": schema.SingleNestedBlock{
						Description: "When the browser moves to the next upstream. Left out, it doesn't. Rotating " +
							"drops open connections; pages reconnect.",
						Attributes: map[string]schema.Attribute{
							"mode": schema.StringAttribute{
								Optional: true,
								Description: "\"off\" (the default), \"session\" (when a Browser API session starts " +
									"on it and no other session is in use) or \"interval\" (every every_minutes).",
								Validators: []validator.String{stringvalidator.OneOf("off", "session", "interval")},
							},
							"every_minutes": schema.Int64Attribute{
								Optional:    true,
								Description: "1 to 1440, with mode \"interval\" only.",
								Validators:  []validator.Int64{int64validator.Between(1, 1440)},
							},
							"order": schema.StringAttribute{
								Optional:    true,
								Description: "\"sequential\" (the default) or \"random\".",
								Validators:  []validator.String{stringvalidator.OneOf("sequential", "random")},
							},
						},
					},
				},
			},
		},
	}
	withPlacement(resp.Schema.Attributes)
	withReach(resp.Schema.Attributes, "A Browser API that holds this browser reaches it whatever this says, and so "+
		"does whatever may reach that Browser API: it drives the browser from its place.", false)
}

func (r *browserResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type browserModel struct {
	Name              types.String   `tfsdk:"name"`
	Engine            types.String   `tfsdk:"engine"`
	CPU               types.String   `tfsdk:"cpu"`
	Memory            types.String   `tfsdk:"memory"`
	Locale            types.String   `tfsdk:"locale"`
	Timezone          types.String   `tfsdk:"timezone"`
	Languages         types.List     `tfsdk:"languages"`
	Geolocation       types.Object   `tfsdk:"geolocation"`
	Proxy             types.Object   `tfsdk:"proxy"`
	ProfilesReady     types.Bool     `tfsdk:"profiles_ready"`
	Ready             types.Bool     `tfsdk:"ready"`
	Timeouts          timeouts.Value `tfsdk:"timeouts"`
	PlacementStrategy types.String   `tfsdk:"placement_strategy"`
	PlacementHost     types.String   `tfsdk:"placement_host"`
	PlacementRegion   types.String   `tfsdk:"placement_region"`
	ReachableFrom     types.List     `tfsdk:"reachable_from"`
}

type geoModel struct {
	Mode      types.String  `tfsdk:"mode"`
	Latitude  types.Float64 `tfsdk:"latitude"`
	Longitude types.Float64 `tfsdk:"longitude"`
	Accuracy  types.Int64   `tfsdk:"accuracy"`
}

var geoAttrTypes = map[string]attr.Type{
	"mode":      types.StringType,
	"latitude":  types.Float64Type,
	"longitude": types.Float64Type,
	"accuracy":  types.Int64Type,
}

type upstreamModel struct {
	Name               types.String `tfsdk:"name"`
	Server             types.String `tfsdk:"server"`
	UsernameWO         types.String `tfsdk:"username_wo"`
	PasswordWO         types.String `tfsdk:"password_wo"`
	ChangeIPURLWO      types.String `tfsdk:"change_ip_url_wo"`
	ChangeIPMethod     types.String `tfsdk:"change_ip_method"`
	MinChangeIPSeconds types.Int64  `tfsdk:"min_change_ip_seconds"`
	HasAuth            types.Bool   `tfsdk:"has_auth"`
	HasChangeIP        types.Bool   `tfsdk:"has_change_ip"`
}

var upstreamAttrTypes = map[string]attr.Type{
	"name":                  types.StringType,
	"server":                types.StringType,
	"username_wo":           types.StringType,
	"password_wo":           types.StringType,
	"change_ip_url_wo":      types.StringType,
	"change_ip_method":      types.StringType,
	"min_change_ip_seconds": types.Int64Type,
	"has_auth":              types.BoolType,
	"has_change_ip":         types.BoolType,
}

type rotationModel struct {
	Mode         types.String `tfsdk:"mode"`
	EveryMinutes types.Int64  `tfsdk:"every_minutes"`
	Order        types.String `tfsdk:"order"`
}

var rotationAttrTypes = map[string]attr.Type{
	"mode":          types.StringType,
	"every_minutes": types.Int64Type,
	"order":         types.StringType,
}

type proxyModel struct {
	Upstream    types.List   `tfsdk:"upstream"`
	Rotation    types.Object `tfsdk:"rotation"`
	AuthVersion types.Int64  `tfsdk:"auth_version"`
	CheckURL    types.String `tfsdk:"check_url"`
}

var upstreamObjType = types.ObjectType{AttrTypes: upstreamAttrTypes}

var proxyAttrTypes = map[string]attr.Type{
	"upstream":     types.ListType{ElemType: upstreamObjType},
	"rotation":     types.ObjectType{AttrTypes: rotationAttrTypes},
	"auth_version": types.Int64Type,
	"check_url":    types.StringType,
}

// known says whether a value is there: neither null nor unknown.
func known(v attr.Value) bool { return !v.IsNull() && !v.IsUnknown() }

func (m browserModel) geo(ctx context.Context) *geoModel {
	if !known(m.Geolocation) {
		return nil
	}
	var g geoModel
	m.Geolocation.As(ctx, &g, objectAsOpts)
	return &g
}

func (m browserModel) proxy(ctx context.Context) *proxyModel {
	if !known(m.Proxy) {
		return nil
	}
	var p proxyModel
	m.Proxy.As(ctx, &p, objectAsOpts)
	return &p
}

func (p *proxyModel) upstreams(ctx context.Context) []upstreamModel {
	if p == nil || !known(p.Upstream) {
		return nil
	}
	var out []upstreamModel
	p.Upstream.ElementsAs(ctx, &out, false)
	return out
}

func (p *proxyModel) rotation(ctx context.Context) *rotationModel {
	if p == nil || !known(p.Rotation) {
		return nil
	}
	var r rotationModel
	p.Rotation.As(ctx, &r, objectAsOpts)
	return &r
}

func (m browserModel) languageList(ctx context.Context) []string {
	if !known(m.Languages) {
		return nil
	}
	var out []string
	m.Languages.ElementsAs(ctx, &out, false)
	return out
}

func upstreamsByName(ups []upstreamModel) map[string]upstreamModel {
	out := map[string]upstreamModel{}
	for _, u := range ups {
		out[u.Name.ValueString()] = u
	}
	return out
}

// hasLogin / hasChangeIPURL: what the configuration gives an upstream.
func (u upstreamModel) hasLogin() bool {
	return u.UsernameWO.ValueString() != "" && u.PasswordWO.ValueString() != ""
}
func (u upstreamModel) hasChangeIPURL() bool { return u.ChangeIPURLWO.ValueString() != "" }

// browserSpec is the browser block the API takes. plan is what is wanted;
// cfg is the configuration, the only place the write-only values are; state
// is what Terraform holds now, nil on a create. A newer setting goes out only
// when the plan has it or the state had it (then as its clear value), so a
// browser without them sends exactly what it always did. The state holds a
// newer setting only while the configuration manages it (readBrowser), so a
// setting made in the console for a browser whose configuration leaves it
// out is never cleared. A proxy block the plan leaves as the state has it is
// not sent at all: the platform keeps it, and an update that only changes cpu
// is no proxy change.
func browserSpec(ctx context.Context, plan, cfg browserModel, state *browserModel) map[string]any {
	spec := map[string]any{}
	engineSpec(spec, plan.Engine)
	if v := plan.CPU.ValueString(); v != "" {
		spec["cpu"] = v
	}
	if v := plan.Memory.ValueString(); v != "" {
		spec["memory"] = v
	}
	if pl := placementSpec(plan.PlacementStrategy, plan.PlacementHost, plan.PlacementRegion); pl != nil {
		spec["placement"] = pl
	}
	if known(plan.Locale) {
		spec["locale"] = plan.Locale.ValueString()
	} else if state != nil && known(state.Locale) {
		spec["locale"] = ""
	}
	if known(plan.Timezone) {
		spec["timezone"] = plan.Timezone.ValueString()
	} else if state != nil && known(state.Timezone) {
		spec["timezone"] = ""
	}
	// languages: what the configuration says. Left out, it follows locale:
	// [] has the platform work it out again when locale is new or changed,
	// and clears it with locale; otherwise it is left out, which keeps it.
	switch {
	case known(cfg.Languages):
		spec["languages"] = cfg.languageList(ctx)
	case known(plan.Locale):
		if state == nil || !plan.Locale.Equal(state.Locale) || !known(state.Languages) {
			spec["languages"] = []string{}
		}
	case state != nil && len(state.languageList(ctx)) > 0:
		spec["languages"] = []string{}
	}
	if g := plan.geo(ctx); g != nil {
		e := map[string]any{"mode": g.Mode.ValueString()}
		if g.Mode.ValueString() == "fixed" {
			e["latitude"] = g.Latitude.ValueFloat64()
			e["longitude"] = g.Longitude.ValueFloat64()
			if known(g.Accuracy) {
				e["accuracy"] = g.Accuracy.ValueInt64()
			}
		}
		spec["geolocation"] = e
	} else if state != nil && known(state.Geolocation) {
		spec["geolocation"] = map[string]any{"mode": "prompt"}
	}
	if p := plan.proxy(ctx); p != nil {
		var prev *proxyModel
		if state != nil {
			prev = state.proxy(ctx)
		}
		if prev == nil || !plan.Proxy.Equal(state.Proxy) {
			spec["proxy"] = proxySpec(ctx, p, cfg.proxy(ctx), prev)
		}
	} else if state != nil && known(state.Proxy) {
		spec["proxy"] = map[string]any{"remove": true}
	}
	return spec
}

// proxySpec is the proxy block. An upstream's write-only values are sent when
// the platform can't hold them yet — a new proxy block, a new upstream, a
// login or change-IP address it didn't hold, or a new server (the platform
// won't carry a login to another host) — and when auth_version changed.
// Otherwise the upstream says it has them (hasAuth: true), which keeps what is
// stored, so an update that only changes cpu sends no secret. An upstream
// with none in the configuration says hasAuth: false, which removes it. An
// upstream the configuration doesn't write (lifecycle ignore_changes) keeps
// what is stored: it sends the flags planBrowser took from the state.
func proxySpec(ctx context.Context, plan, cfg, prev *proxyModel) map[string]any {
	resend := prev == nil || !plan.AuthVersion.Equal(prev.AuthVersion)
	stored := upstreamsByName(prev.upstreams(ctx))
	given := upstreamsByName(cfg.upstreams(ctx))
	ups := []map[string]any{}
	for _, u := range plan.upstreams(ctx) {
		name := u.Name.ValueString()
		e := map[string]any{"name": name, "server": u.Server.ValueString()}
		if known(u.ChangeIPMethod) {
			e["changeIpMethod"] = u.ChangeIPMethod.ValueString()
		}
		if known(u.MinChangeIPSeconds) {
			e["minChangeIpSeconds"] = u.MinChangeIPSeconds.ValueInt64()
		}
		c, written := given[name]
		st, had := stored[name]
		if keptUpstream(c, written) {
			e["hasAuth"] = flagOf(u.HasAuth, st.HasAuth)
			e["hasChangeIp"] = flagOf(u.HasChangeIP, st.HasChangeIP)
			ups = append(ups, e)
			continue
		}
		fresh := resend || !had || st.Server.ValueString() != u.Server.ValueString()
		if c.hasLogin() {
			if fresh || !st.HasAuth.ValueBool() {
				e["username"] = c.UsernameWO.ValueString()
				e["password"] = c.PasswordWO.ValueString()
			} else {
				e["hasAuth"] = true
			}
		} else {
			e["hasAuth"] = false
		}
		if c.hasChangeIPURL() {
			if fresh || !st.HasChangeIP.ValueBool() {
				e["changeIpUrl"] = c.ChangeIPURLWO.ValueString()
			} else {
				e["hasChangeIp"] = true
			}
		} else {
			e["hasChangeIp"] = false
		}
		ups = append(ups, e)
	}
	out := map[string]any{"upstreams": ups}
	if rot := plan.rotation(ctx); rot != nil {
		r := map[string]any{}
		if known(rot.Mode) {
			r["mode"] = rot.Mode.ValueString()
		}
		if known(rot.EveryMinutes) {
			r["everyMinutes"] = rot.EveryMinutes.ValueInt64()
		}
		if known(rot.Order) {
			r["order"] = rot.Order.ValueString()
		}
		out["rotation"] = r
	}
	if known(plan.CheckURL) {
		out["checkUrl"] = plan.CheckURL.ValueString()
	}
	return out
}

// keptUpstream: the configuration doesn't write this upstream itself. Either
// it isn't there, or its has_auth / has_change_ip are set, which a written
// configuration never does (they are read-only): Terraform copied the block
// from the state for lifecycle ignore_changes. Such an upstream keeps what is
// stored instead of losing it for want of write-only values.
func keptUpstream(c upstreamModel, written bool) bool {
	return !written || known(c.HasAuth) || known(c.HasChangeIP)
}

// flagOf is the first known of the planned and the stored flag, else false.
func flagOf(vals ...types.Bool) bool {
	for _, v := range vals {
		if known(v) {
			return v.ValueBool()
		}
	}
	return false
}

// canonicalTag says how the platform spells a language tag when it isn't
// spelled that way: older codes such as "iw-IL" are stored as "he-IL", and a
// plan holding the old one would never settle.
func canonicalTag(s string) (string, bool) {
	t, err := language.Parse(s)
	if err != nil || t.String() == s {
		return "", false
	}
	return t.String(), true
}

// browserConfigErrors: what the configuration needs before any call. Unknown
// values are left to apply.
func browserConfigErrors(ctx context.Context, m browserModel) [][2]string {
	var out [][2]string
	if known(m.Locale) {
		if c, ok := canonicalTag(m.Locale.ValueString()); ok {
			out = append(out, [2]string{"Older language code",
				fmt.Sprintf("locale %q is an older spelling; use %q.", m.Locale.ValueString(), c)})
		}
	}
	for _, l := range m.languageList(ctx) {
		if c, ok := canonicalTag(l); ok {
			out = append(out, [2]string{"Older language code",
				fmt.Sprintf("languages: %q is an older spelling; use %q.", l, c)})
		}
	}
	// The platform refuses languages without a locale (the first language is
	// the locale). A locale not known yet is left to apply.
	if langs := m.languageList(ctx); len(langs) > 0 && m.Locale.IsNull() {
		out = append(out, [2]string{"languages need locale",
			"languages is set without locale. Set locale too (the first language is the locale), or remove languages: left out, they follow locale."})
	}
	if langs := m.languageList(ctx); len(langs) > 0 && known(m.Locale) && langs[0] != m.Locale.ValueString() {
		out = append(out, [2]string{"languages must start with locale",
			fmt.Sprintf("languages starts with %q; with locale = %q it must start with %q.", langs[0], m.Locale.ValueString(), m.Locale.ValueString())})
	}
	if g := m.geo(ctx); g != nil && !g.Mode.IsUnknown() {
		switch g.Mode.ValueString() {
		case "":
			out = append(out, [2]string{"geolocation needs a mode", "Set mode = \"off\" or \"fixed\", or remove the block."})
		case "fixed":
			if g.Latitude.IsNull() || g.Longitude.IsNull() {
				out = append(out, [2]string{"geolocation needs a place", "mode = \"fixed\" needs latitude and longitude."})
			}
		case "off":
			if !g.Latitude.IsNull() || !g.Longitude.IsNull() || !g.Accuracy.IsNull() {
				out = append(out, [2]string{"geolocation off takes no place", "latitude, longitude and accuracy go with mode = \"fixed\" only."})
			}
		}
	}
	p := m.proxy(ctx)
	if p == nil {
		return out
	}
	seen := map[string]bool{}
	for _, u := range p.upstreams(ctx) {
		if u.Name.IsUnknown() || u.Server.IsUnknown() {
			continue
		}
		name, server := u.Name.ValueString(), u.Server.ValueString()
		switch {
		case name == "" || server == "":
			out = append(out, [2]string{"Incomplete upstream", "An upstream block needs a name and a server."})
			continue
		case !browserNameRe.MatchString(name) || len(name) > 63:
			out = append(out, [2]string{"Bad upstream name", fmt.Sprintf("%q: use lowercase letters, digits and dashes, starting and ending with a letter or digit.", name)})
		case seen[name]:
			out = append(out, [2]string{"Upstream name used twice", fmt.Sprintf("Two upstream blocks are called %q.", name)})
		}
		seen[name] = true
		if !proxyRe.MatchString(server) {
			// The server isn't repeated: a login written into it would land
			// in the plan's output.
			out = append(out, [2]string{"Bad upstream server",
				fmt.Sprintf("Upstream %q: the server should be scheme://host:port with scheme http, https or socks5, and no login, path or query (the login goes in username_wo and password_wo).", name)})
		}
		if proxyRe.MatchString(server) && proxyHostIsLocal(server) {
			out = append(out, [2]string{"Bad upstream server",
				fmt.Sprintf("Upstream %q: the server can't be this machine (localhost, a loopback or link-local address).", name)})
		}
		for _, f := range []struct {
			field string
			v     types.String
		}{{"username_wo", u.UsernameWO}, {"password_wo", u.PasswordWO}} {
			// The value is never repeated: it is a secret.
			field, v := f.field, f.v
			if known(v) && (len(v.ValueString()) > 255 || strings.ContainsAny(v.ValueString(), "\r\n\x00")) {
				out = append(out, [2]string{"Bad upstream login",
					fmt.Sprintf("Upstream %q: %s must be one line of at most 255 characters.", name, field)})
			}
		}
		if !u.UsernameWO.IsUnknown() && !u.PasswordWO.IsUnknown() && (u.UsernameWO.ValueString() == "") != (u.PasswordWO.ValueString() == "") {
			out = append(out, [2]string{"Incomplete upstream login", fmt.Sprintf("Upstream %q: set username_wo and password_wo together.", name)})
		}
		if v := u.ChangeIPURLWO; known(v) && !httpURLRe.MatchString(v.ValueString()) {
			out = append(out, [2]string{"Bad change_ip_url_wo", fmt.Sprintf("Upstream %q: the change-IP address should start with http:// or https://.", name)})
		}
		// The platform keeps these only with a change-IP address; without
		// one they would read back empty and plan again forever.
		if u.ChangeIPURLWO.IsNull() && (known(u.ChangeIPMethod) || known(u.MinChangeIPSeconds)) {
			out = append(out, [2]string{"change_ip_method needs change_ip_url_wo",
				fmt.Sprintf("Upstream %q: change_ip_method and min_change_ip_seconds go with change_ip_url_wo.", name)})
		}
	}
	// The address is never repeated: what makes it refused is a credential.
	if known(p.CheckURL) && checkURLCarriesCredential(p.CheckURL.ValueString()) {
		out = append(out, [2]string{"check_url can't carry a credential",
			"check_url has a login, or a token, key, password or signature in its query. Everyone in the workspace sees the address: use one that needs none."})
	}
	if rot := p.rotation(ctx); rot != nil && !rot.Mode.IsUnknown() && !rot.EveryMinutes.IsUnknown() {
		interval := rot.Mode.ValueString() == "interval"
		if interval && rot.EveryMinutes.IsNull() {
			out = append(out, [2]string{"Rotation needs every_minutes", "mode = \"interval\" needs every_minutes."})
		}
		if !interval && !rot.EveryMinutes.IsNull() {
			out = append(out, [2]string{"every_minutes goes with interval", "every_minutes is for mode = \"interval\" only."})
		}
	}
	return out
}

func (r *browserResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg browserModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, e := range browserConfigErrors(ctx, cfg) {
		resp.Diagnostics.AddError(e[0], e[1])
	}
}

// planBrowser fills what the configuration decides but doesn't write:
// languages left out (kept while locale stays, worked out again when it
// changes, gone without locale) and each upstream's has_auth / has_change_ip
// (true when the configuration gives the value, false when not, as proxySpec
// sends). An upstream the configuration doesn't write itself (lifecycle
// ignore_changes) keeps the stored flags. It returns the stored values this
// plan removes, for a warning.
func planBrowser(ctx context.Context, plan, cfg browserModel, state *browserModel) (browserModel, []string) {
	if cfg.Languages.IsNull() {
		switch {
		case plan.Locale.IsUnknown():
			plan.Languages = types.ListUnknown(types.StringType)
		case plan.Locale.IsNull():
			plan.Languages = types.ListNull(types.StringType)
		case state != nil && plan.Locale.Equal(state.Locale) && known(state.Languages):
			plan.Languages = state.Languages
		default:
			plan.Languages = types.ListUnknown(types.StringType)
		}
	}
	p := plan.proxy(ctx)
	if p == nil || !known(p.Upstream) {
		return plan, nil
	}
	if cfg.Proxy.IsNull() {
		// A plan with a proxy the configuration doesn't have: it is the
		// state's, kept by lifecycle ignore_changes. Nothing to work out.
		return plan, nil
	}
	given := upstreamsByName(cfg.proxy(ctx).upstreams(ctx))
	stored := map[string]upstreamModel{}
	if state != nil {
		stored = upstreamsByName(state.proxy(ctx).upstreams(ctx))
	}
	var removed []string
	vals := []attr.Value{}
	for _, u := range p.upstreams(ctx) {
		c, written := given[u.Name.ValueString()]
		if !u.Name.IsUnknown() && keptUpstream(c, written) {
			st := stored[u.Name.ValueString()]
			u.HasAuth = types.BoolValue(flagOf(c.HasAuth, st.HasAuth, u.HasAuth))
			u.HasChangeIP = types.BoolValue(flagOf(c.HasChangeIP, st.HasChangeIP, u.HasChangeIP))
			u.UsernameWO, u.PasswordWO, u.ChangeIPURLWO = types.StringNull(), types.StringNull(), types.StringNull()
			vals = append(vals, upstreamObject(u))
			continue
		}
		hasAuth := types.BoolValue(c.hasLogin())
		hasCIP := types.BoolValue(c.hasChangeIPURL())
		if c.UsernameWO.IsUnknown() || c.PasswordWO.IsUnknown() {
			hasAuth = types.BoolUnknown()
		}
		if c.ChangeIPURLWO.IsUnknown() {
			hasCIP = types.BoolUnknown()
		}
		if u.Name.IsUnknown() {
			hasAuth, hasCIP = types.BoolUnknown(), types.BoolUnknown()
		} else if st, ok := stored[u.Name.ValueString()]; ok {
			if st.HasAuth.ValueBool() && known(hasAuth) && !hasAuth.ValueBool() {
				removed = append(removed, fmt.Sprintf("a login for upstream %q", u.Name.ValueString()))
			}
			if st.HasChangeIP.ValueBool() && known(hasCIP) && !hasCIP.ValueBool() {
				removed = append(removed, fmt.Sprintf("a change-IP address for upstream %q", u.Name.ValueString()))
			}
		}
		u.HasAuth, u.HasChangeIP = hasAuth, hasCIP
		u.UsernameWO, u.PasswordWO, u.ChangeIPURLWO = types.StringNull(), types.StringNull(), types.StringNull()
		vals = append(vals, upstreamObject(u))
	}
	p.Upstream = types.ListValueMust(upstreamObjType, vals)
	plan.Proxy = proxyObject(*p)
	return plan, removed
}

func upstreamObject(u upstreamModel) attr.Value {
	return types.ObjectValueMust(upstreamAttrTypes, map[string]attr.Value{
		"name": u.Name, "server": u.Server,
		"username_wo": u.UsernameWO, "password_wo": u.PasswordWO, "change_ip_url_wo": u.ChangeIPURLWO,
		"change_ip_method": u.ChangeIPMethod, "min_change_ip_seconds": u.MinChangeIPSeconds,
		"has_auth": u.HasAuth, "has_change_ip": u.HasChangeIP,
	})
}

func proxyObject(p proxyModel) types.Object {
	return types.ObjectValueMust(proxyAttrTypes, map[string]attr.Value{
		"upstream": p.Upstream, "rotation": p.Rotation, "auth_version": p.AuthVersion, "check_url": p.CheckURL,
	})
}

func (r *browserResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan, cfg browserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *browserModel
	if !req.State.Raw.IsNull() {
		var s browserModel
		resp.Diagnostics.Append(req.State.Get(ctx, &s)...)
		if resp.Diagnostics.HasError() {
			return
		}
		state = &s
	}
	plan, removed := planBrowser(ctx, plan, cfg, state)
	for _, what := range removed {
		resp.Diagnostics.AddAttributeWarning(path.Root("proxy"), "Stored proxy value will be removed",
			fmt.Sprintf("The browser has %s stored, and the configuration doesn't give it: this apply removes it. "+
				"Set it in the upstream block (write-only) to keep one.", what))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, plan)...)
}

// readBrowser maps the platform's browser block into the model. Values the
// configuration left out read back as left out when the platform holds only
// its default; the write-only values are never read back.
//
// A newer setting (locale, timezone, languages, geolocation, proxy) is read
// only while the state holds it, that is while the configuration manages it,
// or when all is set (an import). A configuration that leaves one out leaves
// it to the console: what a person sets there is kept, never planned away,
// and an update sends nothing for it.
func readBrowser(ctx context.Context, prev browserModel, sp map[string]any, all bool) browserModel {
	m := prev
	m.Engine = readEngine(sp["engine"])
	if all || known(prev.Locale) {
		m.Locale = readSetting(sp["locale"])
	}
	if all || known(prev.Timezone) {
		m.Timezone = readSetting(sp["timezone"])
	}
	if all || known(prev.Languages) || known(prev.Locale) {
		m.Languages = readLanguages(sp["languages"])
	}
	if all || known(prev.Geolocation) {
		m.Geolocation = readGeo(prev.geo(ctx), sp["geolocation"])
	}
	if all || known(prev.Proxy) {
		m.Proxy = readProxy(ctx, prev.proxy(ctx), sp["proxy"])
	}
	if b, ok := sp["profilesReady"].(bool); ok {
		m.ProfilesReady = types.BoolValue(b)
	}
	refreshPlacement(sp, &m.PlacementStrategy, &m.PlacementHost, &m.PlacementRegion)
	return m
}

// readLanguages: the platform's languages, null when it holds none.
func readLanguages(v any) types.List {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return types.ListNull(types.StringType)
	}
	vals := make([]attr.Value, 0, len(raw))
	for _, l := range raw {
		if s, ok := l.(string); ok {
			vals = append(vals, types.StringValue(s))
		}
	}
	return types.ListValueMust(types.StringType, vals)
}

// readSetting: a string setting, null when the platform holds none.
func readSetting(raw any) types.String {
	if s, _ := raw.(string); s != "" {
		return types.StringValue(s)
	}
	return types.StringNull()
}

func number(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

// readBrowserDefault reads a string the platform fills with def when it was left
// out: that default reads back as left out when it was.
func readBrowserDefault(prev types.String, raw any, def string) types.String {
	s, _ := raw.(string)
	if s == "" || (s == def && !known(prev)) {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// readBrowserDefaultInt is readBrowserDefault for a number; 0 is none.
func readBrowserDefaultInt(prev types.Int64, raw any, def int64) types.Int64 {
	f, ok := number(raw)
	if !ok || f == 0 || (int64(f) == def && !known(prev)) {
		return types.Int64Null()
	}
	return types.Int64Value(int64(f))
}

func readGeo(prev *geoModel, raw any) types.Object {
	g, _ := raw.(map[string]any)
	mode, _ := g["mode"].(string)
	if mode == "" || mode == "prompt" {
		return types.ObjectNull(geoAttrTypes)
	}
	if prev == nil {
		prev = &geoModel{}
	}
	v := map[string]attr.Value{
		"mode":      types.StringValue(mode),
		"latitude":  types.Float64Null(),
		"longitude": types.Float64Null(),
		"accuracy":  types.Int64Null(),
	}
	if mode == "fixed" {
		if f, ok := number(g["latitude"]); ok {
			v["latitude"] = types.Float64Value(f)
		}
		if f, ok := number(g["longitude"]); ok {
			v["longitude"] = types.Float64Value(f)
		}
		v["accuracy"] = readBrowserDefaultInt(prev.Accuracy, g["accuracy"], defaultGeoAccuracy)
	}
	return types.ObjectValueMust(geoAttrTypes, v)
}

func readProxy(ctx context.Context, prev *proxyModel, raw any) types.Object {
	p, ok := raw.(map[string]any)
	if !ok || p == nil {
		return types.ObjectNull(proxyAttrTypes)
	}
	before := upstreamsByName(prev.upstreams(ctx))
	ups, _ := p["upstreams"].([]any)
	vals := make([]attr.Value, 0, len(ups))
	for _, e := range ups {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name, _ := em["name"].(string)
		server, _ := em["server"].(string)
		hasAuth, _ := em["hasAuth"].(bool)
		hasCIP, _ := em["hasChangeIp"].(bool)
		was := before[name]
		vals = append(vals, upstreamObject(upstreamModel{
			Name: types.StringValue(name), Server: types.StringValue(server),
			UsernameWO: types.StringNull(), PasswordWO: types.StringNull(), ChangeIPURLWO: types.StringNull(),
			ChangeIPMethod:     readBrowserDefault(was.ChangeIPMethod, em["changeIpMethod"], defaultChangeIPMethod),
			MinChangeIPSeconds: readBrowserDefaultInt(was.MinChangeIPSeconds, em["minChangeIpSeconds"], defaultMinChangeIPSecs),
			HasAuth:            types.BoolValue(hasAuth),
			HasChangeIP:        types.BoolValue(hasCIP),
		}))
	}
	// No upstream block and an empty list of them read the same way: as the
	// configuration had it.
	upList := types.ListValueMust(upstreamObjType, vals)
	if len(vals) == 0 && (prev == nil || prev.Upstream.IsNull()) {
		upList = types.ListNull(upstreamObjType)
	}
	out := proxyModel{
		Upstream:    upList,
		Rotation:    readRotation(prev.rotation(ctx), p["rotation"]),
		AuthVersion: types.Int64Null(),
		CheckURL:    readSetting(p["checkUrl"]),
	}
	if prev != nil {
		out.AuthVersion = prev.AuthVersion // the platform doesn't hold it
	}
	return proxyObject(out)
}

func readRotation(prev *rotationModel, raw any) types.Object {
	r, _ := raw.(map[string]any)
	mode, _ := r["mode"].(string)
	order, _ := r["order"].(string)
	every, _ := number(r["everyMinutes"])
	if prev == nil && (mode == "" || mode == defaultRotationMode) && (order == "" || order == defaultRotationOrder) && every == 0 {
		return types.ObjectNull(rotationAttrTypes)
	}
	if prev == nil {
		prev = &rotationModel{}
	}
	everyV := types.Int64Null()
	if every > 0 {
		everyV = types.Int64Value(int64(every))
	}
	return types.ObjectValueMust(rotationAttrTypes, map[string]attr.Value{
		"mode":          readBrowserDefault(prev.Mode, r["mode"], defaultRotationMode),
		"every_minutes": everyV,
		"order":         readBrowserDefault(prev.Order, r["order"], defaultRotationOrder),
	})
}

// applied is the state after a create or update: the plan, with what only the
// platform knows filled in (languages that follow locale, profiles_ready,
// ready) and no write-only value. A Camoufox engine the platform didn't keep
// is an error, and the state holds the engine the platform made.
func (r *browserResource) applied(ctx context.Context, plan browserModel, diags interface{ AddError(string, string) }) browserModel {
	m := plan
	var fromSpec *bool
	if ws, err := r.data.Client.Workloads(ctx); err == nil {
		if w := findWorkload(ws, plan.Name.ValueString()); w != nil {
			var msg string
			if m.Engine, msg = engineKept(plan.Engine, w.Browser["engine"], plan.Name.ValueString()); msg != "" {
				diags.AddError("Camoufox isn't offered", msg)
			}
			read := readBrowser(ctx, plan, w.Browser, true)
			if plan.Languages.IsUnknown() {
				m.Languages = read.Languages
			}
			if b, ok := w.Browser["profilesReady"].(bool); ok {
				fromSpec = &b
			}
		}
	}
	st, _ := statusOf(ctx, r.data.Client, plan.Name.ValueString())
	m.Ready = types.BoolValue(st != nil && st.Ready)
	m.ProfilesReady = profilesReady(fromSpec, st)
	if m.Languages.IsUnknown() {
		m.Languages = types.ListNull(types.StringType)
	}
	m.Proxy = withoutSecrets(ctx, m.Proxy)
	return m
}

// profilesReady: what the browser's settings say, else its status row.
func profilesReady(fromSpec *bool, st *client.WorkloadStatus) types.Bool {
	if fromSpec != nil {
		return types.BoolValue(*fromSpec)
	}
	return types.BoolValue(st != nil && st.ProfilesReady)
}

// withoutSecrets clears the write-only values before the model goes into state.
func withoutSecrets(ctx context.Context, o types.Object) types.Object {
	if !known(o) {
		return o
	}
	var p proxyModel
	o.As(ctx, &p, objectAsOpts)
	if !known(p.Upstream) {
		return o
	}
	vals := []attr.Value{}
	for _, u := range p.upstreams(ctx) {
		u.UsernameWO, u.PasswordWO, u.ChangeIPURLWO = types.StringNull(), types.StringNull(), types.StringNull()
		vals = append(vals, upstreamObject(u))
	}
	p.Upstream = types.ListValueMust(upstreamObjType, vals)
	return proxyObject(p)
}

func (r *browserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, cfg browserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := browserSpec(ctx, plan, cfg, nil)
	body["id"] = plan.Name.ValueString()
	reach := reachOf(ctx, cfg.ReachableFrom)
	reachBody(body, reach)
	if err := r.data.Client.CreateWorkload(ctx, "browser", body); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot create browser", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, plan.Name.ValueString(), false); err != nil {
		resp.Diagnostics.AddError("Browser did not become ready", err.Error())
	}
	plan = r.applied(ctx, plan, &resp.Diagnostics)
	plan.ReachableFrom = settleReach(ctx, r.data.Client, plan.Name.ValueString(), plan.ReachableFrom, reach, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state browserModel
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
	if w == nil || w.Type != "browser" {
		resp.State.RemoveResource(ctx)
		return
	}
	imported := false
	if b, d := req.Private.GetKey(ctx, importedKey); !d.HasError() && len(b) > 0 {
		imported = true
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, nil)...)
	}
	state = readBrowser(ctx, state, w.Browser, imported)
	state.ReachableFrom = readReach(ctx, state.ReachableFrom, w.ReachableFrom)
	st, _ := statusOf(ctx, r.data.Client, state.Name.ValueString())
	state.Ready = types.BoolValue(st != nil && st.Ready)
	var fromSpec *bool
	if b, ok := w.Browser["profilesReady"].(bool); ok {
		fromSpec = &b
	}
	state.ProfilesReady = profilesReady(fromSpec, st)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *browserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, cfg, state browserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	reach := reachOf(ctx, cfg.ReachableFrom)
	w := client.Workload{
		ID:            plan.Name.ValueString(),
		Type:          "browser",
		Browser:       browserSpec(ctx, plan, cfg, &state),
		ReachableFrom: reach,
	}
	if err := r.data.Client.UpdateWorkload(ctx, w.ID, w); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot update browser", err)
		return
	}
	createTimeout, d := plan.Timeouts.Create(ctx, 10*time.Minute)
	resp.Diagnostics.Append(d...)
	waitCtx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	if err := waitReady(waitCtx, r.data.Client, w.ID, false); err != nil {
		resp.Diagnostics.AddError("Browser did not become ready after update", err.Error())
	}
	plan = r.applied(ctx, plan, &resp.Diagnostics)
	plan.ReachableFrom = settleReach(ctx, r.data.Client, w.ID, plan.ReachableFrom, reach, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *browserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state browserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.data.Client.DeleteWorkload(ctx, state.Name.ValueString()); err != nil {
		apiDiag(&resp.Diagnostics, "Cannot delete browser", err)
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

// importedKey marks a state just imported: its first read takes every
// setting the platform holds, so the plan shows what the configuration lacks.
const importedKey = "imported"

func (r *browserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, importedKey, []byte(`true`))...)
}
