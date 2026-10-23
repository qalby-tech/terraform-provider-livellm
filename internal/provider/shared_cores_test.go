package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dsschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// dedicated_cores on an app, a Desktop App, a database, a browser and a
// Browser API: optional and computed, planned with keepCoreChoice (never a
// Default, which would send false on every save that leaves it out and turn
// off what the console set).
func TestDedicatedCoresAttributes(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]func() resource.Resource{
		"container_app": NewContainerAppResource,
		"desktop_app":   NewDesktopAppResource,
		"storage":       NewStorageResource,
		"browser":       NewBrowserResource,
		"browser_api":   NewBrowserAPIResource,
	} {
		var resp resource.SchemaResponse
		r().Schema(ctx, resource.SchemaRequest{}, &resp)
		v, ok := resp.Schema.Attributes["dedicated_cores"].(schema.BoolAttribute)
		if !ok || !v.Optional || !v.Computed || v.Default != nil || len(v.PlanModifiers) != 1 {
			t.Errorf("%s.dedicated_cores: %+v", name, resp.Schema.Attributes["dedicated_cores"])
			continue
		}
		if _, ok := v.PlanModifiers[0].(keepCoreChoice); !ok {
			t.Errorf("%s.dedicated_cores: plan modifier %T", name, v.PlanModifiers[0])
		}
	}
}

// A known value is sent, false included (that clears it); an unknown one is
// left out. Absent on the platform reads as false.
func TestDedicatedCoresSpecAndRead(t *testing.T) {
	for _, c := range []struct {
		v    types.Bool
		want any
	}{{types.BoolValue(true), true}, {types.BoolValue(false), false}, {types.BoolUnknown(), nil}, {types.BoolNull(), nil}} {
		spec := map[string]any{}
		dedicatedSpec(c.v, spec)
		if spec["dedicatedCores"] != c.want {
			t.Errorf("dedicatedSpec(%v) = %v, want %v", c.v, spec["dedicatedCores"], c.want)
		}
	}
	if !readDedicated(map[string]any{"dedicatedCores": true}).ValueBool() || readDedicated(map[string]any{}).ValueBool() || readDedicated(nil).ValueBool() {
		t.Error("readDedicated")
	}
	if got := settleDedicated(types.BoolUnknown(), map[string]any{"dedicatedCores": true}); !got.ValueBool() {
		t.Errorf("settle unknown from the platform: %v", got)
	}
	if got := settleDedicated(types.BoolValue(false), map[string]any{"dedicatedCores": true}); got.ValueBool() {
		t.Errorf("settle keeps a planned value: %v", got)
	}
	if got := settleDedicated(types.BoolUnknown(), nil); got.IsUnknown() || got.ValueBool() {
		t.Errorf("settle unread: %v", got)
	}
	m := desktopAppModel{CPU: types.StringValue("1"), DedicatedCores: types.BoolValue(true)}
	if got := desktopSpec(m); got["dedicatedCores"] != true {
		t.Errorf("desktop spec: %v", got)
	}
}

// An object storage has no dedicated cores: refused at plan time, and never
// sent.
func TestStorageDedicatedCores(t *testing.T) {
	s3 := baseStorage("s3")
	s3.DedicatedCores = types.BoolValue(true)
	if got := storageConfigErrors(s3); len(got) != 1 || got[0][0] != "Object storage has no dedicated cores" {
		t.Errorf("s3 with dedicated cores: %v", got)
	}
	s3.DedicatedCores = types.BoolValue(false)
	if got := storageConfigErrors(s3); len(got) != 0 {
		t.Errorf("s3 shared: %v", got)
	}
	pg := baseStorage("postgres")
	pg.DedicatedCores = types.BoolValue(true)
	if got := storageConfigErrors(pg); len(got) != 0 {
		t.Errorf("postgres with dedicated cores: %v", got)
	}
}

// running_cpu_model on every kind with cores: computed only, and read from
// the status row of the running resource (the server's CPU model, shared or
// dedicated); null when the platform doesn't say.
func TestRunningCPUModel(t *testing.T) {
	ctx := context.Background()
	for name, r := range map[string]func() resource.Resource{
		"vm":            NewVMResource,
		"container_app": NewContainerAppResource,
		"desktop_app":   NewDesktopAppResource,
		"storage":       NewStorageResource,
		"browser":       NewBrowserResource,
		"browser_api":   NewBrowserAPIResource,
	} {
		var resp resource.SchemaResponse
		r().Schema(ctx, resource.SchemaRequest{}, &resp)
		v, ok := resp.Schema.Attributes["running_cpu_model"].(schema.StringAttribute)
		if !ok || !v.Computed || v.Optional || v.Required || v.Description != runningCPUModelDescription {
			t.Errorf("%s.running_cpu_model: %+v", name, resp.Schema.Attributes["running_cpu_model"])
		}
		if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Errorf("%s: schema: %v", name, d)
		}
	}
	for name, d := range map[string]func() datasource.DataSource{"vm": NewVMDataSource, "vms": NewVMsDataSource} {
		var resp datasource.SchemaResponse
		d().Schema(ctx, datasource.SchemaRequest{}, &resp)
		a := resp.Schema.Attributes["running_cpu_model"]
		if l, ok := resp.Schema.Attributes["vms"].(dsschema.ListNestedAttribute); ok {
			a = l.NestedObject.Attributes["running_cpu_model"]
		}
		if a == nil || !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("data %s.running_cpu_model: %+v", name, a)
		}
	}

	const i9 = "Intel Core i9-14900K"
	c := coresPlatform(t, client.Workload{ID: "web", Type: "pod"}, strings.Join([]string{
		`{"id":"web","type":"pod","phase":"Running","ready":true,"cores":{"model":"` + i9 + `"}}`,
		`{"id":"desk","type":"desktop","phase":"Running","ready":true,"cores":{"speedMHz":5700,"model":"` + i9 + `"}}`,
		`{"id":"db","type":"storage","phase":"Running","ready":true,"cores":{"model":"` + i9 + `"}}`,
		`{"id":"off","type":"pod","phase":"Stopped","ready":false}`,
	}, ","))
	var diags diag.Diagnostics
	var app containerAppModel
	refreshAppStatus(ctx, c, "web", &app, &diags)
	if !app.RunningCPUModel.Equal(types.StringValue(i9)) {
		t.Errorf("app: %v", app.RunningCPUModel)
	}
	var off containerAppModel
	refreshAppStatus(ctx, c, "off", &off, &diags)
	if !off.RunningCPUModel.IsNull() {
		t.Errorf("stopped app: %v", off.RunningCPUModel)
	}
	var gone containerAppModel
	refreshAppStatus(ctx, c, "gone", &gone, &diags)
	if !gone.RunningCPUModel.IsNull() {
		t.Errorf("an app the status doesn't list: %v", gone.RunningCPUModel)
	}
	desk := desktopAppModel{Name: types.StringValue("desk")}
	refreshDesktopStatus(ctx, c, &desk)
	if !desk.RunningCPUModel.Equal(types.StringValue(i9)) {
		t.Errorf("desktop: %v", desk.RunningCPUModel)
	}
	db := storageModel{Name: types.StringValue("db")}
	refreshStorageStatus(ctx, c, &db, &diags)
	if !db.RunningCPUModel.Equal(types.StringValue(i9)) {
		t.Errorf("database: %v", db.RunningCPUModel)
	}
	if diags.HasError() {
		t.Errorf("diags: %v", diags)
	}
}

// A browser's and a Browser API's refresh read the model of the server they
// run on.
func TestBrowserReadsCPUModel(t *testing.T) {
	ctx := context.Background()
	const i9 = "Intel Core i9-14900K"
	for _, c := range []struct {
		name string
		r    func() resource.Resource
		w    client.Workload
	}{
		{"browser", NewBrowserResource, client.Workload{ID: "b1", Type: "browser", Browser: map[string]any{"cpu": "1", "memory": "1Gi"}}},
		{"browser_api", NewBrowserAPIResource, client.Workload{ID: "b1", Type: "controller", Controller: map[string]any{"cpu": "250m", "memory": "256Mi"}}},
	} {
		r := c.r()
		r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
			ProviderData: &providerData{Client: coresPlatform(t, c.w,
				`{"id":"b1","type":"`+c.w.Type+`","phase":"Running","ready":true,"cores":{"model":"`+i9+`"}}`)},
		}, &resource.ConfigureResponse{})
		var sr resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sr)
		st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		st.SetAttribute(ctx, path.Root("name"), "b1")
		resp := resource.ReadResponse{State: st}
		r.Read(ctx, resource.ReadRequest{State: st}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: read: %v", c.name, resp.Diagnostics)
			continue
		}
		var got types.String
		resp.State.GetAttribute(ctx, path.Root("running_cpu_model"), &got)
		if !got.Equal(types.StringValue(i9)) {
			t.Errorf("%s: running_cpu_model %v", c.name, got)
		}
	}
}
