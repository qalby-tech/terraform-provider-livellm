---
page_title: "livellm_browser Resource - livellm"
description: |-
  A headless Chromium browser, optionally driven by a built-in AI agent.
---

# livellm_browser (Resource)

Creates a headless Chromium browser. Add the `ai_agent` block and it becomes
an autonomous browser agent: hand it goals from the console or the task API,
and review its trajectory step by step.

## Example Usage

```terraform
resource "livellm_browser" "scraper" {
  name   = "scraper"
  memory = "2Gi"

  ai_agent {
    provider = "anthropic" # a provider connected on the Integrations page
  }
}
```

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the browser.

### Optional

- `cpu` (String) CPU request, e.g. `1`.
- `memory` (String) Memory request, e.g. `2Gi`.
- `ai_agent` (Block, Single) The browser's built-in AI driver:
  - `provider` (String) A connected AI provider id. Defaults to any connected provider.
  - `model` (String) Model id; defaults to the provider's recommendation.
  - `instructions` (String) Standing guidance for the agent.
- `timeouts` (Block) `create` / `delete` (default 10m each).

### Read-Only

- `ready` (Boolean) Whether the browser is up.

## Import

```shell
terraform import livellm_browser.scraper scraper
```
