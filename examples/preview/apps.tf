# ── A container app from a public image ────────────────────────────────────
# Each HTTP port gets its own public HTTPS hostname.
resource "livellm_container_app" "web" {
  name  = "web"
  image = "nginx:1.27"
  expose { port = 80 }
}

# ── An app built from a git repo (push-to-deploy) ──────────────────────────
# The platform builds the repo's Dockerfile on every push to the default
# branch and rolls the app automatically.
resource "livellm_container_app" "api" {
  name = "api"
  source {
    repo = "acme/api" # a repo in your workspace org (or a connected mirror)
  }
  cpu    = "500m"
  memory = "512Mi"
  env = {
    DB_HOST = livellm_storage.db.host
    DB_NAME = "app"
  }
  secret_env {
    name = "TELEGRAM_BOT_TOKEN"
    path = livellm_secret.tg_token.path
  }
  expose { port = 8080 }

  # Persistent volume mounted into the container.
  volume {
    size_gi = 5
    path    = "/data"
  }

  # Optional: an AI daemon that develops this app — it edits a checkout and
  # ships by pushing (which rebuilds and redeploys).
  ai_daemon {
    provider = "zai-coding-plan"
    model    = "glm-5.2"
  }
}
