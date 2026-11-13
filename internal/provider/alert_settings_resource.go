package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// livellm_alert_settings — which alerts one resource gets and at what level.
// The settings belong to the resource on the platform; this Terraform
// resource holds only the kinds its configuration names, so the platform's
// usual for every other kind never shows as a change.
type alertSettingsResource struct {
	data *providerData
}

func NewAlertSettingsResource() resource.Resource {
	return &alertSettingsResource{}
}

func (r *alertSettingsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_settings"
}

// alertKinds are the kinds of alert, in the platform's order. down and gpu
// are on or off; cpu, memory and disk usual, off or a level.
var alertKinds = []string{"down", "cpu", "memory", "disk", "gpu"}

func onOffKind(kind string) bool { return kind == "down" || kind == "gpu" }

// alertLevelRe is a level as written: a whole percent from 50 to 99.
var alertLevelRe = regexp.MustCompile(`^(usual|off|[5-9][0-9])$`)

func (r *alertSettingsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	onOff := func(what string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true,
			Description: fmt.Sprintf("%s: \"on\" (the platform's usual) or \"off\". Left out, Terraform leaves it as "+
				"it is; removing it later puts it back to on.", what),
			Validators: []validator.String{stringvalidator.OneOf("on", "off")},
		}
	}
	level := func(what string) schema.StringAttribute {
		return schema.StringAttribute{
			Optional: true,
			Description: fmt.Sprintf("%s: \"usual\" (the platform's level), \"off\", or the resource's own level, a "+
				"whole percent from 50 to 99 written as a string (\"95\"). Left out, Terraform leaves it as it is; "+
				"removing it later puts it back to usual.", what),
			Validators: []validator.String{stringvalidator.RegexMatches(alertLevelRe,
				`must be "usual", "off" or a whole percent from 50 to 99, e.g. "95"`)},
		}
	}
	resp.Schema = schema.Schema{
		Description: "Which alerts one resource of the workspace gets (Monitoring) and at what level. " +
			"Only the kinds written here are managed.",
		Attributes: map[string]schema.Attribute{
			"resource": schema.StringAttribute{
				Required:    true,
				Description: "The resource's name (its workload id), e.g. livellm_vm.dev.name. Changing it replaces this.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"down":   onOff("An alert when the resource goes down"),
			"gpu":    onOff("An alert when a GPU the resource uses stops working (only for a resource that uses a GPU)"),
			"cpu":    level("An alert when CPU use stays at or over the level for 5 minutes"),
			"memory": level("An alert when memory use stays at or over the level for 5 minutes"),
			"disk": level("An alert when a disk is at or over the level (only for a resource that keeps disks: " +
				"a machine, a database or object storage, a browser, an app with volumes, a Desktop App that keeps its files)"),
		},
	}
}

func (r *alertSettingsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

type alertSettingsModel struct {
	Resource types.String `tfsdk:"resource"`
	Down     types.String `tfsdk:"down"`
	CPU      types.String `tfsdk:"cpu"`
	Memory   types.String `tfsdk:"memory"`
	Disk     types.String `tfsdk:"disk"`
	GPU      types.String `tfsdk:"gpu"`
}

// kind is the model's attribute for one kind of alert.
func (m *alertSettingsModel) kind(k string) *types.String {
	switch k {
	case "down":
		return &m.Down
	case "cpu":
		return &m.CPU
	case "memory":
		return &m.Memory
	case "disk":
		return &m.Disk
	case "gpu":
		return &m.GPU
	}
	return nil
}

// alertSettingOf is what a written value asks the platform for.
func alertSettingOf(v string) client.AlertSetting {
	switch v {
	case "off":
		return client.AlertSetting{Mode: "off"}
	case "on", "usual", "":
		return client.AlertSetting{Mode: "usual"}
	}
	n, _ := strconv.Atoi(v)
	return client.AlertSetting{Mode: "level", Level: n}
}

// alertValueOf is how a platform setting is written: on/off for down and
// gpu; usual, off or the level for the others.
func alertValueOf(kind string, s client.AlertSetting) string {
	switch s.Mode {
	case "off":
		return "off"
	case "level":
		return strconv.Itoa(s.Level)
	}
	if onOffKind(kind) {
		return "on"
	}
	return "usual"
}

// alertWrite is the PUT body that takes the platform from what state holds
// (prior; nil on a create) to what the plan says: every kind the plan names,
// and as usual every kind the plan no longer names that state still held.
// Nothing else is named, so a kind Terraform never managed keeps what it has.
func alertWrite(plan alertSettingsModel, prior *alertSettingsModel) map[string]client.AlertSetting {
	out := map[string]client.AlertSetting{}
	for _, k := range alertKinds {
		if v := plan.kind(k); !v.IsNull() && !v.IsUnknown() {
			out[k] = alertSettingOf(v.ValueString())
			continue
		}
		if prior != nil {
			if was := prior.kind(k); !was.IsNull() && !was.IsUnknown() {
				out[k] = client.AlertSetting{Mode: "usual"}
			}
		}
	}
	return out
}

// alertDropped are the kinds a write sends only because the configuration
// no longer names them.
func alertDropped(plan alertSettingsModel, body map[string]client.AlertSetting) []string {
	var out []string
	for _, k := range alertKinds {
		if _, ok := body[k]; ok && plan.kind(k).IsNull() {
			out = append(out, k)
		}
	}
	return out
}

// readAlertSettings fills each kind the model holds from the platform's
// settings, so a setting changed elsewhere (or put back to usual when the
// resource was made again) shows as a change the next apply restores. Kinds
// the model doesn't hold stay null: what Terraform doesn't manage never
// shows. A kind that no longer applies to the resource keeps what the model
// holds. With all set (an import), every kind that isn't usual is taken.
func readAlertSettings(m *alertSettingsModel, s *client.AlertSettings, all bool) {
	for _, k := range alertKinds {
		v := m.kind(k)
		got, ok := s.Settings[k]
		if !ok {
			continue
		}
		if !v.IsNull() || (all && got.Mode != "usual" && got.Mode != "") {
			*v = types.StringValue(alertValueOf(k, got))
		}
	}
}

// alertDiag turns an alert-settings error into a diagnostic: the platform's
// refusal (422 alert_settings) on the attribute its field names, a platform
// without the route, a 403 in the API's words, else the usual apiDiag.
func alertDiag(diags *diag.Diagnostics, summary string, err error) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		diags.AddError(summary, err.Error())
		return
	}
	switch {
	case apiErr.RouteMissing():
		diags.AddError("Alert settings aren't available on this server",
			fmt.Sprintf("%s: this LiveLLM server has no alert settings (they need LiveLLM API 0.101.5 or newer).", summary))
		return
	case apiErr.Status == 422 && apiErr.Code() == "alert_settings":
		field := alertField(apiErr)
		detail := fmt.Sprintf("%s: %s", summary, apiErr.Message())
		if field != "" {
			detail += fmt.Sprintf(" (field %s)", field)
		}
		if k := alertFieldKind(field); k != "" {
			diags.AddAttributeError(path.Root(k), "Alert setting refused", detail)
		} else {
			diags.AddError("Alert setting refused", detail)
		}
		return
	case apiErr.Status == 403:
		diags.AddError(summary, fmt.Sprintf("%s: %s\n\nOnly the workspace's owners, or a workspace key, can change "+
			"alert settings: a member's key and an agent sign-in only read them.", summary, apiErr.Message()))
		return
	}
	apiDiag(diags, summary, err)
}

// alertField is the part of the request a 422 names ("settings.cpu.level").
func alertField(e *client.APIError) string {
	var b struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal([]byte(e.Body), &b)
	return b.Field
}

// alertFieldKind is the attribute a refused field belongs to ("cpu" for
// "settings.cpu.level"), or "" when it names no kind of this resource.
func alertFieldKind(field string) string {
	parts := strings.Split(field, ".")
	if len(parts) < 2 || parts[0] != "settings" {
		return ""
	}
	for _, k := range alertKinds {
		if parts[1] == k {
			return k
		}
	}
	return ""
}

// resourceGone says whether an error is the platform answering that the
// resource itself isn't there (its route's own 404), not a missing route.
func resourceGone(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 404 && !apiErr.RouteMissing()
}

func (r *alertSettingsResource) write(ctx context.Context, plan alertSettingsModel, prior *alertSettingsModel, diags *diag.Diagnostics, summary string) (alertSettingsModel, bool) {
	id := plan.Resource.ValueString()
	body := alertWrite(plan, prior)
	var s *client.AlertSettings
	var err error
	if dropped := alertDropped(plan, body); len(dropped) > 0 {
		// A kind dropped from the configuration goes back to usual only where
		// it still applies: the platform refuses even usual for a kind that
		// no longer does (a GPU or the volumes taken away), and that kind has
		// no setting left to put back.
		if s, err = r.data.Client.AlertSettings(ctx, id); err == nil {
			applies := map[string]bool{}
			for _, k := range s.Kinds {
				applies[k] = true
			}
			for _, k := range dropped {
				if !applies[k] {
					delete(body, k)
				}
			}
		}
	}
	switch {
	case err != nil:
	case len(body) == 0 && s == nil:
		// Nothing to change: the read checks the resource and the route.
		s, err = r.data.Client.AlertSettings(ctx, id)
	case len(body) == 0:
	default:
		s, err = r.data.Client.SetAlertSettings(ctx, id, body)
	}
	if err != nil {
		if resourceGone(err) {
			diags.AddAttributeError(path.Root("resource"), summary,
				fmt.Sprintf("%s: the workspace has no resource %q.", summary, id))
			return plan, false
		}
		alertDiag(diags, summary, err)
		return plan, false
	}
	readAlertSettings(&plan, s, false)
	return plan, true
}

func (r *alertSettingsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan, ok := r.write(ctx, plan, nil, &resp.Diagnostics, "Cannot set alert settings")
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// importKey marks a state that comes from an import: its first read takes
// every kind that isn't usual.
const importKey = "imported"

func (r *alertSettingsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state alertSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.data.Client.AlertSettings(ctx, state.Resource.ValueString())
	if err != nil {
		if resourceGone(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		alertDiag(&resp.Diagnostics, "Cannot read alert settings", err)
		return
	}
	imported, d := req.Private.GetKey(ctx, importKey)
	resp.Diagnostics.Append(d...)
	readAlertSettings(&state, s, len(imported) > 0)
	if len(imported) > 0 {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, importKey, nil)...)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *alertSettingsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior alertSettingsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan, ok := r.write(ctx, plan, &prior, &resp.Diagnostics, "Cannot change alert settings")
	if !ok {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// alertReset is the PUT body that puts every kind that applies back to usual.
func alertReset(s *client.AlertSettings) map[string]client.AlertSetting {
	out := map[string]client.AlertSetting{}
	for _, k := range s.Kinds {
		out[k] = client.AlertSetting{Mode: "usual"}
	}
	return out
}

// Delete puts every kind of alert that applies to the resource back to
// usual. A resource already gone has nothing left to put back.
func (r *alertSettingsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state alertSettingsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.Resource.ValueString()
	s, err := r.data.Client.AlertSettings(ctx, id)
	if err == nil {
		if body := alertReset(s); len(body) > 0 {
			_, err = r.data.Client.SetAlertSettings(ctx, id, body)
		}
	}
	if err == nil || resourceGone(err) {
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.RouteMissing() {
		// A platform without alert settings holds none to put back.
		resp.Diagnostics.AddWarning("Alert settings aren't available on this server",
			"Nothing was put back: this LiveLLM server has no alert settings.")
		return
	}
	alertDiag(&resp.Diagnostics, "Cannot put alert settings back to usual", err)
}

func (r *alertSettingsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource"), req.ID)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, importKey, []byte(`true`))...)
}
