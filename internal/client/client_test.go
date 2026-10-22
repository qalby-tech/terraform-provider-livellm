package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A delete whose answer never comes back is looked at again: a workload gone
// from the workspace was deleted; one still there keeps the error; a refusal
// is never looked past.
func TestDeleteWorkloadWhenTheAnswerIsLost(t *testing.T) {
	still := false
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && refuse:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"linked"}`))
		case r.Method == http.MethodDelete:
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		case still:
			_, _ = w.Write([]byte(`{"spec":{"workloads":[{"id":"web","type":"pod"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"spec":{"workloads":[{"id":"other","type":"pod"}]}}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "llc_test")
	ctx := context.Background()
	if err := c.DeleteWorkload(ctx, "web"); err != nil {
		t.Errorf("gone from the workspace after a lost answer: %v", err)
	}
	still = true
	if err := c.DeleteWorkload(ctx, "web"); err == nil {
		t.Error("still in the workspace after a lost answer should be an error")
	}
	refuse, still = true, false
	if err := c.DeleteWorkload(ctx, "web"); err == nil {
		t.Error("a refusal is an error, whatever the workspace shows")
	}
}

// A cores refusal's numbers read back from its answer; an answer without
// them, or not JSON, has none.
func TestAPIErrorCount(t *testing.T) {
	e := &APIError{Status: 409, Body: `{"error":"Only 6 cores at 5.7 GHz are available.","code":"core_speed_short","fit":6,"speedMHz":5700,"workload":"ws"}`}
	if n, ok := e.Count("fit"); !ok || n != 6 {
		t.Errorf("fit = %d, %v", n, ok)
	}
	if n, ok := e.Count("speedMHz"); !ok || n != 5700 {
		t.Errorf("speedMHz = %d, %v", n, ok)
	}
	for _, c := range []struct{ body, key string }{
		{`{"error":"x","code":"server_full","workload":"ws"}`, "fit"},
		{`{"workload":"ws"}`, "workload"},
		{`not json`, "fit"},
	} {
		if n, ok := (&APIError{Status: 409, Body: c.body}).Count(c.key); ok {
			t.Errorf("%s %s = %d, want none", c.body, c.key, n)
		}
	}
}

// The status's cores decode: one speed, or mixed with the platform's words;
// absent from a platform that doesn't say.
func TestStatusCores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workloads":[
			{"id":"fast","type":"vm-ubuntu","phase":"Running","ready":true,"cores":{"speedMHz":5700}},
			{"id":"mixed","type":"vm-ubuntu","phase":"Running","ready":true,"cores":{"mixed":true,"message":"Mixed core speeds: this machine's cores run at different clock speeds."}},
			{"id":"old","type":"vm-ubuntu","phase":"Running","ready":true}]}`))
	}))
	defer srv.Close()
	st, err := New(srv.URL, "llc_test").Status(context.Background())
	if err != nil || len(st.Workloads) != 3 {
		t.Fatalf("status: %v %v", st, err)
	}
	if c := st.Workloads[0].Cores; c == nil || c.SpeedMHz != 5700 || c.Mixed {
		t.Errorf("fast: %+v", c)
	}
	if c := st.Workloads[1].Cores; c == nil || !c.Mixed || c.SpeedMHz != 0 ||
		c.Message != "Mixed core speeds: this machine's cores run at different clock speeds." {
		t.Errorf("mixed: %+v", c)
	}
	if c := st.Workloads[2].Cores; c != nil {
		t.Errorf("old: %+v", c)
	}
}
