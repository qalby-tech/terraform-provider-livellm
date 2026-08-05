---
page_title: "livellm_secret Resource - livellm"
description: |-
  A workspace secret under a Vault-like path, with a write-only value.
---

# livellm_secret (Resource)

Stores a secret in the workspace secret store. The value is a **write-only
argument**: it never lands in Terraform state, and the platform never returns
it — not to you, not to the console, only to workloads that consume it at run
time. Rotation is explicit: bump `value_wo_version`.

## Example Usage

```terraform
variable "bot_token" {
  type      = string
  sensitive = true
}

resource "livellm_secret" "bot_token" {
  path             = "/prod/tg-bot-token"
  value_wo         = var.bot_token
  value_wo_version = 1 # bump to rotate
}
```

Consume it from a container app without the value passing through Terraform:

```terraform
resource "livellm_container_app" "bot" {
  name  = "tg-bot"
  image = "ghcr.io/acme/bot:1.4"

  secret_env {
    name = "TELEGRAM_BOT_TOKEN"
    path = livellm_secret.bot_token.path
  }
}
```

~> **Requires Terraform 1.11+ / OpenTofu 1.11+** — earlier versions reject
write-only attributes.

## Schema

### Required

- `path` (String) Vault-like secret path, e.g. `/prod/tg-bot-token`. Changing it replaces the secret.
- `value_wo` (String, Sensitive, Write-only) The secret value. Not stored in state; re-sent only when `value_wo_version` changes.
- `value_wo_version` (Number) Rotation trigger for `value_wo`.

### Read-Only

- `current_version` (Number) The platform-side version currently serving this path — every write increments it.

## Import

```shell
terraform import livellm_secret.bot_token /prod/tg-bot-token
```

The value cannot be imported (it is unreadable by design); `value_wo_version`
starts at 1 after import.
