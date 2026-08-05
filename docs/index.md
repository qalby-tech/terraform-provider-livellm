---
page_title: "livellm Provider"
description: |-
  Manage a livellm cloud workspace as code — VMs, managed databases,
  container apps, browsers, secrets and AI agents, driven by the same
  public API as the dashboard.
---

# livellm Provider

The livellm provider manages a [livellm cloud](https://cloud.live-llm.com)
workspace: reviewable, versioned, repeatable infrastructure — and a natural
surface for AI agents to write.

Authentication is a single workspace API key — each key manages the
workspace it was minted in. Mint keys on your workspace's
[API keys page](https://cloud.live-llm.com/api-keys).

## Example Usage

```terraform
terraform {
  required_providers {
    livellm = {
      source = "qalby-tech/livellm"
    }
  }
}

# Reads LIVELLM_API_KEY from the environment when api_key is unset.
provider "livellm" {}
```

Prefer the `LIVELLM_API_KEY` environment variable over hardcoding the key in
configuration; keys are secrets.

## Schema

### Optional

- `api_key` (String, Sensitive) Workspace API key (`llc_…`). Read from the
  `LIVELLM_API_KEY` environment variable when unset.
- `endpoint` (String) API endpoint. Defaults to the public livellm cloud API;
  only set this for self-hosted installations.

## Provider status

This provider is an early preview. It ships the provider configuration and
the `livellm_workspace` data source; typed resources (VMs, managed
Postgres/Redis, container apps, browsers, write-only secrets, AI agents) are
in active development — the repository's `examples/preview` directory shows
the planned shapes.
