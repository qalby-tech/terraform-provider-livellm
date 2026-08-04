# PREVIEW — the v0.1 resource set is in development; these files show every
# planned resource shape. Today only the provider config and
# data.livellm_workspace (see ../basic) work.
#
# One file per domain:
#   vms.tf       — terminal / desktop VMs, AI daemons
#   browsers.tf  — browsers, agent-driven browsers, the Browser API
#   apps.tf      — container apps from an image or built from a git repo
#   storage.tf   — managed Postgres and Redis
#   secrets.tf   — write-only workspace secrets
#   agents.tf    — the AI master and provider connections

terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

provider "livellm" {} # export LIVELLM_API_KEY=llc_…
