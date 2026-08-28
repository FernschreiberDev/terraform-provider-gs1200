# Changelog

## 0.1.0 (unreleased)

First public release.

### Resources

- `gs1200_vlan` — the existence of one 802.1Q VLAN.
- `gs1200_port` — one port's whole configuration: PVID, tagged and untagged
  membership, admin state, speed and duplex, flow control, ingress and egress
  rate caps.
- `gs1200_system` — the switch itself: name, loop prevention, storm control,
  IGMP snooping, SNMP, panel LEDs, EEE, port isolation.

### Data sources

- `gs1200_switch` — identity, VLAN table, PVIDs and live link state.

### Notes

The provider refuses, unless `force` is set, the changes that cannot be undone
remotely: removing a port from the management VLAN, deleting that VLAN,
switching off a port carrying it, turning loop prevention off, and turning SNMP
off.

Reboot, factory reset, firmware upgrade, IP addressing and the web password are
deliberately not exposed. See the README for why.
