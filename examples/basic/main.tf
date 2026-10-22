terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

# The API key is all the configuration there is — no workspace name,
# nothing else to wire up. export LIVELLM_API_KEY=llc_…
provider "livellm" {}

# Optional: reads your workspace's facts (name, plan) for use elsewhere in
# the config — e.g. building hostnames in outputs — and fails fast at plan
# time if the key is wrong. Resources never need it.
data "livellm_workspace" "this" {}

output "workspace_name" {
  value = data.livellm_workspace.this.name
}

output "workspace_plan" {
  value = data.livellm_workspace.this.plan
}
