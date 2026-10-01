package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

var (
	sNull = types.StringNull()
	str   = types.StringValue
)

// Automatic (left out, or "auto", even with a host or region next to it)
// sends nothing; host and region send the strategy and what was given.
func TestPlacementSpec(t *testing.T) {
	cases := []struct {
		name                   string
		strategy, host, region types.String
		want                   map[string]any
	}{
		{"left out", sNull, sNull, sNull, nil},
		{"left out with a host", sNull, str("h1"), sNull, nil},
		{"auto", str("auto"), sNull, sNull, nil},
		{"auto with a host", str("auto"), str("h1"), sNull, nil},
		{"empty", str(""), sNull, sNull, nil},
		{"host", str("host"), str("h1"), sNull, map[string]any{"strategy": "host", "host": "h1"}},
		{"region", str("region"), sNull, str("ru-mow"), map[string]any{"strategy": "region", "region": "ru-mow"}},
	}
	for _, c := range cases {
		if got := placementSpec(c.strategy, c.host, c.region); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestReadPlacement(t *testing.T) {
	s, h, r := readPlacement(map[string]any{"placement": map[string]any{"strategy": "region", "region": "ru-mow"}})
	if s.ValueString() != "region" || !h.IsNull() || r.ValueString() != "ru-mow" {
		t.Errorf("region: %v %v %v", s, h, r)
	}
	s, h, r = readPlacement(map[string]any{"cpu": "1"})
	if !s.IsNull() || !h.IsNull() || !r.IsNull() {
		t.Errorf("automatic: %v %v %v", s, h, r)
	}
	s, _, _ = readPlacement(nil)
	if !s.IsNull() {
		t.Errorf("nil block: %v", s)
	}
}

// Read-back: the platform's placement wins, except that an automatic one
// leaves a configuration written as automatic as it was (no planned change).
func TestRefreshPlacement(t *testing.T) {
	placed := map[string]any{"placement": map[string]any{"strategy": "host", "host": "h1"}}
	cases := []struct {
		name                   string
		sp                     map[string]any
		strategy, host, region types.String
		want                   [3]types.String
	}{
		{"import, placed", placed, sNull, sNull, sNull, [3]types.String{str("host"), str("h1"), sNull}},
		{"import, automatic", map[string]any{}, sNull, sNull, sNull, [3]types.String{sNull, sNull, sNull}},
		{"written auto stays", map[string]any{}, str("auto"), str("h1"), sNull, [3]types.String{str("auto"), str("h1"), sNull}},
		{"moved to automatic in the console", map[string]any{}, str("region"), sNull, str("ru-mow"), [3]types.String{sNull, sNull, sNull}},
		{"moved to a host in the console", placed, str("region"), sNull, str("ru-mow"), [3]types.String{str("host"), str("h1"), sNull}},
	}
	for _, c := range cases {
		s, h, r := c.strategy, c.host, c.region
		refreshPlacement(c.sp, &s, &h, &r)
		if got := [3]types.String{s, h, r}; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

var regionSpec = map[string]any{"strategy": "region", "region": "ru-mow"}

// Every resource sends placement inside its own block, and nothing when it
// runs automatically.
func TestPlacementInEveryBlock(t *testing.T) {
	ctx := context.Background()
	type built struct {
		name   string
		placed map[string]any
		auto   map[string]any
	}
	app := func(s, r types.String) containerAppModel {
		return containerAppModel{Image: str("nginx"), PlacementStrategy: s, PlacementRegion: r}
	}
	db := func(s, r types.String) storageModel {
		return storageModel{Engine: str("postgres"), PlacementStrategy: s, PlacementRegion: r}
	}
	br := func(s, r types.String) browserModel { return browserModel{PlacementStrategy: s, PlacementRegion: r} }
	api := func(s, r types.String) browserAPIModel {
		m := baseBrowserAPIState()
		m.AllBrowsers = types.BoolValue(true)
		m.PlacementStrategy, m.PlacementRegion = s, r
		return m
	}
	desk := func(s, r types.String) desktopAppModel {
		return desktopAppModel{PlacementStrategy: s, PlacementRegion: r}
	}
	vm := func(s, r types.String) vmResourceModel {
		return vmResourceModel{PlacementStrategy: s, PlacementRegion: r}
	}
	region, auto := str("region"), str("auto")
	cases := []built{
		{"pod", containerAppSpec(ctx, app(region, str("ru-mow")), false), containerAppSpec(ctx, app(auto, str("ru-mow")), false)},
		{"storage", storageSpec(ctx, db(region, str("ru-mow")), "", true), storageSpec(ctx, db(sNull, sNull), "", true)},
		{"browser", browserSpec(ctx, br(region, str("ru-mow"))), browserSpec(ctx, br(sNull, sNull))},
		{"controller", browserAPISpec(ctx, api(region, str("ru-mow")), nil), browserAPISpec(ctx, api(auto, sNull), nil)},
		{"desktop", desktopSpec(desk(region, str("ru-mow"))), desktopSpec(desk(sNull, str("ru-mow")))},
		{"vm", vmSpec(ctx, vm(region, str("ru-mow")), vmWrite{}), vmSpec(ctx, vm(auto, sNull), vmWrite{})},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.placed["placement"], regionSpec) {
			t.Errorf("%s: placement %v, want %v", c.name, c.placed["placement"], regionSpec)
		}
		if _, sent := c.auto["placement"]; sent {
			t.Errorf("%s: automatic sent %v", c.name, c.auto["placement"])
		}
	}
	// The update a PUT sends carries it in the block too.
	if w := updateWorkloadBody(ctx, app(region, str("ru-mow")), false); !reflect.DeepEqual(w.Pod["placement"], regionSpec) {
		t.Errorf("app update: %v", w.Pod)
	}
}

// fakePlatform answers the workspace and status reads with one workload.
func fakePlatform(t *testing.T, w client.Workload) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspace":
			_ = json.NewEncoder(rw).Encode(map[string]any{"spec": map[string]any{"workloads": []client.Workload{w}}})
		case "/v1/status":
			_, _ = rw.Write([]byte(`{"workloads":[{"id":"` + w.ID + `","type":"` + w.Type + `","phase":"Running","ready":true}]}`))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	return client.New(srv.URL, "llc_test")
}

// An import reads the placement back into state, for every resource that has
// one, through the resource's own Read and schema.
func TestImportReadsPlacement(t *testing.T) {
	ctx := context.Background()
	pl := map[string]any{"placement": map[string]any{"strategy": "host", "host": "selangor"}}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{"placement": pl["placement"]}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		r resource.Resource
		w client.Workload
	}{
		{NewContainerAppResource(), client.Workload{ID: "web", Type: "pod", Pod: with(map[string]any{"image": "nginx"})}},
		{NewStorageResource(), client.Workload{ID: "db", Type: "storage", Storage: with(map[string]any{"engine": "postgres"})}},
		{NewBrowserResource(), client.Workload{ID: "br", Type: "browser", Browser: with(nil)}},
		{NewBrowserAPIResource(), client.Workload{ID: "api", Type: "controller", Controller: with(map[string]any{"autodiscover": true})}},
		{NewDesktopAppResource(), client.Workload{ID: "desk", Type: "desktop", Desktop: with(nil)}},
		{NewVMResource(), client.Workload{ID: "box", Type: "vm-ubuntu", VM: with(map[string]any{"cpus": float64(2)})}},
	}
	for _, c := range cases {
		c.r.(resource.ResourceWithConfigure).Configure(ctx, resource.ConfigureRequest{
			ProviderData: &providerData{Client: fakePlatform(t, c.w)},
		}, &resource.ConfigureResponse{})
		var sr resource.SchemaResponse
		c.r.Schema(ctx, resource.SchemaRequest{}, &sr)
		st := tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}
		if d := st.SetAttribute(ctx, path.Root("name"), c.w.ID); d.HasError() {
			t.Fatalf("%s: %v", c.w.Type, d)
		}
		resp := resource.ReadResponse{State: st}
		c.r.Read(ctx, resource.ReadRequest{State: st}, &resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("%s: read: %v", c.w.Type, resp.Diagnostics)
			continue
		}
		var strategy, host, region types.String
		resp.State.GetAttribute(ctx, path.Root("placement_strategy"), &strategy)
		resp.State.GetAttribute(ctx, path.Root("placement_host"), &host)
		resp.State.GetAttribute(ctx, path.Root("placement_region"), &region)
		if strategy.ValueString() != "host" || host.ValueString() != "selangor" || !region.IsNull() {
			t.Errorf("%s: read %v %v %v", c.w.Type, strategy, host, region)
		}
	}
}

// data.livellm_hosts decodes the platform's host list.
func TestHostsDataSource(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fleet/hosts" || r.Header.Get("x-api-key") != "llc_test" {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write([]byte(`{"hosts":[
			{"id":"selangor","region":"ru-mow","zone":"a","nodeGroup":"","cpuTotal":16,"cpuFree":4.5,"memTotalGi":64,"memFreeGi":31,"gpuTotal":0,"gpuFree":0,"utilization":0.2,"ready":true},
			{"id":"spare","region":"eu-west","cpuTotal":8,"cpuFree":8,"memTotalGi":32,"memFreeGi":32,"gpuTotal":0,"gpuFree":0,"utilization":0,"ready":false}]}`))
	}))
	defer srv.Close()
	d := NewHostsDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(ctx, datasource.ConfigureRequest{
		ProviderData: &providerData{Client: client.New(srv.URL, "llc_test")},
	}, &datasource.ConfigureResponse{})
	var sr datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &sr)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sr.Schema, Raw: tftypes.NewValue(sr.Schema.Type().TerraformType(ctx), nil)}}
	d.Read(ctx, datasource.ReadRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("read: %v", resp.Diagnostics)
	}
	var m hostsModel
	resp.State.Get(ctx, &m)
	var hosts []hostModel
	m.Hosts.ElementsAs(ctx, &hosts, false)
	want := []hostModel{
		{ID: str("selangor"), Region: str("ru-mow"), Zone: str("a"), Ready: types.BoolValue(true)},
		{ID: str("spare"), Region: str("eu-west"), Zone: str(""), Ready: types.BoolValue(false)},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Errorf("hosts %v, want %v", hosts, want)
	}
}
