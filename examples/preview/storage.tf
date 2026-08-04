# ── Managed Postgres ───────────────────────────────────────────────────────
resource "livellm_storage" "db" {
  name    = "app-db"
  engine  = "postgres"
  version = "16"
  disk_gi = 20
  tls     = true

  credentials {
    username    = "app"
    password_wo = var.db_password # write-only: never lands in tfstate
  }

  # Nightly backups, keep the last 10.
  backup {
    schedule    = "0 3 * * *"
    max_backups = 10
  }

  # Optional public endpoint (TLS + allowlist) for external clients.
  network {
    expose    = true
    allowlist = ["203.0.113.0/24"]
  }

  # Optional: a Database Engineer agent — runs migrations (git-versioned),
  # inspects performance, answers data questions in chat.
  ai_daemon {
    provider = "zai-coding-plan"
    model    = "glm-5.2"
  }
}

# ── Managed Redis (3-node replicated) ──────────────────────────────────────
resource "livellm_storage" "cache" {
  name      = "cache"
  engine    = "redis"
  instances = 3
  disk_gi   = 2
}
