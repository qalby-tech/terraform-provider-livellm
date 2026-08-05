---
page_title: "livellm_ai_provider Resource - livellm"
description: |-
  Connects an AI provider to the workspace, with a write-only key.
---

# livellm_ai_provider (Resource)

Connects an AI provider to the workspace — the credential all agents
(VM daemons, the master, browser agents) use. The key is write-only on the
platform **and** in Terraform: stored for the agents' use, never returned to
anyone. Rotation is an explicit version bump.

## Example Usage

```terraform
resource "livellm_ai_provider" "anthropic" {
  provider_id        = "anthropic"
  api_key_wo         = var.anthropic_key
  api_key_wo_version = 1
}
```

A Claude Pro/Max subscription instead of an API key — run `claude setup-token`
locally and use the printed token:

```terraform
resource "livellm_ai_provider" "claude_plan" {
  provider_id        = "claude-subscription"
  api_key_wo         = var.claude_setup_token
  api_key_wo_version = 1
}
```

## Schema

### Required

- `provider_id` (String) A catalog id — e.g. `anthropic`, `openai`, `zai-coding-plan`, `claude-subscription`. Changing it replaces the connection.
- `api_key_wo` (String, Sensitive, Write-only) The API key or subscription token. Never stored in state.
- `api_key_wo_version` (Number) Rotation trigger for `api_key_wo`.

## Import

```shell
terraform import livellm_ai_provider.anthropic anthropic
```

The key cannot be imported (write-only); `api_key_wo_version` starts at 1.
