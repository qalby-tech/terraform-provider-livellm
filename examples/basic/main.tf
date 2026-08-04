terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

provider "livellm" {
  # export LIVELLM_API_KEY=llc_…
}

data "livellm_workspace" "this" {}

output "workspace_name" {
  value = data.livellm_workspace.this.name
}

output "workspace_plan" {
  value = data.livellm_workspace.this.plan
}
