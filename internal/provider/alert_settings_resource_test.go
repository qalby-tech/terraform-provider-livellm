package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// fakeAlerts is a platform's alert settings for one resource "dev" (a
// machine: down, cpu, memory and disk apply), with what each PUT asked.
type fakeAlerts struct {
	mu       sync.Mutex
	settings map[string]client.AlertSetting
	puts     []map[string]client.AlertSetting
	gone     bool // the resource isn't there (the route's own 404)
	noRoute  bool // an API without the route (the router's plain 404)
	refuse   string
}

func newFakeAlerts() *fakeAlerts {
	f := &fakeAlerts{settings: map[string]client.AlertSetting{}}
	for _, k := range []string{"down", "cpu", "memory", "disk"} {
		f.settings[k] = client.AlertSetting{Mode: "usual"}
	}
	return f
}

func (f *fakeAlerts) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noRoute || r.URL.Path != "/v1/workloads/dev/alert-settings" {
		http.NotFound(w, r)
		return
	}
	if f.gone {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"workload not found: dev"}`))
		return
	}
	if r.Method == http.MethodPut {
		if f.refuse != "" {
			w.WriteHeader(f.refusalStatus())
			_, _ = w.Write([]byte(f.refuse))
			return
		}
		var body struct {
			Settings map[string]client.AlertSetting `json:"settings"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for k := range body.Settings {
			if _, ok := f.settings[k]; !ok {
				w.WriteHeader(422)
				_, _ = w.Write([]byte(`{"error":"` + k + ` alerts don't apply to dev","code":"alert_settings","field":"settings.` + k + `"}`))
				return
			}
		}
		f.puts = append(f.puts, body.Settings)
		for k, s := range body.Settings {
			f.settings[k] = s
		}
	}
	_ = json.NewEncoder(w).Encode(client.AlertSettings{
		Usual:    map[string]float64{"cpu": 90, "memory": 90, "disk": 90},
		Kinds:    f.kinds(),
		Settings: f.settings,
	})
}

func (f *fakeAlerts) kinds() []string {
	var out []string
	for _, k := range alertKinds {
		if _, ok := f.settings[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func (f *fakeAlerts) refusalStatus() int {
	if strings.Contains(f.refuse, "alert_settings") {
		return 422
	}
	return 403
}

type alertVals map[string]any

// alertState is the resource's schema with the given values (the rest null).
func alertRaw(t *testing.T, v alertVals) (tftypes.Value, resource.SchemaResponse) {
	t.Helper()
	ctx := context.Background()
	var sr resource.SchemaResponse
	NewAlertSettingsResource().Schema(ctx, resource.SchemaRequest{}, &sr)
	typ := sr.Schema.Type().TerraformType(ctx).(tftypes.Object)
	vals := map[string]tftypes.Value{}
	for name := range typ.AttributeTypes {
		var x any
		if s, ok := v[name]; ok {
			x = s
		}
		vals[name] = tftypes.NewValue(tftypes.String, x)
	}
	return tftypes.NewValue(typ, vals), sr
}

func alertResourceFor(srv *httptest.Server) *alertSettingsResource {
	return &alertSettingsResource{data: &providerData{Client: client.New(srv.URL, "llc_test")}}
}

func alertModelOf(t *testing.T, st tfsdk.State) alertSettingsModel {
	t.Helper()
	var m alertSettingsModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}

// The whole life: a create names only the kinds written; a read holds only
// those (the platform's usual for the others never shows); a setting the
// platform put back to usual (the machine made again) shows as a change; an
// update that drops a kind sends it as usual; a delete puts every kind that
// applies back to usual.
func TestAlertSettingsLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFakeAlerts()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := alertResourceFor(srv)

	plan, sr := alertRaw(t, alertVals{"resource": "dev", "cpu": "95", "disk": "off"})
	cresp := resource.CreateResponse{State: tfsdk.State{Schema: sr.Schema, Raw: plan}}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: plan}}, &cresp)
	if cresp.Diagnostics.HasError() {
		t.Fatal(cresp.Diagnostics)
	}
	if len(f.puts) != 1 || len(f.puts[0]) != 2 || f.puts[0]["cpu"] != (client.AlertSetting{Mode: "level", Level: 95}) ||
		f.puts[0]["disk"] != (client.AlertSetting{Mode: "off"}) {
		t.Fatalf("create sent %+v", f.puts)
	}
	m := alertModelOf(t, cresp.State)
	if m.CPU.ValueString() != "95" || m.Disk.ValueString() != "off" || !m.Down.IsNull() || !m.Memory.IsNull() || !m.GPU.IsNull() {
		t.Fatalf("state after create %+v", m)
	}

	read := func() alertSettingsModel {
		t.Helper()
		rresp := resource.ReadResponse{State: cresp.State}
		r.Read(ctx, resource.ReadRequest{State: cresp.State}, &rresp)
		if rresp.Diagnostics.HasError() {
			t.Fatal(rresp.Diagnostics)
		}
		return alertModelOf(t, rresp.State)
	}
	if got := read(); got != m {
		t.Errorf("read changed nothing on the platform but got %+v", got)
	}

	// The machine was made again: the platform put cpu back to usual.
	f.settings["cpu"] = client.AlertSetting{Mode: "usual"}
	if got := read(); got.CPU.ValueString() != "usual" || got.Disk.ValueString() != "off" {
		t.Errorf("drift not shown: %+v", got)
	}

	// cpu dropped from the configuration, memory added.
	f.settings["cpu"] = client.AlertSetting{Mode: "level", Level: 95}
	next, _ := alertRaw(t, alertVals{"resource": "dev", "memory": "80", "disk": "off"})
	uresp := resource.UpdateResponse{State: cresp.State}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: next}, State: cresp.State}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	last := f.puts[len(f.puts)-1]
	if len(last) != 3 || last["cpu"].Mode != "usual" || last["memory"] != (client.AlertSetting{Mode: "level", Level: 80}) || last["disk"].Mode != "off" {
		t.Errorf("update sent %+v", last)
	}
	if got := alertModelOf(t, uresp.State); !got.CPU.IsNull() || got.Memory.ValueString() != "80" {
		t.Errorf("state after update %+v", got)
	}

	dresp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: uresp.State}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Fatal(dresp.Diagnostics)
	}
	last = f.puts[len(f.puts)-1]
	if len(last) != 4 {
		t.Errorf("delete sent %+v, want every kind that applies", last)
	}
	for k, s := range f.settings {
		if s.Mode != "usual" {
			t.Errorf("%s left %+v after delete", k, s)
		}
	}
}

// down and gpu are written on and off; the others usual, off or a level.
func TestAlertValues(t *testing.T) {
	for _, c := range []struct {
		kind, v string
		s       client.AlertSetting
	}{
		{"down", "on", client.AlertSetting{Mode: "usual"}},
		{"gpu", "off", client.AlertSetting{Mode: "off"}},
		{"cpu", "usual", client.AlertSetting{Mode: "usual"}},
		{"memory", "50", client.AlertSetting{Mode: "level", Level: 50}},
		{"disk", "99", client.AlertSetting{Mode: "level", Level: 99}},
	} {
		if got := alertSettingOf(c.v); got != c.s {
			t.Errorf("%s %q sends %+v, want %+v", c.kind, c.v, got, c.s)
		}
		if got := alertValueOf(c.kind, c.s); got != c.v {
			t.Errorf("%s %+v reads %q, want %q", c.kind, c.s, got, c.v)
		}
	}
	for v, ok := range map[string]bool{"usual": true, "off": true, "50": true, "95": true, "99": true,
		"49": false, "100": false, "095": false, "9": false, "on": false, "95%": false, "": false} {
		if alertLevelRe.MatchString(v) != ok {
			t.Errorf("level %q valid = %v, want %v", v, !ok, ok)
		}
	}
}

// An import takes every kind that isn't usual; a plain read only the kinds
// state holds.
func TestReadAlertSettingsImport(t *testing.T) {
	s := &client.AlertSettings{Settings: map[string]client.AlertSetting{
		"down": {Mode: "off"}, "cpu": {Mode: "usual"}, "memory": {Mode: "level", Level: 97}, "disk": {Mode: "usual"},
	}}
	var m alertSettingsModel
	readAlertSettings(&m, s, true)
	if m.Down.ValueString() != "off" || m.Memory.ValueString() != "97" || !m.CPU.IsNull() || !m.Disk.IsNull() || !m.GPU.IsNull() {
		t.Errorf("import read %+v", m)
	}
	var plain alertSettingsModel
	readAlertSettings(&plain, s, false)
	if plain != (alertSettingsModel{}) {
		t.Errorf("a read with nothing held took %+v", plain)
	}
}

// A resource gone leaves state; an API without the route, a refusal of a
// field and a 403 are said plainly.
func TestAlertSettingsErrors(t *testing.T) {
	ctx := context.Background()
	f := newFakeAlerts()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := alertResourceFor(srv)
	raw, sr := alertRaw(t, alertVals{"resource": "dev", "cpu": "95"})
	state := tfsdk.State{Schema: sr.Schema, Raw: raw}

	f.gone = true
	rresp := resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &rresp)
	if rresp.Diagnostics.HasError() || !rresp.State.Raw.IsNull() {
		t.Errorf("a resource gone: diags %v, state %v", rresp.Diagnostics, rresp.State.Raw)
	}
	dresp := resource.DeleteResponse{}
	r.Delete(ctx, resource.DeleteRequest{State: state}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Errorf("deleting the settings of a resource gone: %v", dresp.Diagnostics)
	}
	cresp := resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw}}, &cresp)
	if !hasError(cresp.Diagnostics, "no resource \"dev\"") {
		t.Errorf("create on a resource gone: %v", cresp.Diagnostics)
	}

	f.gone, f.noRoute = false, true
	rresp = resource.ReadResponse{State: state}
	r.Read(ctx, resource.ReadRequest{State: state}, &rresp)
	if !hasSummary(rresp.Diagnostics, "Alert settings aren't available on this server") {
		t.Errorf("no route: %v", rresp.Diagnostics)
	}

	f.noRoute = false
	f.refuse = `{"error":"disk alerts don't apply to dev","code":"alert_settings","field":"settings.disk"}`
	cresp = resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw}}, &cresp)
	if len(cresp.Diagnostics) != 1 || !cresp.Diagnostics[0].(diag.DiagnosticWithPath).Path().Equal(path.Root("disk")) ||
		!strings.Contains(cresp.Diagnostics[0].Detail(), "settings.disk") {
		t.Errorf("422 alert_settings: %v", cresp.Diagnostics)
	}

	f.refuse = `{"error":"an agent sign-in can't change alert settings"}`
	cresp = resource.CreateResponse{State: state}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: raw}}, &cresp)
	if !hasError(cresp.Diagnostics, "an agent sign-in can't change alert settings") {
		t.Errorf("403: %v", cresp.Diagnostics)
	}
}

// A kind dropped from the configuration that no longer applies (the GPU
// taken away) is not sent: the platform refuses even usual for it, and the
// rest of the change goes through.
func TestAlertSettingsDropAKindThatNoLongerApplies(t *testing.T) {
	ctx := context.Background()
	f := newFakeAlerts()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := alertResourceFor(srv)
	prior, sr := alertRaw(t, alertVals{"resource": "dev", "gpu": "off", "cpu": "95"})
	next, _ := alertRaw(t, alertVals{"resource": "dev", "cpu": "90"})
	state := tfsdk.State{Schema: sr.Schema, Raw: prior}
	uresp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: next}, State: state}, &uresp)
	if uresp.Diagnostics.HasError() {
		t.Fatal(uresp.Diagnostics)
	}
	if len(f.puts) != 1 || len(f.puts[0]) != 1 || f.puts[0]["cpu"].Level != 90 {
		t.Errorf("sent %+v, want cpu alone", f.puts)
	}
	// only a dropped kind, and it no longer applies: nothing is sent
	f.puts = nil
	prior2, _ := alertRaw(t, alertVals{"resource": "dev", "gpu": "off"})
	next2, _ := alertRaw(t, alertVals{"resource": "dev"})
	state = tfsdk.State{Schema: sr.Schema, Raw: prior2}
	uresp = resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{Plan: tfsdk.Plan{Schema: sr.Schema, Raw: next2}, State: state}, &uresp)
	if uresp.Diagnostics.HasError() || len(f.puts) != 0 {
		t.Errorf("diags %v, sent %+v", uresp.Diagnostics, f.puts)
	}
}

func TestAlertFieldKind(t *testing.T) {
	for field, want := range map[string]string{"settings.cpu.level": "cpu", "settings.gpu": "gpu", "settings.foo": "", "settings": "", "": ""} {
		if got := alertFieldKind(field); got != want {
			t.Errorf("%q → %q, want %q", field, got, want)
		}
	}
}

func hasSummary(d diag.Diagnostics, s string) bool {
	for _, x := range d {
		if x.Summary() == s {
			return true
		}
	}
	return false
}

func hasError(d diag.Diagnostics, s string) bool {
	for _, x := range d {
		if strings.Contains(x.Detail(), s) {
			return true
		}
	}
	return false
}
