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
			"Raise the plan (or turn on pay as you go) at https://cloud.live-llm.com/billing, or free resources first."},
		{org, "Cannot create VM: " + org + "\n\nThis workspace belongs to an organization and is using its share. " +
			"An owner of the organization can give it more under Organization → Billing " +
			"(https://cloud.live-llm.com/organization/billing), or free resources first."},
		{"pool exceeded", "Cannot create VM: pool exceeded\n\nThe workspace's plan does not have enough free CPU/RAM/disk for this. " +
			"Raise the plan (or turn on pay as you go) at https://cloud.live-llm.com/billing, or free resources first."},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", &client.APIError{Status: 402, Body: c.body})
		if len(diags) != 1 || diags[0].Summary() != "Workspace plan pool exceeded" || diags[0].Detail() != c.want {
			t.Errorf("402 %s:\n got %v\nwant %q", c.body, diags, c.want)
		}
	}
}

// A billing refusal the API names by its code gets a diagnostic of its own:
// what it is called and what to do, after the platform's words (never the raw
// body: a payment's link stays out of the output). The code decides, so a
// payg_off says to turn on pay as you go whatever its words, and a 403
// payg_not_offered isn't taken for a permission. A code this provider doesn't
// know keeps the pool advice.
func TestBillingRefusalWording(t *testing.T) {
	for _, c := range []struct {
		status        int
		body          string
		summary, want string
	}{
		{402, `{"error":"Free includes no resources. Turn on pay as you go, or choose Pro, on the Billing page.","code":"payg_off"}`,
			"Workspace plan pool exceeded",
			"Cannot create VM: Free includes no resources. Turn on pay as you go, or choose Pro, on the Billing page.\n\n" +
				"The plan has no room for this, and pay as you go is off. A person turns on pay as you go, or chooses a " +
				"bigger plan, on the console's Billing page (https://cloud.live-llm.com/billing), or free resources first."},
		{402, `{"error":"This would bring pay as you go to $140 a month, past your $100 limit. Raise it on the Billing page.","code":"payg_limit"}`,
			"Pay-as-you-go limit reached",
			"Cannot create VM: This would bring pay as you go to $140 a month, past your $100 limit. Raise it on the Billing page.\n\n" +
				"This would take the account's pay as you go past its monthly limit. A person raises the limit on the " +
				"console's Billing page (https://cloud.live-llm.com/billing); an API key can only lower it. Or free " +
				"resources first."},
		{402, `{"error":"Add a card first: Billing → Add a card.","code":"card_required","checkout":"card"}`,
			"A card is needed",
			"Cannot create VM: Add a card first: Billing → Add a card.\n\n" +
				"Pay as you go and paid plans need a card on file. A person adds one on the console's Billing page " +
				"(https://cloud.live-llm.com/billing, Add a card); an API key can't. Then apply again."},
		{402, `{"error":"A payment failed on 3 November. Pay the open invoice to add resources.","code":"payment_overdue","payUrl":"https://invoice.example/i/secret"}`,
			"A payment is overdue",
			"Cannot create VM: A payment failed on 3 November. Pay the open invoice to add resources.\n\n" +
				"A payment failed, so nothing can be added until it is paid; what runs keeps running. A person pays the " +
				"open invoice on the console's Billing page (https://cloud.live-llm.com/billing), then apply again."},
		{403, `{"error":"Pay as you go isn't open to new accounts yet.","code":"payg_not_offered"}`,
			"Pay as you go isn't available",
			"Cannot create VM: Pay as you go isn't open to new accounts yet.\n\n" +
				"Pay as you go and paid plans aren't open to this account yet; LiveLLM opens them for an account. Until " +
				"then keep the workspace within its plan, or free resources first."},
		{402, `{"error":"Something new.","code":"something_new"}`,
			"Workspace plan pool exceeded",
			"Cannot create VM: " + `{"error":"Something new.","code":"something_new"}` + "\n\nThe workspace's plan does not have enough free CPU/RAM/disk for this. " +
				"Raise the plan (or turn on pay as you go) at https://cloud.live-llm.com/billing, or free resources first."},
	} {
		var diags diag.Diagnostics
		apiDiag(&diags, "Cannot create VM", &client.APIError{Status: c.status, Body: c.body})
		if len(diags) != 1 || diags[0].Summary() != c.summary || diags[0].Detail() != c.want {
			t.Errorf("%d %s:\n got %v\nwant %q: %q", c.status, c.body, diags, c.summary, c.want)
		}
		if strings.Contains(diags[0].Detail(), "invoice.example") {
			t.Errorf("%s: the payment's link is in the output", c.body)
		}
	}
	// any other 403 is not a billing refusal
	var diags diag.Diagnostics
	apiDiag(&diags, "Cannot create VM", &client.APIError{Status: 403, Body: `{"error":"Not yours.","code":"payg_off_typo"}`})
	if len(diags) != 1 || diags[0].Summary() != "Cannot create VM" {
		t.Errorf("a 403 without a billing code: %v", diags)
	}
}
