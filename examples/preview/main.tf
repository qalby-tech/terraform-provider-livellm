# PREVIEW — the v0.1 resource set is in development; these shapes show where
# the provider is headed. Today only the provider config and
# data.livellm_workspace (see ../basic) work.

terraform {
  required_providers {
    livellm = { source = "qalby-tech/livellm" }
  }
}

provider "livellm" {} # export LIVELLM_API_KEY=llc_…

# ── A Linux VM — with an AI daemon so agents can drive it ──────────────────
resource "livellm_vm" "dev" {
  name      = "dev-box"
  desktop   = true # GUI Linux Desktop; omit for a terminal VM
  cpu       = 4
  memory_gi = 8
  disk_gi   = 40
  ai_daemon {
    provider = "zai-coding-plan" # a provider connected on /integrations
    model    = "glm-5.2"
  }
}

# ── A managed Postgres ─────────────────────────────────────────────────────
resource "livellm_storage" "db" {
  name    = "app-db"
  engine  = "postgres"
  version = "16"
  disk_gi = 20
  backup { schedule = "0 3 * * *" }
}

# ── A write-only workspace secret (never lands in your tfstate) ────────────
resource "livellm_secret" "tg_token" {
  path             = "/prod/tg-bot-token"
  value_wo         = var.tg_token
  value_wo_version = 1 # bump to rotate
}

variable "tg_token" {
  type      = string
  sensitive = true
}

# ── A container app wired to both ──────────────────────────────────────────
resource "livellm_container_app" "bot" {
  name  = "tg-bot"
  image = "nginx:1.27"
  env   = { DB_HOST = livellm_storage.db.host }
  secret_env {
    name = "TELEGRAM_BOT_TOKEN"
    path = livellm_secret.tg_token.path
  }
  expose { port = 8080 }
}

# ── An agent-driven browser ────────────────────────────────────────────────
resource "livellm_browser" "scraper" {
  name = "scraper"
  ai_agent {
    provider = "zai-coding-plan"
    model    = "glm-5.2"
  }
}
