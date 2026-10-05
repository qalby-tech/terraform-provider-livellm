---
page_title: "livellm Provider"
description: |-
  Manage a livellm cloud workspace as code — VMs, managed databases,
  container apps and browsers, driven by the same public API as the
  dashboard.
---

# livellm Provider

The livellm provider manages a [livellm cloud](https://cloud.live-llm.com)
workspace: reviewable, versioned, repeatable infrastructure.

Authentication is a single workspace API key — each key manages the
workspace it was minted in. Mint keys on your workspace's
[API keys page](https://cloud.live-llm.com/api-keys).

## Example Usage

```terraform
terraform {
  required_providers {
    livellm = {
      source = "qalby-tech/livellm"
    }
  }
}

# Reads LIVELLM_API_KEY from the environment when api_key is unset.
provider "livellm" {}
```

Prefer the `LIVELLM_API_KEY` environment variable over hardcoding the key in
configuration; keys are secrets.

## Schema

### Optional

- `api_key` (String, Sensitive) Workspace API key (`llc_…`). Read from the
  `LIVELLM_API_KEY` environment variable when unset.
- `endpoint` (String) API endpoint. Defaults to the public livellm cloud API;
  only set this for self-hosted installations.

## What you can manage

| Resource | What it creates |
|---|---|
| [`livellm_vm`](resources/vm.md) | A machine: Ubuntu (terminal or desktop), Debian, Fedora or Windows; SSH keys, ports, a stop time |
| [`livellm_container_app`](resources/container_app.md) | Container app from an image or a Git repo the platform builds |
| [`livellm_storage`](resources/storage.md) | Managed Postgres or Redis, backups, external TLS access |
| [`livellm_browser`](resources/browser.md) | A browser with a live view: Chrome (CDP) or Camoufox (Playwright); locale, time zone and rotating proxies |
| [`livellm_browser_api`](resources/browser_api.md) | One address that drives several browsers, Chrome and Camoufox together |
| [`livellm_desktop_app`](resources/desktop_app.md) | A Linux desktop in a container that starts in seconds |

Data sources: [`livellm_workspace`](data-sources/workspace.md),
[`livellm_vm`](data-sources/vm.md), [`livellm_vms`](data-sources/vms.md),
[`livellm_hosts`](data-sources/hosts.md).

Every resource can say where it runs with `placement_strategy`,
`placement_region` and `placement_host`. Left out, LiveLLM picks the host (the
default); a region runs it on any of that region's hosts; a host pins it, and
it waits for that host while the host is down. Changing it restarts the
resource where it now belongs. A resource waiting for room still counts toward
the plan. [`livellm_hosts`](data-sources/hosts.md) lists the hosts and regions.

## Inside the workspace

Resources in a workspace can't reach each other unless you say so. A resource
this provider creates starts closed to the rest of the workspace, and
`reachable_from`, on every resource, says who may connect to it:

- left out when creating, or `[]`: nothing in the workspace (a new service of
  an app that is already there takes that app's value instead);
- names: those resources. A service of an app made of several services, or
  that app's `stack`, stands for all its services;
- `["*"]`: the whole workspace, also resources made later. `"*"` goes alone.

Whatever it says, a resource is also reached by:

- its own parts: the services of one stack reach each other on any port, and a
  database's copies reach each other;
- the apps that link it with a `database` block or wait for it with
  `starts_after`;
- for a browser, the Browser API that holds it, and with it whatever may reach
  that Browser API (it asks no key inside the workspace). Whatever drives a
  browser or a desktop acts from its place.

A caller that is let in reaches every port of the resource. Public addresses
keep their own settings: `allow_cidrs`, `allowlist` and public ports open
nothing inside the workspace, and closing a resource inside closes nothing
public. A public address can be reached from the workspace too, through the
edge, so making a resource public opens it to everyone.

```terraform
resource "livellm_storage" "cache" {
  name   = "cache"
  engine = "redis"

  password_wo         = var.redis_password
  password_wo_version = 1

  # The worker machine may connect; nothing else in the workspace.
  reachable_from = [livellm_vm.worker.name]
}
```

- Removing `reachable_from` from a configuration keeps the value the resource
  has, and Terraform stops managing it. Write `reachable_from = []` to close
  it.
- The services of one stack share one value: a change on one service changes
  it on all of them. Set it on one service and leave it out of the others, or
  write the same value on each; two different values make every apply undo the
  other one. Naming another service of the same stack is refused at apply (the
  plan can't see other resources).
- Resources made before the setting existed were given a value when it
  arrived, mostly `["*"]` (the whole workspace); a refresh reads whatever it
  is.
- A platform without the setting reads it as null, and an apply that sets it
  warns that it wasn't kept.

### Letting more in with an API key

- A key that links resources it made itself needs nothing extra: a `database`
  block, `starts_after` or `reachable_from` between two resources this key
  created.
- Letting in a resource the key didn't make, or reaching one a person (or
  another key) made, needs a key with the **Network** permission, unless the
  one reached already lets the whole workspace in. Setting
  `reachable_from = ["*"]` always needs it, whoever made the resource.
  Putting a browser into a Browser API that other resources may reach, or
  turning on `all_browsers`, is judged the same way (a new browser joining a
  Browser API whose `all_browsers` was already on needs nothing). A person
  turns Network on for the key on the workspace's API keys page; without it
  the apply fails with the platform's words. Narrowing or closing never needs
  it.
- To link a new app to a database a person made, use a key with Network, or
  have a person let the whole workspace reach that database in the console
  first. Naming the new app in the database's `reachable_from` doesn't work:
  the app doesn't exist yet when the database is written.
- Keys and their permissions aren't managed by this provider.

### Replacing a resource

Some changes replace a resource: the plan says it *must be replaced* (a
machine's `os`, a database's `engine`, a browser's `engine`, and the like), as
do `terraform apply -replace` and a tainted resource. A replacement deletes
the resource and makes a new one with the same name:

- The new one is made like any new resource. With `reachable_from` left out of
  the configuration it starts closed, whatever the old one let in (a service
  of an app that keeps other services takes the app's value). Write
  `reachable_from` to keep it.
- When the old one is deleted, the platform drops its name from every other
  resource's `reachable_from`, so those don't let the new one in after the
  apply. Where their configuration writes `reachable_from`, the next plan
  shows the difference and a second apply puts the name back; where it leaves
  `reachable_from` out, the name stays gone until someone sets it again.
- A resource an app links with a `database` block or waits for with
  `starts_after` can't be deleted while the app lists it, so such a
  replacement is refused until the link is taken out.

The plan warns when a replacement its own attributes ask for changes who
reaches what; `-replace` and a tainted resource don't get that warning.

Creates and updates wait until the resource is actually serving; plan-pool
exhaustion surfaces as a clear "raise your plan" diagnostic. Create/update timeouts are configurable per resource via the standard `timeouts` block.

~> Write-only arguments (`password_wo`, `auth_wo`) require Terraform 1.11+ or
OpenTofu 1.11+.
