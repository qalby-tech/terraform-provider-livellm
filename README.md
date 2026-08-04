# terraform-provider-livellm

Terraform/OpenTofu provider for [LiveLLM Cloud](https://cloud.live-llm.com) —
manage workspace resources (VMs, container apps, managed databases, browsers,
secrets) as code against the public API.

Design doc: `docs/terraform-provider.md` in the `qalby-tech/cluster` repo.

## Status

v0 scaffold. Working today:

- provider config (`api_key` / `LIVELLM_API_KEY`, optional `endpoint`) with an
  auth check at configure time
- `data.livellm_workspace`

v0.1 roadmap (in order): `livellm_secret`, `livellm_storage`,
`livellm_container_app`, `livellm_vm` — each with async Ready-polling and
plan-pool (402) diagnostics.

## Usage

```hcl
terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

provider "livellm" {} # export LIVELLM_API_KEY=llc_…
```

The key is all the configuration there is — no workspace name, nothing else
to wire up. Mint keys on the workspace's API keys page.

## Examples

- [`examples/basic`](examples/basic) — provider setup + the optional
  `livellm_workspace` data source (workspace facts, fail-fast auth check).
- [`examples/preview`](examples/preview) — the planned v0.1 resource shapes:
  a VM with an AI daemon, managed Postgres, write-only secrets, a wired
  container app, an agent-driven browser.

## Development

```sh
go build ./...

# run against a local override (no registry needed):
cat > ~/.terraformrc <<EOF
provider_installation {
  dev_overrides {
    "qalby-tech/livellm" = "$(pwd)"
  }
  direct {}
}
EOF
go build -o terraform-provider-livellm && cd examples/basic && terraform plan
```
