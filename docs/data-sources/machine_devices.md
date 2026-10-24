---
page_title: "livellm_machine_devices Data Source - livellm"
description: |-
  The cluster's devices a machine can be given (a GPU, a USB controller), with how many are free.
---

# livellm_machine_devices (Data Source)

Reads the cluster's devices a machine of the workspace may be given: a GPU, a
USB controller. Each has the `id` that [`livellm_vm`](../resources/vm.md)'s
`devices` takes, how many the cluster has and how many no machine has. Empty
where the cluster offers none to this workspace.

It takes no arguments: the API key already names the workspace.

## Example Usage

```terraform
data "livellm_machine_devices" "this" {}

output "free_devices" {
  # e.g. ["rtx5090"]
  value = [for d in data.livellm_machine_devices.this.devices : d.id if d.free > 0]
}

resource "livellm_vm" "gpu" {
  name      = "gpu-box"
  cpus      = 8
  memory_gi = 32
  # …credentials…

  devices = ["rtx5090"]
}
```

A device is one machine's at a time: giving a machine one that another
machine has is refused ("NVIDIA RTX 5090 is in use by another machine (1 in
the cluster)."). The counts change as machines take and give back devices, so
write `devices` yourself rather than from `free`, or a plan after the apply
would change it.

## Schema

### Read-Only

- `devices` (List of Object) One entry per device:
  - `id` (String) What `livellm_vm`'s `devices` names (`rtx5090`).
  - `label` (String) The device's name (`NVIDIA RTX 5090`).
  - `kind` (String) `gpu` or `usb`.
  - `total` (Number) How many the cluster has.
  - `free` (Number) How many no machine has.
  - `machines` (List of String) This workspace's machines that have it.

Needs a LiveLLM API with devices for machines: on an older one the read fails
("Cannot read the devices").
