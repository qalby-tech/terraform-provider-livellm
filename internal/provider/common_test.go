package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qalby-tech/terraform-provider-livellm/internal/client"
)

// Right after an update the old desktop still reads ready: a status that says
// the change is still rolling out (updating) is waited through, and the wait
// ends only once the new version is ready. A platform that doesn't report
// updating is read as before.
func TestWaitReadyWaitsThroughAnUpdate(t *testing.T) {
	pollEvery = 10 * time.Millisecond
	defer func() { pollEvery = 5 * time.Second }()
	var looks atomic.Int32
	answers := []string{
		`{"id":"desk","type":"desktop","phase":"Updating","ready":false,"updating":true}`,
		`{"id":"desk","type":"desktop","phase":"Running","ready":true,"updating":true}`,
		`{"id":"desk","type":"desktop","phase":"Pending","ready":false,"updating":true}`,
		`{"id":"desk","type":"desktop","phase":"Running","ready":true}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			http.NotFound(w, r)
			return
		}
		n := int(looks.Add(1)) - 1
		_, _ = w.Write([]byte(`{"workloads":[` + answers[min(n, len(answers)-1)] + `]}`))
	}))
	defer srv.Close()
	c := client.New(srv.URL, "llc_test")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitReady(ctx, c, "desk", false); err != nil {
		t.Fatal(err)
	}
	if got := looks.Load(); got != int32(len(answers)) {
		t.Errorf("returned after %d looks, want %d (the first ready without updating)", got, len(answers))
	}

	// still updating when the time is up: the error says so
	answers = answers[:1]
	looks.Store(0)
	short, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := waitReady(short, c, "desk", false); err == nil || !strings.Contains(err.Error(), "Updating") {
		t.Errorf("timed out while updating: %v", err)
	}
}
