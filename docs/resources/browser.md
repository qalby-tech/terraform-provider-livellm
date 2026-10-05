---
page_title: "livellm_browser Resource - livellm"
description: |-
  A browser with a live view and an automation endpoint: Chrome or Camoufox.
---

# livellm_browser (Resource)

Creates a browser. Watch it live from the console, or drive it from your own
automation: a Chrome browser over its CDP endpoint, a Camoufox browser with
Playwright.

A browser can speak a language and live in a time zone of its own, answer a
fixed location or none, and send its traffic through proxies that rotate,
mobile proxies with a change-IP address included.

## Example Usage

```terraform
resource "livellm_browser" "scraper" {
  name   = "scraper"
  memory = "2Gi"
}
```

In Russian, on Moscow time, with no location for pages that ask:

```terraform
resource "livellm_browser" "ru" {
  name     = "ru"
  locale   = "ru-RU"
  timezone = "Europe/Moscow"

  geolocation {
    mode = "off"
  }
}
```

Through two proxies, the second with a login and a change-IP address, moving to
the next one each time a Browser API session starts:

```terraform
resource "livellm_browser" "proxied" {
  name = "proxied"

  proxy {
    upstream {
      name   = "res-1"
      server = "http://proxy-1.example.com:3128"
    }
    upstream {
      name             = "mobile-1"
      server           = "socks5://mobile.example.com:1080"
      username_wo      = var.mobile_proxy_user
      password_wo      = var.mobile_proxy_password
      change_ip_url_wo = var.mobile_change_ip_url
    }
    auth_version = 1   # bump it to send new values for the *_wo arguments

    rotation {
      mode = "session"
    }
  }
}
```

A Camoufox browser (Firefox-based), for sites that turn a Chrome browser away:

```terraform
resource "livellm_browser" "fox" {
  name   = "fox"
  engine = "camoufox"
}
```

Pinned to one host (ids come from the `livellm_hosts` data source):

```terraform
resource "livellm_browser" "pinned" {
  name = "pinned"

  placement_strategy = "host"
  placement_host     = "host-1"
}
```

## Locale and time zone

`locale`, `timezone`, `languages` and `geolocation` restart the browser when
they change. The profile, the connection address and the live view stay. The
locales on offer are listed at `GET /v1/browsers/locales`.

## Engines

- `chrome` (the default) is driven over CDP, with any CDP client.
- `camoufox` is a Firefox-based browser. Drive it with Playwright 1.62 only
  (`firefox.connect`, with the address and headers that connecting to the
  browser answers), in its first context. It takes no extensions; uBlock
  Origin is built in.
- The engine is fixed when the browser is made. Changing it, or removing
  `engine = "camoufox"` from the configuration, replaces the browser, and the
  new one starts with an empty profile. Profiles move only between browsers of
  one engine; cookies can be imported into either.
- A [Browser API](browser_api.md) holds browsers of either engine; a session
  can ask for one when it starts.
- A platform that doesn't offer Camoufox refuses the create with its message.
  A platform too old to know engines makes a Chrome browser instead: the
  apply then fails and the state holds `chrome`.

## Proxies

- Adding the `proxy` block restarts the browser once. After that, changing
  upstreams, logins or rotation does not restart it. Removing the block
  restarts it again.
- Adding the block restarts the browser even when it has no `upstream`. Once
  the block is there, emptying it (no `upstream`) sends the browser straight
  out without a restart.
- When an upstream can't be reached, page loads fail. The browser never falls
  back to going out directly. With rotation on, it moves to the next upstream.
- Rotating drops open connections, and pages reconnect. One browser has one
  exit address, so a rotation changes it for every client of that browser.
  `session` rotates only when a Browser API session starts and no other session
  on that browser was used in the last 10 minutes.
- The proxy covers the browser as LiveLLM starts it. A client connected to it
  over CDP can open a context that goes around the proxy, and so can an
  extension with the `proxy` permission.
- Changing the proxy needs a person's go: the API key needs the **proxies**
  permission, which a person turns on on the Keys page. Without it, a plan
  that changes the proxy fails at apply with the platform's message. An update
  that leaves the proxy alone, such as a new `cpu`, still works.

### Settings made in the console

A setting the configuration leaves out (`locale`, `timezone`, `languages`,
`geolocation`, `proxy`) is left to the console: what a person or an agent sets
there is kept, doesn't show in the plan, and an update doesn't touch it. Once
the configuration sets one, Terraform manages it: a change made in the console
shows in the plan, and removing it from the configuration puts the browser
back to its default. To keep a proxy managed in the console, logins
included, use `lifecycle { ignore_changes = [proxy] }`.

### Write-only values

`username_wo`, `password_wo` and `change_ip_url_wo` are never stored in state
or shown in a plan, and the platform never sends them back. They are sent:

- when the browser or the upstream is created;
- when an upstream's `server` changes, because a login doesn't move to another
  host;
- when `auth_version` changes.

Otherwise the stored values are kept. A new value alone makes no plan, so
bump `auth_version` to send it. An upstream block without them removes what
is stored for it, and the plan warns about that.

## Schema

### Required

- `name` (String) Workload id. Changing it replaces the browser.

### Optional

- `engine` (String) `chrome` (the default) or `camoufox`, see
  [Engines](#engines). Changing it replaces the browser.
- `cpu` (String) CPU request, e.g. `1`.
- `memory` (String) Memory request, e.g. `2Gi`.
- `locale` (String) The browser's language and region, e.g. `ru-RU`, used for
  its pages, `navigator.language` and `Intl`. Use current codes (`he-IL`, not
  `iw-IL`). Removing it puts the browser back to its default.
- `timezone` (String) An IANA time zone such as `Europe/Moscow`, or `UTC`.
- `languages` (List of String) The languages pages are asked for
  (`Accept-Language`, `navigator.languages`), in order, 1 to 6. They need
  `locale`, and the first must be `locale`. Left out, it follows `locale`: `ru-RU`
  gives `ru-RU, ru, en-US, en`. Removing it from the configuration keeps the
  current list until `locale` changes.
- `geolocation` (Block) What pages get when they ask for a location. Left out,
  pages are asked as in Chrome.
  - `mode` (String, required in the block) `off` refuses every page; `fixed`
    answers `latitude` and `longitude`.
  - `latitude` (Number) -90 to 90 (`fixed`).
  - `longitude` (Number) -180 to 180 (`fixed`).
  - `accuracy` (Number) Meters, 1 to 10000 (`fixed`, default 100).
- `proxy` (Block) Proxies for the browser's traffic, see [Proxies](#proxies).
  - `upstream` (Block List, at most 20) A proxy to go out through. Rotation
    moves through the list in order.
    - `name` (String, required in the block) Lowercase letters, digits and
      dashes, unique in the list.
    - `server` (String, required in the block) `scheme://host:port` with scheme
      `http`, `https` or `socks5`. No login, path or query, and not this
      machine (`localhost`, a loopback or link-local address).
    - `username_wo`, `password_wo` (String, Sensitive, write-only) The proxy's
      login, one line of at most 255 characters each. Set both or neither.
    - `change_ip_url_wo` (String, Sensitive, write-only) A mobile proxy's
      change-IP address. It is called before the browser rotates to this
      upstream.
    - `change_ip_method` (String) `GET` (default) or `POST`. Only with
      `change_ip_url_wo`.
    - `min_change_ip_seconds` (Number) The shortest time between two change-IP
      calls, 10 to 3600 seconds (default 60). Only with `change_ip_url_wo`.
    - `has_auth`, `has_change_ip` (Boolean, read-only) Whether a login or a
      change-IP address is stored. They are planned from the configuration.
  - `auth_version` (Number) Change it to send the write-only values again.
  - `rotation` (Block) When the browser moves to the next upstream.
    - `mode` (String) `off` (default), `session` or `interval`.
    - `every_minutes` (Number) 1 to 1440, only with `interval`.
    - `order` (String) `sequential` (default) or `random`.
  - `check_url` (String) An http(s) address that answers with the caller's IP,
    as plain text or as JSON with `ip`. It is fetched through the proxy to find
    the exit address. Left out, LiveLLM uses its own. Everyone in the workspace
    sees it, so it can't carry a login, or a token, key, password or signature
    in its query.
- `timeouts` (Block) `create` / `delete` (default 10m each).
- `placement_strategy` (String) Where it runs: omit for automatic (the default; LiveLLM picks the host), `region` for any host in `placement_region`, `host` to pin `placement_host`. Changing it restarts the resource where it now belongs. A resource pinned to a host waits for that host while it is down.
- `placement_region` (String) Region to run in (`placement_strategy = "region"`).
- `placement_host` (String) Host id to pin to (`placement_strategy = "host"`); ids come from the [`livellm_hosts`](../data-sources/hosts.md) data source.

### Read-Only

- `ready` (Boolean) Whether the browser is up.
- `profiles_ready` (Boolean) Whether the browser's profile can be saved,
  restored, exported and imported (from the console, the API or the CLI).
  A browser made before profiles existed turns this on at its next restart.

The exit address and its country change with every rotation, so they are not
attributes. The console, the API (`GET /v1/workloads/{id}/proxy`) and the CLI
show them.

## Import

```shell
terraform import livellm_browser.scraper scraper
```

An import reads every setting the browser holds, so the first plan shows the
ones the configuration lacks. Set `engine = "camoufox"` before importing a
Camoufox browser: left out, the engine is Chrome, and the plan replaces it. The write-only values can't be imported, and neither can `auth_version`, which
the platform doesn't keep. When the configuration sets `auth_version`, the
first plan after an import sets it, and that apply sends the configuration's
values.
