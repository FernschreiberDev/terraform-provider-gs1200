---
page_title: "gs1200_switch Data Source - gs1200"
subcategory: ""
description: |-
  Everything the switch will report about itself and its ports.
---

# gs1200_switch (Data Source)

Useful for discovering what is on a switch before writing resources for it, and
for asserting invariants with `check` blocks.

## Example usage

```terraform
data "gs1200_switch" "this" {}

output "ports_down" {
  value = [for l in data.gs1200_switch.this.links : l.port if !l.up]
}
```

## Schema

### Read-Only

- `host`, `name`, `model`, `hardware`, `firmware`, `mac`, `gateway`, `netmask`
  (String)
- `uptime_seconds` (Number)
- `port_count` (Number)
- `vlan_enabled` (Boolean) Whether 802.1Q mode is switched on at all.
- `management_vlan` (Number) Zero when unknown.
- `partial` (Boolean) True when no password is configured, in which case only
  the unauthenticated VLAN table could be read and `pvids` is empty.
- `vlans` (List of Object) `vid`, `name`, `index`, `tagged`, `untagged`.
- `pvids` (Map of Number) Port number, as a string key, to its native VLAN.
- `links` (List of Object) `port`, `up`, `speed_mb` — live electrical state,
  read without a session.

## A note on what is not decoded

`portStatus.xml` serves four groups of per-port values. The first two — link
state and negotiated rate — were checked against independent SNMP readings on
every port of two units and are exposed as `links`. The remaining two are
**not** decoded, because a guess in a data source becomes a fact people rely
on.
