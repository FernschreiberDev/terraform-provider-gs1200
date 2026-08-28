---
page_title: "gs1200_system Resource - gs1200"
subcategory: ""
description: |-
  The switch itself: name, and the protections that guard the whole device.
---

# gs1200_system (Resource)

There is one of these per switch, so it takes no identifier. An attribute left
out keeps whatever the switch already has — this resource declines to reset
what it was not asked about.

## Example usage

```terraform
resource "gs1200_system" "this" {
  name            = "rack-a"
  loop_prevention = true

  # Pinned rather than left to drift: a monitoring system may be reading this
  # switch's port counters over SNMP, and IGMP snooping keeps multicast off
  # every port.
  snmp          = true
  igmp_snooping = true
}
```

## Schema

### Optional

- `name` (String) Device name. The firmware accepts 1 to 14 characters —
  letters, digits, underscore or hyphen — and its own page refuses anything
  else, as does the provider, before touching the switch.
- `loop_prevention` (Boolean) Detects a cable plugged back into the same switch
  and shuts the loop down.
- `storm_control` (Boolean) Caps broadcast, multicast and unknown-unicast
  floods.
- `storm_control_pps` (Number) The cap in packets per second, 1-500000. Only
  meaningful while `storm_control` is on.
- `igmp_snooping` (Boolean) Learn which ports asked for which multicast groups
  and send each group only there, instead of flooding every port.
- `igmp_unknown_multicast_drop` (Boolean) Discard multicast nobody subscribed
  to. Only meaningful while `igmp_snooping` is on.
- `igmp_static_router_port` (Number) Port where the multicast router sits, `0`
  to discover it automatically.
- `snmp` (Boolean) SNMP v1 and v2c, which the firmware switches together.
- `led` (Boolean) The firmware's own Disable/Enable control for the panel
  lights.
- `energy_efficient_ethernet` (Boolean) 802.3az. Applied to every port at once
  on this hardware, and the switch takes about ten seconds to settle.
- `port_isolation_uplink` (Number) `0` lets every port talk to every other. Any
  other value names the single port the others may reach — a guest-network
  arrangement where devices see the uplink and nothing else.
- `force` (Boolean) Bypass the refusals below. Default `false`.

### Read-Only

- `management_vlan` (Number) The VLAN carrying the switch's own traffic.
  Read-only on purpose: changing it is how a switch is lost, and the provider
  would sever its own connection halfway through its own write.
- `model`, `hardware`, `firmware`, `mac` (String) As the switch reports them.

## Refusals

**Turning `loop_prevention` off** is refused unless `force` is set. A loop
floods the segment, and the flood is what would stop you reaching the switch to
undo it.

**Turning `snmp` off** is refused too: anything polling the switch goes blind,
and what stops working is a dashboard nobody is watching at the moment of the
apply.

## Destroying

Destroying this resource does not change the switch. A switch always has a name
and some protection setting; there is no unconfigured state to return it to.
