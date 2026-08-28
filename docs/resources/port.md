---
page_title: "gs1200_port Resource - gs1200"
subcategory: ""
description: |-
  One physical port's whole configuration: 802.1Q membership, link settings and
  rate caps.
---

# gs1200_port (Resource)

The switch stores the opposite arrangement — a table of VLANs, each holding a
bitmap of member ports — so this resource is a view the provider assembles and
writes back.

A write only ever moves **this port's** bit in each VLAN row. That is what lets
every port be its own resource without two of them undoing each other, and what
makes a parallel apply safe.

Every VLAN named here must already exist as a [`gs1200_vlan`](vlan.md). A typo
in a VLAN id fails rather than provisioning one.

## Example usage

```terraform
# Access port: the attached device knows nothing of VLANs.
resource "gs1200_port" "camera" {
  port     = 5
  pvid     = 1003
  untagged = [1003]
}

# Hybrid port: management native, the rest tagged towards the core.
resource "gs1200_port" "uplink" {
  port     = 1
  pvid     = 1
  untagged = [1]
  tagged   = [8, 1003]
}

# Link settings and rate caps are optional; leaving one out keeps whatever the
# switch already has.
resource "gs1200_port" "guest" {
  port              = 4
  pvid              = 1003
  untagged          = [1003]
  speed             = "100-full"
  ingress_rate_kbps = 10240
  egress_rate_kbps  = 2048
}
```

## PVID and untagged

**`pvid` is ingress.** The VLAN a frame arriving *without* a tag is placed
into. One value per port — an untagged frame carries nothing to choose from.

**`untagged` and `tagged` are egress.** Whether a frame of that VLAN leaves
this port stripped of its tag or still carrying it.

`pvid` must appear in `untagged`. A PVID naming a VLAN the port does not carry
untagged means frames enter in that VLAN and cannot leave the same way; the
hardware permits it and reports nothing, so the provider refuses it unless
`force` is set.

## Schema

### Required

- `port` (Number) 1-based port number, as printed on the switch. Changing it
  replaces the resource.
- `pvid` (Number) Ingress VLAN, 1-4094.

### Optional

- `untagged` (Set of Number) VLAN ids whose frames leave this port with the tag
  stripped.
- `tagged` (Set of Number) VLAN ids whose frames leave this port still carrying
  their tag.
- `enabled` (Boolean) Whether the port is switched on at all.
- `speed` (String) `auto` (the default on this hardware), `1000-full`,
  `100-auto`, `100-full`, `10-auto` or `10-full`. Forcing a rate the other end
  does not agree to leaves the link down.
- `flow_control` (Boolean) 802.3x pause frames.
- `ingress_rate_kbps` (Number) Cap on traffic entering the port, `0` for none.
  The firmware stores rates in steps of 32 kbps, so a figure it cannot
  represent exactly is refused rather than quietly rounded. Range 32-1000000.
- `egress_rate_kbps` (Number) Cap on traffic leaving the port. Same grid.
- `force` (Boolean) Bypass the safety checks. Default `false`.

`enabled`, `speed`, `flow_control` and the two rate caps are optional **and**
computed: leaving one out keeps whatever the switch already has, rather than
resetting it.

## Refusals

Switching off a port that carries the management VLAN takes the switch off the
network, and recovering needs physical access. Refused unless `force` is set.

## Destroying

**Destroying this resource does not change the switch.** A port always has some
configuration; there is no "unconfigured" state to return it to, and picking
one during a destroy would move traffic nobody asked to move. Terraform simply
stops tracking the port and says so.

## Import

Import is by port number.

```shell
terraform import gs1200_port.uplink 1
```
