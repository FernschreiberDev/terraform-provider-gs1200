# A worked example against two GS1200-5 v3 units.
#
# One provider instance per switch: the device is the unit of contention (it
# answers one request at a time and serves a single web session), so it is
# also the unit of configuration.

terraform {
  required_providers {
    gs1200 = {
      source  = "fernschreiberdev/gs1200"
      version = "~> 0.1"
    }
  }
}

variable "switch_password" {
  description = "Web-interface password of the switch"
  type        = string
  sensitive   = true
}

provider "gs1200" {
  alias    = "rack"
  host     = "192.0.2.10"
  password = var.switch_password
}

# What the switch reports, without writing anything. Useful for discovering a
# configuration before taking it over.
data "gs1200_switch" "rack" {
  provider = gs1200.rack
}

output "rack_ports_down" {
  value = [for l in data.gs1200_switch.rack.links : l.port if !l.up]
}

# --- The switch itself -----------------------------------------------------
resource "gs1200_system" "rack" {
  provider = gs1200.rack

  name            = "rack-a"
  loop_prevention = true
  snmp            = true
  igmp_snooping   = true
}

# --- VLANs -----------------------------------------------------------------
# These declare existence only. Which ports carry them belongs to the port
# resources, so the two never write the same bytes.

# The management VLAN already exists; import it rather than create it, and
# refuse to destroy it — losing it means walking to the switch.
#
#   terraform import 'gs1200_vlan.mgmt' 1
resource "gs1200_vlan" "mgmt" {
  provider = gs1200.rack
  vid      = 1

  lifecycle {
    prevent_destroy = true
  }
}

resource "gs1200_vlan" "iot" {
  provider = gs1200.rack
  vid      = 1003
}

# --- Ports -----------------------------------------------------------------

# Uplink to the core: management native, IoT riding tagged.
resource "gs1200_port" "port1" {
  provider = gs1200.rack

  port     = 1
  pvid     = 1
  untagged = [1]
  tagged   = [1003]

  depends_on = [gs1200_vlan.mgmt, gs1200_vlan.iot]
}

# Access port, capped at 10 Mbit in and 2 Mbit out.
resource "gs1200_port" "port3" {
  provider = gs1200.rack

  port     = 3
  pvid     = 1003
  untagged = [1003]

  ingress_rate_kbps = 10240
  egress_rate_kbps  = 2048

  depends_on = [gs1200_vlan.iot]
}
