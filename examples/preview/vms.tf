# ── A terminal (headless) Ubuntu VM ────────────────────────────────────────
resource "livellm_vm" "worker" {
  name      = "worker"
  cpu       = 2
  memory_gi = 4
  disk_gi   = 20

  # SSH login for the VM (write-only — never lands in your tfstate).
  credentials {
    username    = "dev"
    password_wo = var.vm_password
  }

  # Expose an app port on a public HTTPS hostname.
  port {
    name = "api"
    port = 3000
  }
}

# ── A GUI Linux Desktop VM with an AI daemon ───────────────────────────────
# The daemon is an agent that lives next to the VM and operates it (shell,
# GUI, screenshots) — chat with it from the console or dispatch tasks via
# the API.
resource "livellm_vm" "dev" {
  name      = "dev-box"
  desktop   = true
  cpu       = 4
  memory_gi = 8
  disk_gi   = 40

  credentials {
    username    = "dev"
    password_wo = var.vm_password
  }

  ai_daemon {
    provider     = "zai-coding-plan" # a provider connected on /integrations
    model        = "glm-5.2"
    sudo         = true
    instructions = "Prefer uv for Python projects."
  }
}

# ── Stop a VM without destroying it (disk kept, billing drops to disk-only) ─
resource "livellm_vm" "archive" {
  name    = "archive"
  cpu     = 2
  memory_gi = 4
  disk_gi = 100
  stopped = true
}
