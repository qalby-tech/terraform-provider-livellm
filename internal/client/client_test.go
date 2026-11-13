package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// The status's cores decode: dedicated cores of one speed, or mixed with the
// platform's words; shared cores the server's CPU model alone; absent from a
// platform that doesn't say.
func TestStatusCores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workloads":[
			{"id":"fast","type":"vm-ubuntu","phase":"Running","ready":true,"cores":{"speedMHz":5700,"model":"Intel Core i9-14900K"}},
			{"id":"mixed","type":"vm-ubuntu","phase":"Running","ready":true,"cores":{"mixed":true,"message":"Mixed core speeds"}},
			{"id":"old","type":"vm-ubuntu","phase":"Running","ready":true},
			{"id":"web","type":"pod","phase":"Running","ready":true,"cores":{"model":"AMD EPYC 9654"}}]}`))
	}))
	defer srv.Close()
	st, err := New(srv.URL, "llc_test").Status(context.Background())
	if err != nil || len(st.Workloads) != 4 {
		t.Fatalf("status: %v %v", st, err)
	}
	if c := st.Workloads[0].Cores; c == nil || c.SpeedMHz != 5700 || c.Mixed || c.Model != "Intel Core i9-14900K" {
		t.Errorf("fast: %+v", c)
	}
	if c := st.Workloads[1].Cores; c == nil || !c.Mixed || c.SpeedMHz != 0 ||
		c.Message != "Mixed core speeds" || c.Model != "" {
		t.Errorf("mixed: %+v", c)
	}
	if c := st.Workloads[2].Cores; c != nil {
		t.Errorf("old: %+v", c)
	}
	if c := st.Workloads[3].Cores; c == nil || c.SpeedMHz != 0 || c.Mixed || c.Model != "AMD EPYC 9654" {
		t.Errorf("shared: %+v", c)
	}
}

// A 429 is sent again after Retry-After, body and all; past the tries, or a
// Retry-After past a minute, it is the error.
func TestRetryOn429(t *testing.T) {
	var calls int
	var bodies []string
	limit, after := 2, "0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if calls <= limit {
			w.Header().Set("Retry-After", after)
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":"Too many requests. Try again shortly.","code":"rate_limited"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"acme"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "llc_test")
	var out map[string]any
	if err := c.do(context.Background(), "POST", "/v1/workloads", map[string]string{"id": "web"}, &out); err != nil {
		t.Fatalf("after two 429s: %v", err)
	}
	if calls != 3 || out["name"] != "acme" {
		t.Fatalf("%d calls, %v", calls, out)
	}
	for _, b := range bodies {
		if b != `{"id":"web"}` {
			t.Fatalf("a retry sent %q", b)
		}
	}
	calls, limit = 0, 100
	err := c.do(context.Background(), "GET", "/v1/status", nil, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 429 || ae.Code() != "rate_limited" || calls != RetryTries+1 {
		t.Fatalf("past the tries: %v after %d calls", err, calls)
	}
	calls, after = 0, "900"
	if err := c.do(context.Background(), "GET", "/v1/status", nil, nil); !errors.As(err, &ae) || calls != 1 {
		t.Fatalf("Retry-After 900: %v after %d calls; want the error at once", err, calls)
	}
	// Another 429 (a counter's) is the error at once.
	calls, after = 0, "1"
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":"Too many tries.","code":"billing_rate"}`))
	}))
	defer other.Close()
	if err := New(other.URL, "k").do(context.Background(), "PUT", "/v1/subscription", nil, nil); !errors.As(err, &ae) || calls != 1 {
		t.Fatalf("billing_rate: %v after %d calls; want it at once", err, calls)
	}
	calls = 0
	// A cancelled apply stops waiting.
	calls, after = 0, "30"
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c.do(ctx, "GET", "/v1/status", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestRetryWait(t *testing.T) {
	if d, ok := RetryWait("3", 1); !ok || d < 3*time.Second || d > 3750*time.Millisecond {
		t.Fatalf("3: %v %v", d, ok)
	}
	if d, ok := RetryWait("", 3); !ok || d < 4*time.Second || d > 5*time.Second {
		t.Fatalf("none, try 3: %v %v", d, ok)
	}
	if _, ok := RetryWait("61", 1); ok {
		t.Fatal("61 s was waited")
	}
}

// A 404 from the router (plain text) is a platform without the route; the
// route's own 404 names what it didn't find in JSON.
func TestRouteMissing(t *testing.T) {
	for body, want := range map[string]bool{
		"404 page not found":                  true,
		`{"error":"workload not found: dev"}`: false,
	} {
		if got := (&APIError{Status: 404, Body: body}).RouteMissing(); got != want {
			t.Errorf("%q: %v, want %v", body, got, want)
		}
	}
	if (&APIError{Status: 403, Body: "forbidden"}).RouteMissing() {
		t.Error("a 403 is never a missing route")
	}
}
