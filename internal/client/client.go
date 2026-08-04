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

// Workspace is the tenant an llc_ key is scoped to (GET /v1/me/tenant).
type Workspace struct {
	Name  string         `json:"name"`
	Plan  string         `json:"plan"`
	Owner string         `json:"owner"`
	Spec  map[string]any `json:"spec"`
}

// MyWorkspace resolves the key's workspace — also the provider's auth check.
func (c *Client) MyWorkspace(ctx context.Context) (*Workspace, error) {
	var w Workspace
	if err := c.do(ctx, http.MethodGet, "/v1/me/tenant", nil, &w); err != nil {
		return nil, err
	}
	return &w, nil
}
