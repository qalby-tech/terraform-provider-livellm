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
