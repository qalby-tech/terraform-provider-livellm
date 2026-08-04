# ── Connect an AI provider ─────────────────────────────────────────────────
# The key every agent in the workspace uses. Same write-only treatment as
# secrets — it never lands in your tfstate.
resource "livellm_ai_provider" "zai" {
  provider_id    = "zai-coding-plan"
  api_key_wo     = var.zai_key
  api_key_wo_version = 1
}

variable "zai_key" {
  type      = string
  sensitive = true
}

# ── The AI Master ──────────────────────────────────────────────────────────
# The workspace's orchestrator: it breaks goals into tasks, delegates each to
# the right agent (VM daemons, browser agents, database engineers), reviews
# their work, and schedules recurring loops. One per workspace.
resource "livellm_agent_master" "master" {
  name     = "Master 1"
  provider = livellm_ai_provider.zai.provider_id
  model    = "glm-5.2"

  # Let it answer routine agent questions itself instead of relaying each
  # one to you; deletions always require your approval regardless.
  autonomous = true

  # Let it approve AI-requested dependency resources (databases an app
  # scaffold declares) without waiting for you.
  auto_provision = true

  instructions = "Working hours are 09:00-18:00 UTC+3; batch reports at 18:00."
}
