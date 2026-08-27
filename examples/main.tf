# A worked example against the two GS1200-5 v3 in the rack.
#
# One aliased provider instance per switch: the device is the unit of
# contention (it serves a single web session), so it is also the unit of
# configuration.

terraform {
  required_providers {
    schaltwerk = {
      source  = "fernschreiberdev/schaltwerk"
      version = "~> 0.1"
    }
  }
}

variable "gs1200_password" {
  description = "Mot de passe de l'interface web du GS1200 principal"
  type        = string
  sensitive   = true
}

provider "schaltwerk" {
  alias    = "gs1200"
  host     = "192.168.2.6"
  password = var.gs1200_password
}

# Ce que le switch rapporte, sans rien y écrire. Utile pour découvrir une
# configuration avant de la reprendre en main.
data "schaltwerk_zyxel_switch" "gs1200" {
  provider = schaltwerk.gs1200
}

output "gs1200_vlans" {
  value = data.schaltwerk_zyxel_switch.gs1200.vlans
}

# --- VLAN IoT, taguée vers l'uplink, non taguée sur le port 5 --------------
resource "schaltwerk_zyxel_vlan" "iot" {
  provider = schaltwerk.gs1200

  vid      = 1003
  tagged   = [1, 2] # trunks vers le coeur de réseau
  untagged = [3, 4] # ports d'accès
}

# Le PVID d'un port est une ressource distincte : appartenir à une VLAN et
# recevoir son trafic non tagué sont deux réglages différents sur ce matériel.
resource "schaltwerk_zyxel_pvid" "port3" {
  provider = schaltwerk.gs1200

  port = 3
  # La référence ordonne les deux : la VLAN existe avant qu'un port la vise.
  vid = schaltwerk_zyxel_vlan.iot.vid
}

resource "schaltwerk_zyxel_pvid" "port4" {
  provider = schaltwerk.gs1200

  port = 4
  vid  = schaltwerk_zyxel_vlan.iot.vid
}

# --- La VLAN de management -------------------------------------------------
# Elle existe déjà : on l'importe plutôt que de la créer, et on interdit sa
# destruction. La supprimer rendrait le switch injoignable.
#
#   tofu import 'schaltwerk_zyxel_vlan.mgmt' 1
resource "schaltwerk_zyxel_vlan" "mgmt" {
  provider = schaltwerk.gs1200

  vid      = 1
  untagged = [1, 2]

  lifecycle {
    prevent_destroy = true
  }
}
