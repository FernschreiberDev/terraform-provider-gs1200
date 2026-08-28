---
page_title: "gs1200_vlan Resource - gs1200"
subcategory: ""
description: |-
  The existence of one 802.1Q VLAN.
---

# gs1200_vlan (Resource)

Declares that a VLAN exists, and nothing else. Which ports carry it, tagged or
untagged, belongs to [`gs1200_port`](port.md) — the two never write the same
bytes, so they cannot fight over them. A VLAN created here starts with no
members.

## Example usage

```terraform
resource "gs1200_vlan" "iot" {
  vid = 1003
}

# The management VLAN carries the switch's own traffic. Deleting it would put
# the switch off the network; prevent_destroy fails at plan time, before any
# attempt.
resource "gs1200_vlan" "mgmt" {
  vid = 1

  lifecycle {
    prevent_destroy = true
  }
}
```

## Schema

### Required

- `vid` (Number) VLAN id, 1-4094. Changing it destroys and recreates the VLAN.

### Optional

- `force` (Boolean) Bypass the safety check that refuses to delete the switch's
  management VLAN. Default `false`.

### Read-Only

- `index` (Number) The vendor table slot holding this VLAN. The firmware
  addresses VLANs by slot rather than by id; exposed because it is what appears
  in the switch's own web interface.

## Refusals

Creating a VLAN that already exists on the switch is refused rather than
silently adopted — creating it here would take ownership of live configuration
nobody declared, so a later destroy would remove something this run never
created. Import it instead:

```shell
terraform import gs1200_vlan.mgmt 1
```

Deleting a VLAN still named as some port's PVID is refused too, with the ports
listed. `force` does not help there: move those ports first.

## Import

Import is by VLAN id.

```shell
terraform import gs1200_vlan.iot 1003
```
