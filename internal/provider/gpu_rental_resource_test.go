package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// fakeGPUs is a platform's GPU routes: its rentals by id, every request it
// got (method, path, body), and what it is told to answer instead.
type fakeGPUs struct {
	mu      sync.Mutex
	rentals map[string]*client.GPURental
	order   []string
	calls   []gpuCall
	// answer, when set for "METHOD path", is the status and body sent
	// instead (once per entry in the slice).
	answer map[string][]fakeAnswer
	// confirmNeeded: a DELETE (rental or card) answers 428 until the body
	// carries the right confirm.
	confirmNeeded bool
	// switchPolls: after a change of use, how many list reads still show it
	// switching.
	switchPolls int
	next        int
	// keepRemoved: a GPU taken off a cluster stays listed, ended (as the
	// platform lists one still coming off its machine or apps).
	keepRemoved bool
}

type gpuCall struct {
	Method, Path string
	Body         map[string]any
}

type fakeAnswer struct {
	status int
	body   string
}

func newFakeGPUs() *fakeGPUs {
	return &fakeGPUs{rentals: map[string]*client.GPURental{}, answer: map[string][]fakeAnswer{}}
}

func (f *fakeGPUs) id() string {
	f.next++
	return "g-0000000" + string(rune('0'+f.next))
}

func (f *fakeGPUs) add(r client.GPURental) {
	f.rentals[r.ID] = &r
	f.order = append(f.order, r.ID)
}

func ptr[T any](v T) *T { return &v }

func (f *fakeGPUs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.calls = append(f.calls, gpuCall{r.Method, r.URL.Path, body})
	key := r.Method + " " + r.URL.Path
	if as := f.answer[key]; len(as) > 0 {
		f.answer[key] = as[1:]
		w.WriteHeader(as[0].status)
		_, _ = w.Write([]byte(as[0].body))
		return
	}
	reply := func(status int, v any) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/gpus"), "/")
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/gpus":
		out := []client.GPURental{}
		for _, id := range f.order {
			rt := f.rentals[id]
			if f.switchPolls > 0 {
				f.switchPolls--
			} else if strings.HasPrefix(rt.Phase, "switching-") {
				rt.Phase = "ready"
			}
			out = append(out, *rt)
		}
		reply(200, map[string]any{"rentals": out})
	case r.Method == "GET" && r.URL.Path == "/v1/gpus/models":
		reply(200, map[string]any{"models": []client.GPUModel{{Model: "rtx5090", Name: "RTX 5090", CentsHour: ptr(60), Total: 2,
			FreeNow: map[string]int{"machine": 1, "apps": 2}, FreeUntil: map[string]*string{"machine": nil, "apps": ptr("2026-11-20T10:00:00Z")},
			Slots: []int{1, 2}}}})
	case r.Method == "POST" && r.URL.Path == "/v1/gpus":
		var req client.GPURentRequest
		_ = json.Unmarshal(raw, &req)
		state, booked := "active", false
		if req.StartsAt != "" {
			state, booked = "booked", true
		}
		var ends *string
		if req.EndsAt != "" {
			ends = ptr(req.EndsAt)
		}
		mk := func(use string, cards map[string]int) client.GPURental {
			id := f.id()
			rt := client.GPURental{ID: id, Use: use, State: state, Booked: booked, StartsAt: req.StartsAt, EndsAt: ends,
				Phase: "ready", Host: ptr("selangor"), Region: ptr("ru-mow")}
			n := 0
			for m, c := range cards {
				rt.Model = m
				for i := 0; i < c; i++ {
					cid := id
					if n > 0 {
						cid = f.id()
					}
					rt.Cards = append(rt.Cards, client.GPUCard{ID: cid, Model: m, Name: "RTX 5090", State: state, Answering: true})
					n++
				}
			}
			rt.Name = "RTX 5090"
			f.add(rt)
			return rt
		}
		if len(req.Clusters) > 0 {
			var out []client.GPURental
			for _, c := range req.Clusters {
				out = append(out, mk(c.Use, c.Cards))
			}
			reply(201, map[string]any{"clusters": out})
			return
		}
		reply(201, map[string]any{"rental": mk(req.Use, map[string]int{req.Model: 1})})
	case len(parts) >= 2 && f.rentals[parts[1]] == nil:
		reply(422, map[string]string{"error": "There is no such GPU rental.", "code": "gpu_unknown"})
	case r.Method == "PATCH" && len(parts) == 2:
		rt := f.rentals[parts[1]]
		rt.EndsAt = ptr(body["endsAt"].(string))
		reply(200, map[string]any{"rental": rt})
	case r.Method == "POST" && len(parts) == 3 && parts[2] == "use":
		rt := f.rentals[parts[1]]
		rt.Use = body["use"].(string)
		rt.Phase = "switching-to-" + rt.Use
		reply(202, map[string]any{"rental": rt})
	case r.Method == "DELETE" && len(parts) == 4 && parts[2] == "cards":
		rt := f.rentals[parts[1]]
		if f.confirmNeeded && body["confirm"] != parts[3] {
			reply(428, map[string]string{"error": "This booking starts within a day, so it is charged in full. Send its id as confirm to cancel it.", "code": "confirm_required"})
			return
		}
		var left []client.GPUCard
		for _, c := range rt.Cards {
			if c.ID != parts[3] {
				left = append(left, c)
			} else if f.keepRemoved {
				c.State = "ended"
				left = append(left, c)
			}
		}
		rt.Cards = left
		reply(200, map[string]any{"rental": rt})
	case r.Method == "DELETE" && len(parts) == 2:
		rt := f.rentals[parts[1]]
		if rt.Over() {
			reply(409, map[string]string{"error": "This rental has ended.", "code": "gpu_ended"})
			return
		}
		if f.confirmNeeded && body["confirm"] != parts[1] {
			reply(428, map[string]string{"error": "This booking starts within a day, so it is charged in full. Send its id as confirm to cancel it.", "code": "confirm_required"})
			return
		}
		rt.State, rt.EndedReason = "ended", ptr("released")
		reply(200, map[string]any{"rental": rt, "chargedCents": 1234})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGPUs) callsTo(method, prefix string) []gpuCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gpuCall
	for _, c := range f.calls {
		if c.Method == method && strings.HasPrefix(c.Path, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func gpuSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	var sr resource.SchemaResponse
	NewGPURentalResource().Schema(context.Background(), resource.SchemaRequest{}, &sr)
	if sr.Diagnostics.HasError() {
		t.Fatal(sr.Diagnostics)
	}
	return sr
}

// gpuModel is a configuration: every computed field unknown (a plan), or
// null for what isn't set.
func gpuModel() gpuRentalModel {
	return gpuRentalModel{
		ID: types.StringUnknown(), Model: types.StringNull(), Use: types.StringNull(), StartsAt: types.StringNull(),
		EndsAt: types.StringUnknown(), AllowChargedCancel: types.BoolNull(),
		Name: types.StringUnknown(), Status: types.StringUnknown(), Phase: types.StringUnknown(), Booked: types.BoolUnknown(),
		Host: types.StringUnknown(), Region: types.StringUnknown(), StartedAt: types.StringUnknown(), EndedAt: types.StringUnknown(),
		EndedReason: types.StringUnknown(), UsedBy: types.ListUnknown(types.StringType),
		Cards:          types.ListUnknown(types.ObjectType{AttrTypes: gpuCardAttrTypes}),
		HoursThisMonth: types.Float64Unknown(), CentsThisMonth: types.Int64Unknown(),
		Timeouts: timeouts.Value{Object: types.ObjectNull(map[string]attr.Type{"update": types.StringType})},
	}
}

func gpuPlanOf(t *testing.T, m gpuRentalModel) tfsdk.Plan {
	t.Helper()
	p := tfsdk.Plan{Schema: gpuSchema(t).Schema}
	if d := p.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return p
}

func gpuStateOf(t *testing.T, m gpuRentalModel) tfsdk.State {
	t.Helper()
	s := tfsdk.State{Schema: gpuSchema(t).Schema}
	if d := s.Set(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return s
}

func gpuGet(t *testing.T, st tfsdk.State) gpuRentalModel {
	t.Helper()
	var m gpuRentalModel
	if d := st.Get(context.Background(), &m); d.HasError() {
		t.Fatal(d)
	}
	return m
}

func emptyState(t *testing.T) tfsdk.State {
	sch := gpuSchema(t).Schema
	return tfsdk.State{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}
}

func gpuResourceFor(srv *httptest.Server) *gpuRentalResource {
	return &gpuRentalResource{data: &providerData{Client: client.New(srv.URL, "llc_test")}}
}

func rentWith(t *testing.T, r *gpuRentalResource, cfg gpuRentalModel) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	resp := resource.CreateResponse{State: emptyState(t)}
	plan := gpuPlanOf(t, cfg)
	r.Create(context.Background(), resource.CreateRequest{Plan: plan, Config: tfsdk.Config{Schema: plan.Schema, Raw: plan.Raw}}, &resp)
	return resp.State, resp.Diagnostics
}

func updateWith(t *testing.T, r *gpuRentalResource, state tfsdk.State, plan gpuRentalModel) (tfsdk.State, diag.Diagnostics) {
	t.Helper()
	p := gpuPlanOf(t, plan)
	resp := resource.UpdateResponse{State: tfsdk.State{Schema: p.Schema, Raw: p.Raw}}
	r.Update(context.Background(), resource.UpdateRequest{Plan: p, State: state, Config: tfsdk.Config{Schema: p.Schema, Raw: p.Raw}}, &resp)
	return resp.State, resp.Diagnostics
}

// planFrom is the next plan for a configuration over state: what is written
// from cfg, the rest from state (no change to the computed fields).
func planFrom(st gpuRentalModel, edit func(*gpuRentalModel)) gpuRentalModel {
	p := st
	if st.Cluster != nil {
		c := *st.Cluster
		p.Cluster = &c
	}
	edit(&p)
	return p
}

func TestGPURentNow(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use = types.StringValue("rtx5090"), types.StringValue("machine")
	st, d := rentWith(t, r, cfg)
	if d.HasError() {
		t.Fatal(d)
	}
	posts := f.callsTo("POST", "/v1/gpus")
	if len(posts) != 1 || posts[0].Body["model"] != "rtx5090" || posts[0].Body["use"] != "machine" ||
		posts[0].Body["startsAt"] != nil || posts[0].Body["clusters"] != nil {
		t.Fatalf("rent sent %+v", posts)
	}
	m := gpuGet(t, st)
	if m.ID.ValueString() != "g-00000001" || m.Status.ValueString() != "active" || !m.EndsAt.IsNull() ||
		m.Host.ValueString() != "selangor" || len(m.Cards.Elements()) != 1 {
		t.Fatalf("state %+v", m)
	}
	// A refresh with nothing changed keeps the state as it was.
	rresp := resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, &rresp)
	if rresp.Diagnostics.HasError() || !rresp.State.Raw.Equal(st.Raw) {
		t.Fatalf("read changed state: %v", rresp.Diagnostics)
	}
}

func TestGPUBooking(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use = types.StringValue("rtx5090"), types.StringValue("apps")
	cfg.StartsAt, cfg.EndsAt = types.StringValue("2026-11-20T10:00:00Z"), types.StringValue("2026-11-20T14:00:00Z")
	cfg.Target = &gpuTargetModel{Machine: types.StringNull(), Apps: stringList([]string{"web"})}
	st, d := rentWith(t, r, cfg)
	if d.HasError() {
		t.Fatal(d)
	}
	b := f.callsTo("POST", "/v1/gpus")[0].Body
	tg, _ := b["target"].(map[string]any)
	if b["startsAt"] != "2026-11-20T10:00:00Z" || b["endsAt"] != "2026-11-20T14:00:00Z" || tg == nil || tg["apps"] == nil {
		t.Fatalf("booking sent %+v", b)
	}
	if m := gpuGet(t, st); m.Status.ValueString() != "booked" || !m.Booked.ValueBool() || m.EndsAt.ValueString() != "2026-11-20T14:00:00Z" {
		t.Fatalf("state %+v", m)
	}
}

func clusterCfg(cards map[string]int) gpuRentalModel {
	cfg := gpuModel()
	cfg.Cluster = &gpuClusterModel{Use: types.StringValue("machine"), Cards: countsMap(cards),
		Location: &gpuLocationModel{Mode: types.StringValue("region"), Region: types.StringValue("ru-mow"), Host: types.StringNull()}}
	return cfg
}

func TestGPUClusterRentAndShrink(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	st, d := rentWith(t, r, clusterCfg(map[string]int{"rtx5090": 3}))
	if d.HasError() {
		t.Fatal(d)
	}
	b := f.callsTo("POST", "/v1/gpus")[0].Body
	cl, _ := b["clusters"].([]any)
	if len(cl) != 1 || b["model"] != nil {
		t.Fatalf("clusters sent %+v", b)
	}
	c0 := cl[0].(map[string]any)
	if c0["use"] != "machine" || c0["cards"].(map[string]any)["rtx5090"] != float64(3) ||
		c0["location"].(map[string]any)["region"] != "ru-mow" {
		t.Fatalf("cluster sent %+v", c0)
	}
	m := gpuGet(t, st)
	if len(m.Cards.Elements()) != 3 {
		t.Fatalf("cards %v", m.Cards)
	}
	// The middle GPU stopped working: shrinking takes it off first.
	rt := f.rentals[m.ID.ValueString()]
	broken := rt.Cards[1].ID
	rt.Cards[1].Answering = false
	st2, d := updateWith(t, r, st, planFrom(m, func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 2}) }))
	if d.HasError() {
		t.Fatal(d)
	}
	dels := f.callsTo("DELETE", "/v1/gpus/"+m.ID.ValueString()+"/cards/")
	if len(dels) != 1 || !strings.HasSuffix(dels[0].Path, "/"+broken) || dels[0].Body != nil {
		t.Fatalf("card removals %+v, want %s without a confirm", dels, broken)
	}
	// All working: the last one goes.
	m2 := gpuGet(t, st2)
	last := rt.Cards[len(rt.Cards)-1].ID
	if _, d = updateWith(t, r, st2, planFrom(m2, func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 1}) })); d.HasError() {
		t.Fatal(d)
	}
	dels = f.callsTo("DELETE", "/v1/gpus/"+m.ID.ValueString()+"/cards/")
	if len(dels) != 2 || !strings.HasSuffix(dels[1].Path, "/"+last) {
		t.Fatalf("second removal %+v, want %s", dels, last)
	}
}

func TestGPUCardRemovalCharged(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	st, d := rentWith(t, r, clusterCfg(map[string]int{"rtx5090": 2}))
	if d.HasError() {
		t.Fatal(d)
	}
	f.confirmNeeded = true
	m := gpuGet(t, st)
	_, d = updateWith(t, r, st, planFrom(m, func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 1}) }))
	if !d.HasError() || !strings.Contains(d[0].Detail(), "allow_charged_cancel") {
		t.Fatalf("want a charged-in-full error, got %v", d)
	}
	for _, c := range f.callsTo("DELETE", "/v1/gpus/") {
		if c.Body != nil {
			t.Fatalf("a confirm was sent without allow_charged_cancel: %+v", c)
		}
	}
	_, d = updateWith(t, r, st, planFrom(m, func(p *gpuRentalModel) {
		p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 1})
		p.AllowChargedCancel = types.BoolValue(true)
	}))
	if d.HasError() {
		t.Fatal(d)
	}
	dels := f.callsTo("DELETE", "/v1/gpus/")
	lastCall := dels[len(dels)-1]
	if card := lastCall.Path[strings.LastIndex(lastCall.Path, "/")+1:]; lastCall.Body["confirm"] != card {
		t.Fatalf("confirm %+v, want %s", lastCall.Body, card)
	}
}

func TestGPUExtendAndChangeUse(t *testing.T) {
	gpuPollEvery = time.Millisecond
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use, cfg.EndsAt = types.StringValue("rtx5090"), types.StringValue("machine"), types.StringValue("2026-11-13T18:00:00Z")
	st, d := rentWith(t, r, cfg)
	if d.HasError() {
		t.Fatal(d)
	}
	m := gpuGet(t, st)
	f.switchPolls = 2
	st2, d := updateWith(t, r, st, planFrom(m, func(p *gpuRentalModel) {
		p.EndsAt = types.StringValue("2026-11-13T22:00:00Z")
		p.Use = types.StringValue("apps")
	}))
	if d.HasError() {
		t.Fatal(d)
	}
	ext := f.callsTo("PATCH", "/v1/gpus/")
	if len(ext) != 1 || ext[0].Body["endsAt"] != "2026-11-13T22:00:00Z" {
		t.Fatalf("extend %+v", ext)
	}
	use := f.callsTo("POST", "/v1/gpus/"+m.ID.ValueString()+"/use")
	if len(use) != 1 || use[0].Body["use"] != "apps" {
		t.Fatalf("use %+v", use)
	}
	got := gpuGet(t, st2)
	if got.Use.ValueString() != "apps" || got.Phase.ValueString() != "ready" || got.EndsAt.ValueString() != "2026-11-13T22:00:00Z" {
		t.Fatalf("state %+v", got)
	}
	if n := len(f.callsTo("GET", "/v1/gpus")); n < 3 {
		t.Errorf("the change of use wasn't waited for: %d reads", n)
	}
}

// An ended rental stays in state, keeps what was written (no plan), and
// a destroy sends nothing; one no longer listed stays too, as ended.
func TestGPUEndedStaysInState(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use, cfg.EndsAt = types.StringValue("rtx5090"), types.StringValue("machine"), types.StringValue("2026-11-13T18:00:00Z")
	st, d := rentWith(t, r, cfg)
	if d.HasError() {
		t.Fatal(d)
	}
	before := gpuGet(t, st)
	rt := f.rentals[before.ID.ValueString()]
	rt.State, rt.EndedReason, rt.EndedAt = "ended", ptr("admin"), ptr("2026-11-13T15:00:00Z")
	rt.EndsAt, rt.Use = ptr("2026-11-13T15:00:00Z"), "apps"
	rresp := resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, &rresp)
	if rresp.Diagnostics.HasError() || rresp.State.Raw.IsNull() {
		t.Fatalf("read: %v, removed %v", rresp.Diagnostics, rresp.State.Raw.IsNull())
	}
	after := gpuGet(t, rresp.State)
	if after.Status.ValueString() != "ended" || after.EndedReason.ValueString() != "admin" ||
		!after.EndsAt.Equal(before.EndsAt) || !after.Use.Equal(before.Use) || !after.Model.Equal(before.Model) {
		t.Fatalf("ended state %+v", after)
	}
	// The same configuration plans nothing and raises nothing.
	mresp := resource.ModifyPlanResponse{Plan: gpuPlanOf(t, after)}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: rresp.State, Plan: gpuPlanOf(t, after)}, &mresp)
	if mresp.Diagnostics.HasError() {
		t.Fatal(mresp.Diagnostics)
	}
	// A changed configuration on an ended rental is refused, never re-rented.
	changed := planFrom(after, func(p *gpuRentalModel) { p.EndsAt = types.StringValue("2026-11-13T20:00:00Z") })
	mresp = resource.ModifyPlanResponse{Plan: gpuPlanOf(t, changed)}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: rresp.State, Plan: gpuPlanOf(t, changed)}, &mresp)
	if !mresp.Diagnostics.HasError() || mresp.Diagnostics[0].Summary() != "This GPU rental has ended" {
		t.Fatalf("want the ended error, got %v", mresp.Diagnostics)
	}
	// Dropped out of the list (ended over 30 days ago): kept, ended.
	delete(f.rentals, before.ID.ValueString())
	f.order = nil
	st2 := gpuStateOf(t, planFrom(before, func(p *gpuRentalModel) {}))
	rresp = resource.ReadResponse{State: st2}
	r.Read(context.Background(), resource.ReadRequest{State: st2}, &rresp)
	if rresp.State.Raw.IsNull() || gpuGet(t, rresp.State).Status.ValueString() != "ended" {
		t.Fatalf("an unlisted rental must stay as ended: %v", rresp.Diagnostics)
	}
	n := len(f.calls)
	dresp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: rresp.State}, &dresp)
	if dresp.Diagnostics.HasError() || len(f.calls) != n {
		t.Fatalf("destroy of an ended rental: %v, %d calls", dresp.Diagnostics, len(f.calls)-n)
	}
}

func TestGPUReleaseCharged(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use = types.StringValue("rtx5090"), types.StringValue("apps")
	cfg.StartsAt, cfg.EndsAt = types.StringValue("2026-11-14T10:00:00Z"), types.StringValue("2026-11-14T12:00:00Z")
	st, d := rentWith(t, r, cfg)
	if d.HasError() {
		t.Fatal(d)
	}
	f.confirmNeeded = true
	id := gpuGet(t, st).ID.ValueString()

	// Off: an error, nothing cancelled, no confirm sent.
	dresp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: st}, &dresp)
	if !dresp.Diagnostics.HasError() || dresp.Diagnostics[0].Summary() != "This would be charged in full" {
		t.Fatalf("want the charged error, got %v", dresp.Diagnostics)
	}
	for _, c := range f.callsTo("DELETE", "/v1/gpus/"+id) {
		if c.Body != nil {
			t.Fatalf("confirm sent without allow_charged_cancel: %+v", c)
		}
	}
	if f.rentals[id].Over() {
		t.Fatal("cancelled without allow_charged_cancel")
	}
	// The plan says so ahead of the destroy.
	gpuNow = func() time.Time { return time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC) }
	defer func() { gpuNow = time.Now }()
	mresp := resource.ModifyPlanResponse{}
	sch := gpuSchema(t).Schema
	nullPlan := tfsdk.Plan{Schema: sch, Raw: tftypes.NewValue(sch.Type().TerraformType(context.Background()), nil)}
	r.ModifyPlan(context.Background(), resource.ModifyPlanRequest{State: st, Plan: nullPlan}, &mresp)
	if mresp.Diagnostics.WarningsCount() != 1 {
		t.Errorf("want a plan warning, got %v", mresp.Diagnostics)
	}

	// On: the confirm is the rental's id.
	m := gpuGet(t, st)
	m.AllowChargedCancel = types.BoolValue(true)
	dresp = resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: gpuStateOf(t, m)}, &dresp)
	if dresp.Diagnostics.HasError() {
		t.Fatal(dresp.Diagnostics)
	}
	dels := f.callsTo("DELETE", "/v1/gpus/"+id)
	if last := dels[len(dels)-1]; last.Body["confirm"] != id || !f.rentals[id].Over() {
		t.Fatalf("confirmed cancel %+v", last)
	}
	if !strings.Contains(dresp.Diagnostics[0].Detail(), "$12.34") {
		t.Errorf("the charge isn't said: %v", dresp.Diagnostics)
	}
}

func TestGPURentingOffAndRefusals(t *testing.T) {
	f := newFakeGPUs()
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	cfg := gpuModel()
	cfg.Model, cfg.Use = types.StringValue("rtx5090"), types.StringValue("machine")
	for _, c := range []struct {
		status        int
		body, summary string
	}{
		{404, `{"error":"GPUs aren't offered on this cloud."}`, "GPU renting isn't offered on this server"},
		{404, `404 page not found`, "GPU renting isn't offered on this server"},
		{403, `{"error":"Agents can't rent GPUs. Do it in the LiveLLM console."}`, "An agent sign-in can't rent GPUs"},
		{403, `{"error":"This API key can't rent GPUs. A person can give it the billing permission on the Keys page."}`, "Not allowed to rent GPUs"},
		{402, `{"error":"Pay as you go is off.","code":"payg_off","payUrl":"https://pay.example/secret"}`, "Pay as you go is off"},
		{409, `{"error":"No free RTX 5090 for those hours.","code":"gpu_none_free"}`, "No free GPU"},
		{422, `{"error":"Clusters aren't offered.","code":"gpu_clusters_off"}`, "Clusters aren't offered here"},
		{422, `{"error":"Choose whole hours.","code":"gpu_span"}`, "Cannot rent the GPU"},
	} {
		f.answer["POST /v1/gpus"] = []fakeAnswer{{c.status, c.body}}
		_, d := rentWith(t, r, cfg)
		if !d.HasError() || d[0].Summary() != c.summary {
			t.Errorf("%d %s: got %v, want %q", c.status, c.body, d, c.summary)
			continue
		}
		if strings.Contains(d[0].Detail(), "pay.example") {
			t.Errorf("a payment link was printed: %s", d[0].Detail())
		}
		if c.status == 422 && c.summary == "Cannot rent the GPU" && !strings.Contains(d[0].Detail(), "(gpu_span)") {
			t.Errorf("the code isn't named: %s", d[0].Detail())
		}
	}
	if n := len(f.rentals); n != 0 {
		t.Errorf("%d rentals made", n)
	}
}

func TestGPUPlanRefusals(t *testing.T) {
	state := gpuModel()
	state.ID, state.Status = types.StringValue("g-00000001"), types.StringValue("active")
	state.Cluster = &gpuClusterModel{Use: types.StringValue("machine"), Cards: countsMap(map[string]int{"rtx5090": 2})}
	state.EndsAt = types.StringValue("2026-11-13T18:00:00Z")
	cases := []struct {
		name string
		edit func(*gpuRentalModel)
		at   string
	}{
		{"earlier end", func(p *gpuRentalModel) { p.EndsAt = types.StringValue("2026-11-13T16:00:00Z") }, "ends_at"},
		{"more cards", func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 3}) }, "cluster"},
		{"another model", func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx4090": 1}) }, "cluster"},
		{"to one GPU", func(p *gpuRentalModel) {
			p.Cluster, p.Model, p.Use = nil, types.StringValue("rtx5090"), types.StringValue("machine")
		}, "cluster"},
		{"a start", func(p *gpuRentalModel) { p.StartsAt = types.StringValue("2026-11-14T10:00:00Z") }, "starts_at"},
	}
	for _, c := range cases {
		errs := gpuPlanErrors(planFrom(state, c.edit), state)
		if len(errs) == 0 || errs[0][0] != c.at {
			t.Errorf("%s: %v", c.name, errs)
		}
	}
	ok := []func(*gpuRentalModel){
		func(p *gpuRentalModel) { p.EndsAt = types.StringValue("2026-11-14T18:00:00Z") },
		func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 1}) },
		func(p *gpuRentalModel) { p.Cluster.Use = types.StringValue("apps") },
		func(p *gpuRentalModel) { p.AllowChargedCancel = types.BoolValue(true) },
	}
	for i, e := range ok {
		if errs := gpuPlanErrors(planFrom(state, e), state); len(errs) > 0 {
			t.Errorf("in-place change %d refused: %v", i, errs)
		}
	}
}

func TestGPUConfigErrors(t *testing.T) {
	m := gpuModel()
	if errs := gpuConfigErrors(m); len(errs) != 1 {
		t.Errorf("nothing to rent: %v", errs)
	}
	m.Model = types.StringValue("rtx5090")
	if errs := gpuConfigErrors(m); len(errs) != 1 || errs[0][0] != "use" {
		t.Errorf("no use: %v", errs)
	}
	m.Use = types.StringValue("machine")
	m.StartsAt, m.EndsAt = types.StringValue("2026-11-20T10:00:00Z"), types.StringNull()
	if errs := gpuConfigErrors(m); len(errs) != 1 || errs[0][0] != "ends_at" {
		t.Errorf("booking with no end: %v", errs)
	}
	m.StartsAt = types.StringNull()
	m.Target = &gpuTargetModel{Machine: types.StringValue("dev"), Apps: types.ListNull(types.StringType)}
	if errs := gpuConfigErrors(m); len(errs) != 1 || errs[0][0] != "target" {
		t.Errorf("target without a booking: %v", errs)
	}
	c := clusterCfg(map[string]int{"rtx5090": 9})
	if errs := gpuConfigErrors(c); len(errs) != 1 {
		t.Errorf("9 cards: %v", errs)
	}
	if !gpuHourRe.MatchString("2026-11-20T10:00:00Z") || gpuHourRe.MatchString("2026-11-20T10:30:00Z") || gpuHourRe.MatchString("2026-11-20T10:00:00+03:00") {
		t.Error("whole UTC hours")
	}
}

func TestGPUDataSources(t *testing.T) {
	f := newFakeGPUs()
	f.add(client.GPURental{ID: "g-0000000a", Model: "rtx5090", Name: "RTX 5090", Use: "apps", State: "active", Phase: "ready",
		StartsAt: "2026-11-13T10:00:00Z", Cards: []client.GPUCard{{ID: "g-0000000a", Model: "rtx5090", Answering: true}}})
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := client.New(srv.URL, "llc_test")
	ms, err := c.GPUModels(context.Background(), "machine", "2026-11-20T10:00:00Z", "2026-11-20T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if q := f.calls[len(f.calls)-1].Path; q != "/v1/gpus/models" {
		t.Fatalf("models path %s", q)
	}
	list := gpuModelList(ms)
	row := list.Elements()[0].(types.Object).Attributes()
	if row["cents_hour"].(types.Int64).ValueInt64() != 60 || !row["free_until_machine"].IsNull() ||
		row["free_until_apps"].(types.String).ValueString() != "2026-11-20T10:00:00Z" || len(row["slots"].(types.List).Elements()) != 2 {
		t.Fatalf("models %v", row)
	}
	rs, err := c.GPURentals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := gpuRentalList(rs).Elements()
	if len(rows) != 1 || rows[0].(types.Object).Attributes()["id"].(types.String).ValueString() != "g-0000000a" {
		t.Fatalf("rentals %v", rows)
	}
}

// A machine's, an app's or a Desktop App's gpu: sent only when the plan
// knows it ("" too, which takes it off); left out it keeps what the
// resource has; "" read back stays "" so it plans no change.
func TestGPURefKeptWhenUnset(t *testing.T) {
	spec := map[string]any{}
	gpuRefSpec(types.StringNull(), spec)
	gpuRefSpec(types.StringUnknown(), spec)
	if _, sent := spec["gpu"]; sent {
		t.Fatal("gpu sent while left out")
	}
	gpuRefSpec(types.StringValue(""), spec)
	if spec["gpu"] != "" {
		t.Fatal("\"\" not sent")
	}
	d := desktopSpec(desktopAppModel{GPU: types.StringValue("g-1a2b3c4d")})
	if d["gpu"] != "g-1a2b3c4d" {
		t.Fatalf("desktop spec %v", d)
	}
	if _, sent := desktopSpec(desktopAppModel{GPU: types.StringNull()})["gpu"]; sent {
		t.Fatal("desktop sent gpu while left out")
	}
	if g := readGPURef(types.StringValue(""), map[string]any{}); g.ValueString() != "" || g.IsNull() {
		t.Errorf("\"\" read back as %v", g)
	}
	if g := readGPURef(types.StringNull(), map[string]any{}); !g.IsNull() {
		t.Errorf("none read back as %v", g)
	}
	if g := readGPURef(types.StringNull(), map[string]any{"gpu": "g-1a2b3c4d"}); g.ValueString() != "g-1a2b3c4d" {
		t.Errorf("rental read back as %v", g)
	}
	// Left out of the configuration: the plan keeps what state holds.
	obj := tftypes.Object{AttributeTypes: map[string]tftypes.Type{"x": tftypes.String}}
	st := tfsdk.State{Raw: tftypes.NewValue(obj, map[string]tftypes.Value{"x": tftypes.NewValue(tftypes.String, "a")})}
	req := planmodifier.StringRequest{State: st, ConfigValue: types.StringNull(), StateValue: types.StringValue("g-1a2b3c4d"), PlanValue: types.StringUnknown()}
	resp := planmodifier.StringResponse{PlanValue: req.PlanValue}
	keepWhenLeftOut{}.PlanModifyString(context.Background(), req, &resp)
	if resp.PlanValue.ValueString() != "g-1a2b3c4d" {
		t.Errorf("left out planned %v", resp.PlanValue)
	}
	// ends_at left out: a rental with no end keeps null (no plan); a
	// new one is the platform's to say.
	req = planmodifier.StringRequest{State: st, ConfigValue: types.StringNull(), StateValue: types.StringNull(), PlanValue: types.StringUnknown()}
	resp = planmodifier.StringResponse{PlanValue: req.PlanValue}
	keepStateWhenLeftOut{}.PlanModifyString(context.Background(), req, &resp)
	if !resp.PlanValue.IsNull() {
		t.Errorf("ends_at left out planned %v", resp.PlanValue)
	}
	req.State = tfsdk.State{Raw: tftypes.NewValue(obj, nil)}
	resp = planmodifier.StringResponse{PlanValue: req.PlanValue}
	keepStateWhenLeftOut{}.PlanModifyString(context.Background(), req, &resp)
	if !resp.PlanValue.IsUnknown() {
		t.Errorf("ends_at on a create planned %v", resp.PlanValue)
	}
	for _, v := range []string{"", "g-1a2b3c4d"} {
		if !gpuRefRe.MatchString(v) {
			t.Errorf("%q refused", v)
		}
	}
	if gpuRefRe.MatchString("rtx5090") {
		t.Error("a model taken for a rental")
	}
}

// A GPU taken off stays listed for a while as ended: it isn't counted (no
// plan to take another off) and is never picked again.
func TestGPUShrinkWithEndedCardListed(t *testing.T) {
	f := newFakeGPUs()
	f.keepRemoved = true
	srv := httptest.NewServer(f)
	defer srv.Close()
	r := gpuResourceFor(srv)
	st, d := rentWith(t, r, clusterCfg(map[string]int{"rtx5090": 3}))
	if d.HasError() {
		t.Fatal(d)
	}
	m := gpuGet(t, st)
	id := m.ID.ValueString()
	st, d = updateWith(t, r, st, planFrom(m, func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 2}) }))
	if d.HasError() {
		t.Fatal(d)
	}
	rresp := resource.ReadResponse{State: st}
	r.Read(context.Background(), resource.ReadRequest{State: st}, &rresp)
	got := gpuGet(t, rresp.State)
	if n := cardCounts(got.Cluster.Cards)["rtx5090"]; n != 2 {
		t.Fatalf("read counts %d GPUs, want 2 (the one taken off is ended)", n)
	}
	if !rresp.State.Raw.Equal(st.Raw) {
		t.Errorf("a refresh after the shrink changed state")
	}
	ended := f.callsTo("DELETE", "/v1/gpus/"+id+"/cards/")[0].Path
	if _, d = updateWith(t, r, rresp.State, planFrom(got, func(p *gpuRentalModel) { p.Cluster.Cards = countsMap(map[string]int{"rtx5090": 1}) })); d.HasError() {
		t.Fatal(d)
	}
	dels := f.callsTo("DELETE", "/v1/gpus/"+id+"/cards/")
	if len(dels) != 2 || dels[1].Path == ended {
		t.Fatalf("second shrink %+v picked the ended GPU again", dels)
	}
}

// Times the platform answers in another zone are kept as UTC, so they
// compare with the configuration.
func TestGPUTimesInUTC(t *testing.T) {
	var m gpuRentalModel
	m.EndsAt = types.StringUnknown()
	readGPURental(&m, &client.GPURental{ID: "g-1", State: "active", EndsAt: ptr("2026-11-20T17:00:00+03:00"),
		StartedAt: ptr("2026-11-20T12:00:00.000000Z")}, true)
	if m.EndsAt.ValueString() != "2026-11-20T14:00:00Z" || m.StartedAt.ValueString() != "2026-11-20T12:00:00Z" {
		t.Fatalf("ends_at %s, started_at %s", m.EndsAt, m.StartedAt)
	}
}
