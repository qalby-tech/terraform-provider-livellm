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

## What you can manage

| Resource | What it creates |
|---|---|
| [`livellm_vm`](resources/vm.md) | Ubuntu VM (terminal or desktop), ports, placement, optional AI agent |
| [`livellm_container_app`](resources/container_app.md) | Container app from an image or a repo built on every push |
| [`livellm_storage`](resources/storage.md) | Managed Postgres or Redis, backups, external TLS access |
| [`livellm_secret`](resources/secret.md) | Workspace secret with a write-only value |

Data sources: [`livellm_workspace`](data-sources/workspace.md),
[`livellm_vm`](data-sources/vm.md), [`livellm_vms`](data-sources/vms.md).

Creates and updates wait until the resource is actually serving; plan-pool
exhaustion surfaces as a clear "raise your plan" diagnostic. Browsers, AI
masters and provider connections are next.

~> Write-only arguments (`password_wo`, `value_wo`) require Terraform 1.11+ or
OpenTofu 1.11+.
