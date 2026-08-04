# ── An agent-driven browser ────────────────────────────────────────────────
# A real Chromium the built-in AI agent operates: give it tasks, review its
# step-by-step trajectory, watch it live over NoVNC.
resource "livellm_browser" "scraper" {
  name = "scraper"
  ai_agent {
    provider     = "zai-coding-plan"
    model        = "glm-5.2"
    instructions = "Never log into accounts without asking first."
  }
}

# ── A plain browser + the Browser API ──────────────────────────────────────
# The Browser API drives plain browsers programmatically over HTTP:
# /search, /content, /interact, /attribute. A browser has ONE driver —
# either its AI agent or the Browser API, never both.
resource "livellm_browser" "plain" {
  name = "plain"
}

resource "livellm_browser_api" "api" {
  name     = "browser-api"
  browsers = [livellm_browser.plain.name]

  # Bring your own remote browser too (any ws:// CDP endpoint).
  external_browser {
    name   = "remote-edge"
    ws_url = "wss://browser.example.com/cdp"
  }
}
