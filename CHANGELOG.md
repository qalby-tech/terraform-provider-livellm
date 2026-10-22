# Changelog

## Unreleased

- **Dedicated cores.** `livellm_vm` takes `dedicated_cores = true`: every
  core reserved for the machine on its server, instead of shared (the
  default), which may be slower when the server is busy. On servers whose
  cores run at different clock speeds the cores are all of one speed:
  `core_speed` asks for one, in MHz (`5700` for 5.7 GHz; left out or `0`, any
  one speed), and `mixed_speeds = true` keeps the count with cores that may
  run at different speeds. A change reaches a running machine at its next
  restart.
- The three are optional and computed, as the sizes are: left out, a machine
  keeps what it has, the console's choice included, and a new one is shared;
  `false` and `0` clear. `dedicated_cores = false` clears `core_speed` and
  `mixed_speeds`, and setting either on shared cores is an error at plan
  time. A refresh and an import read all three.
- **Cores and room refused in the platform's words.** A create or an update
  the platform refuses says what it said and what to write: too few cores at
  the speed asked for ("Only 6 cores at 5.7 GHz are available. Continue with
  mixed core speeds?") or no single speed with the cores ("Mixed core speeds:
  this machine's cores run at different clock speeds."): `cpus` = the most
  that fit, or `mixed_speeds = true`; a speed no server has ("No server this
  machine may run on has cores at 3 GHz."): another `core_speed`, or `0`; no
  room ("This server is full: it has no room for this machine." / "No server
  has room for this machine.", the platform naming the kind: any resource,
  machine, app, database, object storage, browser, Browser API or Desktop
  App, that is new, started, grown or moved): stop or shrink another
  resource, or ask for less.
- `running_core_speed` and `running_mixed_speeds` (computed, on `livellm_vm`
  and on the `livellm_vm` and `livellm_vms` data sources): the clock in MHz of
  the running machine's cores when they are all of one speed, or that they
  run at different clock speeds; null while it isn't running and where the
  servers list no speeds.
- `cpus` is described in cores (a core is one host thread).
- Needs a LiveLLM API with dedicated cores. An older one ignores the three
  settings: the apply warns ("Cores not kept as written") and the cores stay
  shared; nothing changes for a machine that doesn't ask for them.

## 0.18.0

- **Object storage of 3 servers.** `livellm_storage` with `engine = "s3"`
  takes `instances = 3`: 3 servers, one on each of 3 hosts, that keep reading
  and writing when one host is lost. It needs 3 hosts the workspace can use
  (on fewer the platform refuses the apply in plain words and nothing is
  made), can't be pinned to one host (`placement_strategy = "host"` is refused
  at plan time), and counts 3 times its cpu and memory and 1.5 times its
  `disk_gi` toward the plan. `instances = 1`, or leaving it out, is one server
  as before. Needs LiveLLM API 0.72 or newer; an older platform refuses
  `instances = 3` on object storage.
- **Servers are fixed when a store is made.** Changing `instances` on an
  existing object storage, 1 to 3 or 3 to 1, is an error at plan time ("An
  object storage's servers can't be changed"), never a replacement, which
  would delete the files: make a new one under another name and copy the
  files over.
- `servers_desired` and `servers_ready` (computed) say how many servers an
  object storage of 3 servers runs and how many are ready; null for anything
  else.
- `instances` is now optional and computed on every engine, as the sizes are:
  left out it reads back what the database has (`1` when the platform holds
  none), so a refresh fills it in once with no change on the platform, and a
  configuration that drops `instances = 3` keeps 3 instead of planning it
  away. Write `instances = 1` to bring a Postgres database back to one.

## 0.17.0

- **Sizes the platform chose read back.** `cpus`, `memory_gi` and `disk_gi`
  on `livellm_vm`, `cpu` and `memory` on `livellm_browser`,
  `livellm_browser_api` and `livellm_container_app`, and `cpu`, `memory` and
  `storage_gi` on `livellm_desktop_app` are now optional and computed, as
  `livellm_storage`'s sizes already were. A resource that leaves a size out
  reads back the default the platform gave it (the platform now writes its
  kind's default onto a new resource that names no size), so the next plan
  shows no change; removing a size from the configuration later keeps the
  size the resource has instead of planning it away. A refresh and an import
  read every size the platform holds. A configured size is sent and kept as
  written.
- A size in GiB the platform holds in a unit that isn't whole
  GiB (`512Mi`) no longer reads as that number of GiB: whole `Mi` and `Ti`
  sizes are converted, anything else keeps what state had.
- On a platform that doesn't write defaults yet, a size left out stays unset
  as before: nothing changes for those resources.

## 0.16.0

- **Pay as you go.** A create or an update refused for billing now says what
  to do, by the platform's code: pay as you go off (`payg_off`: turn it on, or
  choose a bigger plan, on the console's Billing page; Free includes no
  resources), past its monthly limit (`payg_limit`: a person raises it there;
  an API key can only lower it), a card missing (`card_required`: a person
  adds one there), a payment overdue (`payment_overdue`: a person pays the
  open invoice there; what runs keeps running) and pay as you go not yet open
  to the account (`payg_not_offered`, a 403 that is no longer read as a
  missing permission). The diagnostic shows the platform's words, never the
  link that pays an invoice. Any other full plan still says to raise it, now
  "or turn on pay as you go" instead of "enable metered billing".
- **Organizations.** A create or an update refused because an organization's
  workspace has used its share of the organization's plan now says so: an
  owner of the organization can give it more under Organization → Billing,
  or free resources first. On a workspace of your own the error still says to
  raise the plan.
- `livellm_workspace`'s `owner` is described as the id of the person who
  created the workspace. The value is unchanged; on an organization's
  workspace it names who made it, not everyone who owns it.

## 0.15.0

- **Object storage.** `livellm_storage` takes `engine = "s3"`: an S3 server
  of the workspace's own on its own disk (`disk_gi`, which grows), with a
  bucket `app` made with it. `username` is its access key (generated when
  left out) and `password_wo` its secret key; a new secret key
  (`password_wo_version`) restarts it for a few seconds. `expose` gives it an
  HTTPS S3 address (path-style) and `allowlist` covers that address and its
  admin console's. `endpoints` lists `s3` and, when exposed, `s3-external`.
  It keeps one copy of your files and has no backups: a `backup` block,
  `instances` other than `1`, a `version` other than `"1"` and a secret key
  with a space, tab or line break at either end are refused at plan time, and
  an update says nothing about backups for it. An import reads its version
  back as the platform holds it (`"1"`, or nothing for one made through the
  API without a version). A plan that removes its allowlist while its console
  stays on warns: the console's address also answers S3 requests. So does
  every plan while its console is on with `admin_console` left out (turned
  on in the dashboard, for one) and no `allowlist` or `expose`.
- **Added** `admin_console` to `livellm_storage`, for every engine: pgAdmin,
  Redis Commander, or the RustFS console for object storage. Left out, the
  console keeps the state it has, so an apply no longer turns off a console
  switched on in the dashboard; a refresh and an import read it back. Turning
  it on sends `password_wo` in the same apply, since the platform now needs
  the password to turn a console on; a plan that does so without a new
  `password_wo_version` warns, since a `password_wo` other than the current
  password changes it. pgAdmin and Redis Commander answer from
  any network (signed in with `admin` and the database password); `allowlist`
  doesn't cover them.
- `livellm_container_app`'s `database` block takes an object storage's
  details: `endpoint`, `host`, `port`, `region`, `bucket`, `accessKey` and
  `secretKey` (read from its stored keys, never in state). The plan accepts
  every engine's details; the apply refuses one the linked engine doesn't
  give.
- `livellm_vm` and `livellm_desktop_app` `database` blocks link an object
  storage like a database (reach only).
- Needs a platform that knows object storage for `engine = "s3"`; an older
  one refuses it at apply.

## 0.14.0

- **Added** `reachable_from` to `livellm_vm`, `livellm_container_app`,
  `livellm_browser`, `livellm_browser_api` and `livellm_desktop_app`: which
  other resources of the workspace may connect to this one. Names, `["*"]`
  for the whole workspace (also resources made later; `"*"` goes alone), or
  `[]` for none. A service's name or its `stack` stands for every service of
  that app.
- **Resources are closed inside the workspace.** A resource created with
  `reachable_from` left out is reached only by its own parts (the services of
  one `stack` reach each other), the apps that wait for it (`starts_after`),
  and, for a browser, the Browser API that holds it (a new service of an app
  that is already there takes that app's value instead). Write
  `reachable_from` on whatever other resources must connect to it. Resources
  made before are closed too when the setting arrives (`[]`), and a refresh
  reads it: open what must stay reachable with `reachable_from` or a
  `database` block.
- **A database is reached only by what links it.** `livellm_storage` has no
  `reachable_from`: an app's `database` block or `starts_after`, or a
  `database` block on a machine or a Desktop App, is what lets a resource
  reach it.
- **Reach-only database links.** `env` in `livellm_container_app`'s
  `database` block is optional (0 to 12 variables). Without variables the
  link only lets the app, with every service of its stack, reach the
  database: nothing is added to its environment and it doesn't wait for the
  database, so adding or removing such a block never restarts the app. A link
  with variables works as before, waiting included. For start order without
  variables use `starts_after`.
- **Added** a `database { name }` block to `livellm_vm` and
  `livellm_desktop_app` (at most 8, each database once): the machine or
  Desktop App may reach that database. Reach only: nothing is put into it and
  nothing restarts. A database can't be deleted while a machine or Desktop
  App links it. Refresh and import read the links back, and an apply warns
  when the platform didn't keep them.
- Left out later, `reachable_from` keeps the value the resource has: an update
  sends nothing about it, and removing the attribute from a configuration
  doesn't change it. `reachable_from = []` closes the resource. Refresh and
  import read it back, so a change made in the console shows in the plan while
  the configuration sets it.
- The services of one `stack` share one value: a change on one service
  changes it on all. Set it on one service and leave it out of the others, or
  write the same value on each; two different values make every apply undo
  the other one.
- Refused at plan, as the platform refuses them at apply: `"*"` next to
  another name, a name twice, more than 64 names, a name that isn't a resource
  name, the resource itself, and a service's own `stack`. A name that is
  another service of the same stack, or no resource at all, is refused at
  apply (the plan can't see other resources).
- Linking resources a key made itself (`reachable_from`, `database`,
  `starts_after`, a shared `stack`) needs nothing more: the key made both
  ends. Any other link, whichever end a person (or another key) made, needs a
  key with the **Network** permission, unless the one reached already lets
  the whole workspace in (`reachable_from = ["*"]`; a database never does, so
  a `database` block needs it unless the key made both the database and what
  links it); setting `["*"]` always needs it. Putting a service into a
  `stack` is an opening too, unless the key made that service and every
  service already in the stack. Putting a browser into a Browser API that others may reach, or
  turning on `all_browsers`, is judged the same way (a new browser joining a
  Browser API whose `all_browsers` was already on needs nothing). A
  person turns Network on for the key on the API keys page; without it the
  apply fails with the platform's words and how to get the permission.
  Narrowing or closing never needs it. Keys and their permissions aren't
  managed by this provider.
- `allow_cidrs`, `allowlist` and public ports open nothing inside the
  workspace, and an `internal` port answers only the resources
  `reachable_from` lets in; their descriptions now say so.
- A platform without the setting reads `reachable_from` as null, and an apply
  that sets it warns that it wasn't kept. A platform without reach-only links
  refuses an app's `database` block without variables at apply with its own
  words. It doesn't keep a `database` block on a machine or a Desktop App:
  the apply succeeds with a warning that the links weren't kept, the machine
  can't reach those databases, and the next plan shows the blocks to add
  again.
- A replacement is a new resource: left out of the configuration,
  `reachable_from` starts closed, and the platform drops the old resource's
  name from the other resources, which then take a second apply (or, where
  their configuration leaves `reachable_from` out, a new value) to let the new
  one in. The plan warns when a replacement changes who reaches what; see
  "Replacing a resource" in the docs.
- An apply whose `reachable_from` holds a name known only at apply stores the
  names sent, never an unknown value.

## 0.13.1

- Changing a browser's `proxy` no longer needs a permission on the API key
  once the platform has dropped the 0.12.0 **proxies** permission. An older
  platform still refuses the change without it: that refusal is shown in the
  platform's words and says it comes from an older platform.
- Before you change a browser's proxy, ask the user and wait for their
  agreement: it changes where the browser's traffic goes and the address sites
  see.

## 0.13.0

- **Added** `engine` to `livellm_browser`: `chrome` (the default) or
  `camoufox`, a Firefox-based browser for sites that turn a Chrome browser
  away. A Camoufox browser is driven with Playwright 1.62 (`firefox.connect`),
  not over CDP, and takes no extensions.
- The engine is fixed at creation. Changing it, or removing
  `engine = "camoufox"`, plans a replace; the new browser starts with an empty
  profile. Refresh and import read the engine back. Set
  `engine = "camoufox"` before importing a Camoufox browser.
- `livellm_browser_api` has no engine: one Browser API can hold Chrome and
  Camoufox browsers together, `all_browsers` means every browser in the
  workspace, and remote browsers are Chrome. A session can ask for an engine
  when it starts (`POST /start_session` with `{"engine":"camoufox"}`). An
  `engine` argument on `livellm_browser_api` fails at plan as an unexpected
  argument.
- Configurations without `engine` send exactly what 0.12.0 sent, and a state
  0.12.0 wrote plans clean, with or without a refresh.
- A platform that doesn't offer Camoufox refuses the create with its message.
  A platform too old to know engines makes a Chrome browser instead: the
  apply fails and the state holds `chrome`, never `camoufox`.
- `engine = "camoufox"` over a state 0.12.0 wrote (no engine in it) is an
  update, never a replace: the platform keeps a Camoufox browser as it is and
  refuses to turn a Chrome one into Camoufox.

## 0.12.0

- **Added** `locale`, `timezone` and `languages` to `livellm_browser`, along
  with a `geolocation` block (`mode` `off` or `fixed`, with `latitude`,
  `longitude` and `accuracy`). A browser can now speak a language and live in a
  time zone of its own. Left out, `languages` follows `locale`. Changing any of
  them restarts the browser and keeps its profile. Removing one puts the
  browser back to its default.
- **Added** a `proxy` block to `livellm_browser`:
  - `upstream` blocks (`name`, `server` with `http`, `https` or `socks5`, and
    `change_ip_method` / `min_change_ip_seconds` for mobile proxies);
  - `rotation` (`off`, `session` or `interval`, `every_minutes`, `order`);
  - `check_url`.

  Logins and change-IP addresses are write-only (`username_wo`, `password_wo`,
  `change_ip_url_wo`) and never reach state or a plan. They are sent when the
  browser or an upstream is created, when an upstream's server changes, and
  when `auth_version` changes; otherwise the stored ones are kept.
  `has_auth` / `has_change_ip` show what is stored. Adding the block restarts
  the browser once, later changes don't, and removing the block restarts it.
  Changing a proxy needs an API key with the **proxies** permission, which a
  person turns on on the Keys page. Without it the apply fails with the
  platform's message, while updates that leave the proxy alone still work.
- **Added** `profiles_ready` to `livellm_browser`: whether the browser's
  profile can be saved, restored, exported and imported.
- Configurations without the new arguments send exactly what 0.11.0 sent.
  Refresh reads a newer setting back only while the configuration sets it, so
  a locale, time zone, geolocation or proxy set in the console for a browser
  whose configuration leaves it out is kept and doesn't show in the plan. An
  import reads them all. `lifecycle { ignore_changes = [proxy] }` keeps a
  proxy and its stored logins as the console has them.
- `change_ip_method` and `min_change_ip_seconds` need `change_ip_url_wo`, and
  `locale` / `languages` refuse older language codes (`iw-IL`; use `he-IL`):
  both would plan again after every apply.
- Refused at plan, as the platform refuses them at apply: `languages` without
  `locale`; a `check_url` with a login, or a token, key, password or signature
  in its query (everyone in the workspace sees it; the address isn't repeated
  in the error); an upstream `server` on this machine (`localhost`, loopback
  or link-local); a `username_wo` / `password_wo` longer than 255 characters or
  on more than one line (the value isn't repeated).

## 0.11.0

- **Added** where it runs to `livellm_container_app`, `livellm_storage`,
  `livellm_browser`, `livellm_browser_api` and `livellm_desktop_app`, the
  same three attributes `livellm_vm` has: `placement_strategy` (`auto`,
  `region` or `host`), `placement_region` and `placement_host`. Left out, the
  platform picks the host, as before. A change is an update in place: the
  resource restarts where it now belongs. Refresh and import read it back, so
  a location changed in the console shows in the plan. A resource pinned to a
  host waits for that host while it is down; with `host`, every copy of a
  database runs on that host; an app with volumes and a location stops before
  its new copy starts, so each change briefly takes it offline.
- **Added** the `livellm_hosts` data source: the hosts resources can run on
  (`id`, `region`, `zone`, `ready`, `schedulable`), for `placement_host` and
  `placement_region`. A new or changed placement is accepted only on a host
  that is `ready` and `schedulable`. A host without a region or zone has them
  null.
- **Changed** `placement_strategy = "host"` without a `placement_host` (or
  `"region"` without a `placement_region`) is now refused at plan time, on
  every resource including `livellm_vm`, instead of failing at apply.

## 0.10.0 (breaking)

- **Fixed** a wait that timed out while the platform could not be read (an
  address that stopped answering mid-apply) reporting "no status reported
  yet": the error now says the workspace's status could not be read and why,
  after the last status it saw. A resource missing from the status says so.
- **Fixed** an apply that changed a `livellm_desktop_app` (or any resource
  that waits for ready: `livellm_vm`, `livellm_container_app`,
  `livellm_storage`, `livellm_browser`, `livellm_browser_api`) finishing on
  the old version: right after the change the old desktop still answered, so
  the wait ended at once and the refresh that followed stored `ready = false`
  while the new one started. The platform now says a change is still rolling
  out (`updating`), and the wait lasts until the new version is ready.
- **Removed (breaking)** `replicas` and `desktops_ready` from
  `livellm_desktop_app`: a Desktop App is one desktop. Make one resource for
  each desktop (`for_each` over their names); `ready` says whether it is up.
  A state written by 0.9.0 reads as before, without the two attributes; a
  configuration that still sets `replicas` fails to plan until the line is
  removed.
- **Fixed** `livellm_storage` without `username`: the platform reports its
  default login name, `app`, and every later plan replaced the database. A
  `username` left out now plans as the name the database has — for a state
  written by an earlier version too, and after an import — and state reads the
  name back (a new database shows `app`). Only changing the name in the
  configuration replaces the database.
- **Fixed** a delete that the platform finished but answered too late for the
  connection (it clears away what belonged to the resource first): the
  provider looks at the workspace, and a resource gone from it is deleted
  rather than an error.
- **Added** `database` blocks to `livellm_container_app` (at most 8): each
  links a managed database (`name`, e.g. `livellm_storage.db.name`) and maps
  environment variables to its connection details in `env` — `host`, `port`,
  `database`, `username`, `password` or `url` (a Redis database gives `host`,
  `port`, `url` and `password`). The password and the URL are read from the
  database's own login, so nothing secret reaches the app's settings or state.
  The app waits for its databases before it starts. The plan checks what the
  platform checks: 1 to 12 variables per block, variable names, the details,
  each database once, and a name used once across `env`, `secret_env` and the
  blocks. Links are read back on refresh and on import.
- `livellm_vm`: a Windows machine takes `ssh_keys` like a Linux one. The keys
  open its SSH (PowerShell) for `username`, next to the workspace's own; the
  plan-time refusal is gone.

## 0.9.0 (breaking)

- **Removed** `backup_schedule` and `backup_keep` from `livellm_storage`,
  deprecated since 0.8.0; the platform no longer takes a database backup
  schedule. Use `backup { mode, keep_days }` instead (`backup_keep = N` is
  `keep_days = N`).
- **Removed** reading an app's disk from before volumes existed as the volume
  `data`: the platform reports every app's disks as volumes.

## 0.8.0

- **Added** Windows to `livellm_vm`: `os = "windows"` with `windows_edition`
  (`desktop`, Windows 11 Pro, the default; or `server`, Windows Server 2025
  Server Core). Windows installs itself on first start, so an apply waits up
  to 45 minutes unless `timeouts` says otherwise. Plan-time checks match the
  platform: a disk of at least 64 GiB, no `Administrator` username, no
  `desktop` and no `ssh_keys`; `windows_edition` without Windows is refused.
  Import reads Windows machines as `os = "windows"`.
- **Fixed** `livellm_vm` import reads the login's username, so the next plan
  no longer replaces an imported machine.
- **Added** the `livellm_desktop_app` resource: Linux desktops in containers
  that start in seconds (`replicas` 1..20, `image`, `cpu`, `memory`,
  `resolution`, `keep_files` with `storage_gi`, `stopped`), with `ready` and
  `desktops_ready`. An apply that adds desktops waits until every one
  answers. Import reads every setting.

- **Fixed** `livellm_storage` backups were never taken: `backup_schedule`
  sent a schedule without turning backups on, so the database had none and
  no error said so. It now turns them on.
- **Added** `livellm_storage` `backup { mode, keep_days }`. `mode` is `daily`
  (a full copy each night, the default), `continuous` (the nightly copy plus
  every change, so the database can be restored to any minute) or `manual`
  (nothing scheduled). `keep_days` is how many days backups are kept (10 by
  default). With the block backups are on, without it off, and a plan shows
  backups turned on or off in the console as a change. A block that leaves
  `mode` or `keep_days` out means `daily` and 10 days, so a mode or keep
  changed in the console is put back by the next apply.
- **Deprecated** `backup_schedule` and `backup_keep`. They still work: any
  schedule turns backups on (daily on a new database; a database switched to
  continuous in the console stays continuous), and `backup_keep` is the same number of days
  as `keep_days` (it always was days, not a number of backups; its
  description said otherwise). They can't be combined with `backup`.
- **Added** plan-time checks on `livellm_storage`: `instances` is 1 or 3,
  three instances and backups are Postgres only (Redis runs as one instance
  and has no backups).
- **Changed** `livellm_storage` `cpu`, `memory` and `disk_gi` read back the
  sizes the platform gives a database when they are left out (1, 1Gi, 5 GiB),
  and later changes send them back unchanged instead of dropping them. A
  database saved without sizes keeps having none: it no longer plans a change
  on every run, and is never given CPU or memory it didn't have.
- **Added** `livellm_vm` `backup { schedule, keep }`: scheduled backups of the
  disk, keeping the last `keep` (1..100 — a count, unlike a database's days).
  A schedule set in the console now shows in the plan; before, the next apply
  removed it without saying so.
- **Added** the `livellm_browser_api` resource: one address that drives
  several browsers. Name them in `browsers`, or drive every browser in the
  workspace with `all_browsers = true`; `remote_browser` blocks add browsers
  running somewhere else, with a write-only `auth_wo` header. A call that
  names no browser goes to the one with the fewest open tabs, a session stays
  on its browser, and `/browsers/<name>/…` or `X-Browser-Id` picks one.
  Import reads the browsers and the remote addresses. `remote_browser.has_auth`
  says whether a header is stored; it is planned from the configuration, so a
  header set or removed in the console shows as a change, and a plan that
  removes a stored header warns by remote id.
- **Added** to `livellm_container_app`:
  - `port { tcp = true }` and `port { udp = true }` — a raw port with a public
    `host:port` address instead of an HTTPS hostname, for anything that isn't
    HTTP (a game server, a VPN, DNS). The address is in `endpoints`.
  - `port { allow_cidrs = [...] }` — the source addresses allowed to reach a
    port. Unset, a port is open to anyone; a raw port takes no password, so
    this is its only protection.
  - `volume { name, size_gi, mount_path }` blocks, up to 8 — disks that keep
    their data across restarts, redeploys and stops. A volume grows in place;
    shrinking one is refused at plan time, and a plan that removes one warns
    by name, because removing it deletes its data. An app that had one disk
    before volumes existed has it as the volume `data`, and import reads it.
  - `stopped` — stop an app without deleting it: it runs nothing, keeps its
    volumes and is billed for their disk only. `false` starts it again.
  - Plan-time checks for all of these, matching the platform: a port is `tcp`
    or `udp`, not both and not `internal`; an internal port takes no
    `allow_cidrs`; `allow_cidrs` are real CIDRs; volume names are unique
    lowercase labels of at most 15 characters; mount paths are absolute and
    written plainly (letters, digits and `. _ @ + -`, at most 200 characters,
    no trailing slash), unique, not `/`, not in `/proc`, `/sys` or `/dev`, and
    not inside one another; the same number is not a tcp (or udp) port of
    one app twice.
  - Removing every `volume` block removes the app's volumes (the platform
    keeps them on a save that leaves them out, so the provider says so).
- **Fixed** `livellm_container_app` reads its ports back: a port changed
  outside Terraform (an allow-list lifted in the console) shows in the plan,
  and an import fills the `port` blocks.
- **Added** `udp` to every resource's and data source's `endpoints`: `true`
  for a raw UDP port, next to `tcp` for a raw TCP one.

## 0.7.0

- **Added** `os` to `livellm_vm`: `debian` (13) or `fedora` (44) servers next to
  Ubuntu. Changing it replaces the machine.

## 0.6.0

- **Added** to `livellm_container_app`: `stack`, `hostname` and `starts_after`
  — an app made of several services, which reach each other by hostname —
  and `port { internal = true }` for a port with no public address. Until now
  a service created in the console with these settings lost them on the next
  apply.
- **Added** `port { internal = true }` to `livellm_vm`: a machine port
  reachable from inside the workspace only, with no node port.

## 0.5.0 (breaking)

- **Removed** everything that managed AI features, which the platform no
  longer has: the `livellm_agent_master` and `livellm_ai_provider` resources,
  `ai_daemon` and `state_repo` on `livellm_vm`, and `ai_agent` on
  `livellm_browser`. Take them out of your configuration before upgrading.
- **Added** to `livellm_vm`:
  - `ssh_keys` — SSH public keys for that machine alone, installed next to the
    workspace's own. A change reaches a running machine within a minute or two.
    The platform keeps a machine's last keys, so an empty list is refused at
    plan time; replace a key, or leave `ssh_keys` out to stop managing it.
  - `stop_after` — have the platform stop the machine after a while (`"4h"`),
    keeping its disk. The clock starts when Terraform creates the machine,
    starts it again, or when the value changes — never on an apply that edits
    something else.
  - `expires_at` (read-only) — when the machine will be stopped.
- **Changed** the provider sends its changes to the workspace one at a time.
  Terraform applies up to ten resources at once, and they all belong to one
  workspace; sent together they queued behind each other on the platform and
  could come back as conflicts. Waiting for a resource to come up still
  happens in parallel.
- **Fixed** `livellm_container_app`: an app with no `image_auth` block (or no
  `source` block) was refused with "Missing Configuration for Required
  Attribute". What each block needs is now checked only when the block is
  there, at plan time, together with the image / source / image_auth rules
  that used to fail only at apply.

- **Changed** the provider calls the platform's shorter API paths
  (`/v1/workspace`, `/v1/status`, `/v1/workloads/…`), which never name the
  workspace: the API key already belongs to exactly one.
- **Removed** `livellm_secret`. The workspace secret store is gone; give an
  app its secret values directly with the new `secret_env` map on
  `livellm_container_app`.
- **Changed** `livellm_container_app`:
  - `source_repo` is replaced by a `source { git { url, ref, dockerfile,
    context } token }` block. Apps are built from any HTTPS Git repo with a
    Dockerfile; the platform no longer watches the repo — rebuild from the
    dashboard or the API.
  - `secret_env` is now a sensitive map of `NAME = value` (was a block list of
    `name` + workspace secret `path`).
  - **Added** an `image_auth { username, password }` block to pull private
    images. The password is sensitive and write-only on the platform; it is
    not allowed together with `source`.

## 0.4.0 and earlier

See the Git tags.
