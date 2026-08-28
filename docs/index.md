---
page_title: "gs1200 Provider"
description: |-
  Manage a Zyxel GS1200-5 v3 web-managed switch: VLANs, per-port 802.1Q
  configuration, link settings and the switch-wide protections.
---

# gs1200 Provider

Manages a **Zyxel GS1200-5 v3** through its web interface. The switch has no
API; the protocol was derived from the firmware's own JavaScript on a unit
running `V1.00(ACPS.2)C0` and verified against the hardware.

One provider instance addresses one switch, because the device is the unit of
contention: a GS1200 answers one request at a time and serves a single web
session. Making it the unit of configuration keeps credentials, timeouts and
TLS settings attached to the thing they describe.

## Example usage

```terraform
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
```

### Several switches

Use aliases:

```terraform
provider "gs1200" {
  alias    = "rack"
  host     = "192.0.2.10"
  password = var.rack_password
}

resource "gs1200_vlan" "iot" {
  provider = gs1200.rack
  vid      = 1003
}
```

Or a module per switch, which lets the resources inside drop the `provider`
argument entirely:

```terraform
module "rack" {
  source    = "./rack"
  providers = { gs1200 = gs1200.rack }
}
```

The provider's lock is keyed by device address, so several switches are
configured in parallel while one switch's requests queue up. `-parallelism=1`
is not required.

## Schema

### Optional

- `host` (String) Address of the switch's web interface, without a scheme, for
  example `192.0.2.10`. Falls back to the `GS1200_HOST` environment variable.
- `password` (String, Sensitive) Web-interface password. It is hashed with
  SHA-256 before being sent, exactly as the firmware's own login page does, so
  the plaintext never reaches the wire. Falls back to `GS1200_PASSWORD`.
- `scheme` (String) `https` (default) or `http`.
- `verify_tls` (Boolean) Verify the switch's TLS certificate. Off by default:
  these switches ship a self-signed certificate that cannot be replaced. Turn
  it on when a proxy with a real certificate fronts the switch.
- `timeout` (Number) Per-request timeout in seconds, default 20. A request
  costs about 1.9 s on idle hardware, almost all of it the TLS handshake; the
  margin is there for whatever else is talking to the same switch.

## Reading without a session

`gs1200_vlan` refreshes and the `gs1200_switch` data source's identity and link
fields are read from endpoints the firmware leaves unauthenticated. Refreshing
those never claims the switch's single web session, so a `plan` does not lock
its owner out of the web interface.

Anything involving PVIDs, port settings or the switch-wide options does need a
session. The provider caches those reads for the duration of a command, so a
five-port switch costs one login rather than five.
