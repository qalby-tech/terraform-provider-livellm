package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"

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

// A platform that can't be read for the whole wait (an address that answers
// nothing) ends the wait with why, not "no status reported yet"; one whose
// status lacks the resource says that.
func TestWaitReadySaysWhyItSawNoStatus(t *testing.T) {
	pollEvery = 10 * time.Millisecond
	defer func() { pollEvery = 5 * time.Second }()
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.URL
	dead.Close() // nothing listens there any more
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := waitReady(short, client.New(addr, "llc_test"), "win", true)
	if err == nil || !strings.Contains(err.Error(), "status could not be read") || strings.Contains(err.Error(), "no status reported yet") {
		t.Errorf("unreachable: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"workloads":[{"id":"other","type":"desktop","phase":"Running","ready":true}]}`))
	}))
	defer srv.Close()
	short2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if err := waitReady(short2, client.New(srv.URL, "llc_test"), "win", false); err == nil || !strings.Contains(err.Error(), "not in the workspace's status") {
		t.Errorf("missing: %v", err)
	}
}

// A 402 on a workspace of the person's own says to raise the workspace's
// plan; one that carries organization_share says the workspace is using its
// share of an organization, which an owner raises under Organization →
// Billing. Both keep the platform's answer in the detail.
func TestPoolRefusalWording(t *testing.T) {
	own := `{"error":"subscription \"starter\" pool exceeded: CPU (3/2 cores) — upgrade or enable metered overage"}`
	org := `{"error":"This workspace is using its share of Acme. An owner can give it more under Organization → Billing.","code":"organization_share"}`
	for _, c := range []struct{ body, want string }{
		{own, "Cannot create VM: " + own + "\n\nThe workspace's plan does not have enough free CPU/RAM/disk for this. " +
			"Raise the plan (or enable metered billing) at https://cloud.live-llm.com/billing, or free resources first."},
		{org, "Cannot create VM: " + org + "\n\nThis workspace belongs to an organization and is using its share. " +
			"An owner of the organization can give it more under Organization → Billing " +
			"(https://cloud.live-llm.com/organization/billing), or free resources first."},
		{"pool exceeded", "Cannot create VM: pool exceeded\n\nThe workspace's plan does not have enough free CPU/RAM/disk for this. " +
			"Raise the plan (or enable metered billing) at https://cloud.live-llm.com/billing, or free resources first."},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", &client.APIError{Status: 402, Body: c.body})
		if len(diags) != 1 || diags[0].Summary() != "Workspace plan pool exceeded" || diags[0].Detail() != c.want {
			t.Errorf("402 %s:\n got %v\nwant %q", c.body, diags, c.want)
		}
	}
}
