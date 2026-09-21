// Package client is a thin hand-written LiveLLM Cloud API client.
//
// PLANNED: replace the request plumbing with a client generated from
// tenant-api's api/openapi.yaml (oapi-codegen) once the surface grows past a
// handful of calls — see docs/terraform-provider.md in the cluster repo. The
// wrapper (auth header, error typing, Ready-polling) stays either way and is
// the piece the future `livellm` CLI reuses.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultEndpoint = "https://api.live-llm.com"

type Client struct {
	endpoint string
	apiKey   string
	http     *http.Client
}

func New(endpoint, apiKey string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		apiKey:   apiKey,
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

// APIError is a non-2xx response, preserving the server's error body.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("livellm api %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return &APIError{Status: res.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Workspace is the one workspace an llc_ key belongs to (GET /v1/workspace).
type Workspace struct {
	Name  string `json:"name"`
	Plan  string `json:"plan"`
	Owner string `json:"owner"`
}

// MyWorkspace resolves the key's workspace — also the provider's auth check.
func (c *Client) MyWorkspace(ctx context.Context) (*Workspace, error) {
	var w Workspace
	if err := c.do(ctx, http.MethodGet, "/v1/workspace", nil, &w); err != nil {
		return nil, err
	}
	return &w, nil
}

// EndpointStatus is one exposed port on a workload: HTTP ports carry the
// public HTTPS URL; raw TCP/UDP ports carry the host:port to connect to.
type EndpointStatus struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
	TCP  bool   `json:"tcp,omitempty"`
	Addr string `json:"addr,omitempty"`
}

// WorkloadStatus is the live view of one workload (GET /v1/status).
type WorkloadStatus struct {
	ID        string           `json:"id"`
	Type      string           `json:"type"`
	Phase     string           `json:"phase"`
	Ready     bool             `json:"ready"`
	Message   string           `json:"message,omitempty"`
	SSH       string           `json:"ssh,omitempty"`
	Endpoints []EndpointStatus `json:"endpoints,omitempty"`
}

type TenantStatus struct {
	Workloads []WorkloadStatus `json:"workloads"`
}

// Status returns the live status of every workload in the key's workspace.
func (c *Client) Status(ctx context.Context) (*TenantStatus, error) {
	var st TenantStatus
	if err := c.do(ctx, http.MethodGet, "/v1/status", nil, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

// --- Workloads --------------------------------------------------------------

// Workload is one entry of the workspace spec's workloads list. Kind blocks
// stay loose maps: each typed resource owns its own field mapping.
type Workload struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Stopped bool   `json:"stopped,omitempty"`
	// ExpiresAt is when the platform will stop this machine (RFC 3339). It is
	// read-only: a stop time is set by sending vm.stopAfter.
	ExpiresAt string         `json:"expiresAt,omitempty"`
	VM        map[string]any `json:"vm,omitempty"`
	Pod       map[string]any `json:"pod,omitempty"`
	Storage   map[string]any `json:"storage,omitempty"`
	Browser   map[string]any `json:"browser,omitempty"`
}

// Workloads returns the workspace spec's workloads list.
func (c *Client) Workloads(ctx context.Context) ([]Workload, error) {
	var w struct {
		Spec struct {
			Workloads []Workload `json:"workloads"`
		} `json:"spec"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/workspace", nil, &w); err != nil {
		return nil, err
	}
	return w.Spec.Workloads, nil
}

// CreateWorkload posts a new workload of the given type; body is the kind's
// flat field set (id included).
func (c *Client) CreateWorkload(ctx context.Context, wtype string, body map[string]any) error {
	return c.do(ctx, http.MethodPost, "/v1/workloads/"+wtype, body, nil)
}

// UpdateWorkload PUTs the full desired workload (the path id wins).
func (c *Client) UpdateWorkload(ctx context.Context, id string, w Workload) error {
	return c.do(ctx, http.MethodPut, "/v1/workloads/"+id, w, nil)
}

func (c *Client) DeleteWorkload(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/workloads/"+id, nil, nil)
}
