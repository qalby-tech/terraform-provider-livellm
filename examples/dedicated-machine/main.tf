terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

# export LIVELLM_API_KEY=llc_…
provider "livellm" {}

# The servers' clock speeds, fastest first, each with its CPU model and the
# most cores of it a new machine can get on one server: empty where every
# server runs at one speed. Each speed is a value core_speed takes.
data "livellm_machine_cores" "this" {}

output "core_speeds" {
  value = data.livellm_machine_cores.this.speeds
}

# A machine with dedicated cores: whole cores of its server for it alone,
# instead of shared (the default). On servers whose cores have 2 threads they
# come in pairs (cpus = 2, 4, 6...), and each counts as 2 of the plan's cores.
# Where the servers' cores run at different clock speeds they are all of one
# speed: core_speed (MHz) would ask for one, and left out, as here, the
# platform picks any that fit.
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

# The clock in MHz of the cores the running machine got (dedicated cores
# only: shared cores have no clock): null while it isn't running, when they
# run at different speeds and where the servers list no speeds.
output "build_clock" {
  value = livellm_vm.build.running_core_speed
}

# The CPU model of the server the running machine runs on, shared or
# dedicated: "Intel Core i9-14900K".
output "build_cpu" {
  value = livellm_vm.build.running_cpu_model
}
