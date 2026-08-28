# terraform-provider-gs1200

A Terraform / OpenTofu provider for the **Zyxel GS1200-5 v3** web-managed
switch: VLANs, per-port 802.1Q membership and PVID, link settings, rate limits,
and the switch-wide protections.

The GS1200 has no API. Everything here was derived by reading the firmware's
own JavaScript on a unit running `V1.00(ACPS.2)C0`, then checked against the
hardware — including, where possible, against independent SNMP readings.

> **Scope.** This targets the GS1200-5 v3 specifically. Other GS1200 revisions
> ship different firmware (v1 and v2 have no SNMP agent at all), and the larger
> GS1900 and XGS families speak something else entirely. It may work on a
> GS1200-8 v3, which uses the same pages, but that has not been tested.

## What it manages

| | Read | Write |
|---|---|---|
| 802.1Q VLANs | yes | yes |
| Per port: PVID, tagged and untagged membership | yes | yes |
| Per port: admin state, speed/duplex, flow control | yes | yes |
| Per port: ingress and egress rate caps | yes | yes |
| Device name | yes | yes |
| Loop prevention, storm control | yes | yes |
| IGMP snooping, unknown-multicast drop, static router port | yes | yes |
| SNMP v1/v2c, panel LEDs, EEE (802.3az) | yes | yes |
| Port isolation | yes | yes |
| Identity: model, hardware revision, firmware, MAC, gateway, uptime | yes | — |
| Live link state: presence and negotiated rate | yes | — |
| Management VLAN | yes | — (deliberately) |

## Quick start

```hcl
terraform {
  required_providers {
    gs1200 = {
      source  = "fernschreiberdev/gs1200"
      version = "~> 0.1"
    }
  }
}

provider "gs1200" {
  host     = "192.0.2.10"
  password = var.switch_password
}

resource "gs1200_vlan" "iot" {
  vid = 1003
}

resource "gs1200_port" "port3" {
  port     = 3
  pvid     = 1003
  untagged = [1003]
}
```

One provider instance addresses one switch. For several, use aliases — or a
module per switch, which lets the resources inside drop the `provider`
argument entirely. See [`examples/`](examples/).

## PVID and untagged: two directions of travel

This is the distinction that governs the whole model, and it is easy to
conflate.

**`untagged` / `tagged` — egress.** For a (port, VLAN) pair: when a frame of
that VLAN *leaves* this port, does it keep its 802.1Q tag or lose it?

**`pvid` — ingress.** When a frame arrives on this port *without* a tag, which
VLAN does it belong to? One value per port, necessarily: a bare frame carries
nothing to choose from.

```hcl
# Access port: the attached device knows nothing of VLANs.
resource "gs1200_port" "camera" {
  port     = 5
  pvid     = 1003   # what arrives untagged becomes VLAN 1003
  untagged = [1003] # what leaves in VLAN 1003 leaves bare
}

# Hybrid port: management native, the rest tagged towards the core.
resource "gs1200_port" "uplink" {
  port     = 1
  pvid     = 1
  untagged = [1]
  tagged   = [8, 1003]
}
```

They are nearly always consistent, but they are independent. A `pvid` naming a
VLAN the port does not carry untagged gives asymmetric behaviour: traffic
enters in one place and replies leave in another. The hardware allows it and
reports nothing — this provider refuses it unless `force` is set.

## What the hardware forces on the design

Four traits of this firmware shape everything below them.

**One request at a time.** Not one *session* — one *request*. Four refreshes
sent together do not run in parallel; they queue inside the device, and the
client's clock runs during that wait. So the queue lives on this side, where
waiting is free, behind a lock keyed **by device**: several switches make
progress at once while one switch's requests line up.

**One web session at a time.** While a session is held, nobody else reaches the
web interface — including you. The provider claims one only when it must, and
always releases it, including when an apply is interrupted.

**Port bitmaps are offset by one.** Port 1 is bit 1, not bit 0. Being one out
silently moves every port by one position — on a write, that reassigns live
traffic to the wrong socket. It is the most heavily tested part of the code.

**The CGI endpoints answer 200 whatever they did.** The reply proves nothing,
so every write is read back and compared against what was asked.

Two more oddities have their own handling: the switch **drops idle connections
without warning** (Go re-sends idempotent requests on its own but never a POST,
and the login is a POST), and its **TLS is ancient** — one cipher suite,
`AES128-GCM-SHA256` over an RSA key exchange, which Go 1.22 removed from its
client defaults.

## Safety refusals

Four changes are refused because they are not recoverable remotely. `force =
true` lifts each one.

| Refusal | Why |
|---|---|
| Removing a port from the management VLAN | The switch drops off the network. |
| Deleting the management VLAN | Same, permanently. |
| Switching off a port carrying the management VLAN | Same. |
| Turning loop prevention off | A loop floods the segment, and the flood is what would stop you reaching the switch to undo it. |
| Turning SNMP off | Anything polling the switch goes blind, and a monitoring outage is silent by nature. |

The provider also refuses a `pvid` the port does not carry untagged, a rate the
firmware cannot represent exactly, and creating a VLAN that already exists —
that last one points you at `import` rather than silently adopting live
configuration.

## Resources

- **`gs1200_vlan`** — the existence of one VLAN. Which ports carry it belongs
  to `gs1200_port`; the two never write the same bytes.
- **`gs1200_port`** — one port's whole configuration: `pvid`, `untagged`,
  `tagged`, plus the optional `enabled`, `speed`, `flow_control`,
  `ingress_rate_kbps`, `egress_rate_kbps`.
- **`gs1200_system`** — the switch itself: name, loop prevention, storm
  control, IGMP snooping, SNMP, LEDs, EEE, port isolation.
- **`gs1200_switch`** (data source) — everything the switch will report,
  including live link state, read without claiming the web session.

Full reference under [`docs/`](docs/).

Optional attributes left out of a configuration keep whatever the switch
already has. This provider declines to reset what it was not asked about.

## What it deliberately does not expose

- **Reboot, factory reset, firmware upgrade, config backup/restore.** These are
  acts, not states. A factory reset triggered by a misread plan is not
  recoverable.
- **The switch's IP address and management VLAN.** Changing either severs the
  provider's own connection mid-write. Both are readable.
- **The web password.** A resource that rotates the credential the provider
  authenticates with is a trap for its own author.

Reachable but not yet implemented, every page having been mapped: QoS (802.1p
and port-based), link aggregation, port mirroring, jumbo frames, SNMP
community strings, static MAC entries.

**Cable diagnostics** deserve their own note: it is an active test that briefly
interrupts the link. As a data source, refreshed on every plan, it would cut
the network on every plan.

## Development

```bash
make          # fmt, vet, test, build
```

No test touches hardware. Two things make them meaningful:

- **Captured fixtures.** `internal/zyxel/live_fleet_test.go` holds the exact
  bytes two units served, paired with what an independent SNMP poll reported
  for the same ports at the same moment. Two implementations of an
  undocumented format checking each other; a parser alone can only prove itself
  self-consistent.
- **A stateful emulator.** `internal/fakeswitch` imitates the device closely
  enough to run a real `plan` and `apply` against it: the single session, CGI
  endpoints that answer 200 without applying anything, a refusal to delete a
  VLAN still used as a PVID.

```bash
go run ./cmd/fakeswitch -addr 127.0.0.1:8099 -password secret
```

Builds are reproducible: same source, same bytes. That needed
`-buildvcs=false`, since Go otherwise stamps the current commit into the
binary — the same code built either side of a commit produced different bytes,
and a lock file generated from one rejected the other while blaming the
checksum rather than git.

## Provenance

The protocol was recovered by reading the pages the firmware serves and the
JavaScript it ships to the browser, for the purpose of interoperating with
hardware its owner already has. No firmware code or assets are redistributed
here; the test fixtures hold only the small factual payloads needed to prove
the parsing is right.

Zyxel is a trademark of Zyxel Communications Corp. This project is not
affiliated with, endorsed by, or supported by Zyxel.

## License

[MPL-2.0](LICENSE).
