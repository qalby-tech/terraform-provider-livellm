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

provider "livellm" {
  # api_key = "llc_…"   # or export LIVELLM_API_KEY
}

data "livellm_workspace" "this" {}

output "workspace" {
  value = data.livellm_workspace.this.name
}
```

The API key is **workspace-scoped**: one provider block manages exactly one
workspace. Mint keys on the workspace's Integrations page.

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
