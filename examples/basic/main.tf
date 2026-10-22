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

# A machine with dedicated cores: every core reserved for it on its server,
# instead of shared (the default). Where the servers' cores run at different
# clock speeds they are all of one speed: core_speed (MHz) would ask for one,
# and left out, as here, the platform picks any that fit.
variable "machine_password" {
  type      = string
  sensitive = true
}

resource "livellm_vm" "build" {
  name      = "build-box"
  username  = "dev"
  cpus      = 2
  memory_gi = 4

  password_wo         = var.machine_password
  password_wo_version = 1

  dedicated_cores = true
}

# The clock in MHz of the cores the running machine got: null while it isn't
# running, when they run at different speeds and where the servers list no
# speeds.
output "build_clock" {
  value = livellm_vm.build.running_core_speed
}
