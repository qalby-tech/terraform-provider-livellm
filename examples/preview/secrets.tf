# ── Write-only workspace secrets ───────────────────────────────────────────
# Values live under Vault-like paths, are versioned and rollbackable, and are
# never returned by the API — nor stored in your tfstate (write-only args).
# Reference one from a workload via secret_env (see apps.tf).
resource "livellm_secret" "tg_token" {
  path             = "/prod/tg-bot-token"
  value_wo         = var.tg_token
  value_wo_version = 1 # bump to rotate
}

resource "livellm_secret" "stripe_key" {
  path             = "/prod/stripe-key"
  value_wo         = var.stripe_key
  value_wo_version = 1
}

variable "tg_token" {
  type      = string
  sensitive = true
}

variable "stripe_key" {
  type      = string
  sensitive = true
}

variable "db_password" {
  type      = string
  sensitive = true
}

variable "vm_password" {
  type      = string
  sensitive = true
}
