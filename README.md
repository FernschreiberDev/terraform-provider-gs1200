# terraform-provider-schaltwerk

Provider OpenTofu pour les switchs **Zyxel GS1200 (v3)**, pilotés par leur
interface web. Écrit pour être utilisé depuis [tf-opnsense](https://github.com/FernschreiberDev/tf-opnsense),
là où le reste du réseau est déjà décrit en HCL.

Le nom est l'allemand pour *appareillage de commutation* : l'armoire où les
circuits sont réellement basculés, par opposition au schéma qui les décrit.

## Ce qu'il gère, et ce qu'il ne gère pas

| | Lecture | Écriture |
|---|---|---|
| VLANs 802.1Q (membres tagués / non tagués) | oui | oui |
| PVID par port | oui | oui |
| Modèle, firmware, VLAN de management | oui | — |

Rien d'autre. Pas de trunking, pas de QoS, pas de mise à jour de firmware :
le protocole a été relevé sur le matériel, pas dans une documentation, et ce
provider ne prétend faire que ce qui a été observé.

**Les autres équipements ne sont pas gérés.** Le MikroTik CRS305 sous SwOS
n'expose ses VLANs ni en SNMP ni par une API exploitable ; le Linksys LGS328C
les expose en lecture SNMP mais pas en écriture. Les bornes WiFi (Apple
AirPort, HUAWEI Mesh) sont pilotables — les pilotes existent en Python dans le
projet switchboard — mais ne sont pas encore portées ici.

## Le protocole, en bref

Tout a été dérivé du JavaScript du firmware d'un GS1200-5 v3 en
`V1.00(ACPS.2)C0`. Quatre points comptent :

- **Une seule session web à la fois.** Tant qu'une session est ouverte,
  personne d'autre n'atteint l'interface — y compris toi. Le provider n'en
  ouvre une que pour écrire, ou pour lire les PVID, et la referme toujours,
  même quand l'`apply` est interrompu. Les lectures authentifiées sont mises
  en cache pour la durée d'une commande : un switch à cinq ports serait sinon
  verrouillé cinq fois de suite par un seul `plan`.
- **La lecture des VLANs ne demande pas de session.** `/vlanEntry.xml` répond
  à qui le demande. C'est une faiblesse du firmware, mais elle a une
  conséquence heureuse : rafraîchir une VLAN ne verrouille jamais ton switch.
- **Les bitmaps de ports sont décalés de un.** Le port 1 est le bit 1, pas le
  bit 0. Se tromper d'un cran déplace silencieusement chaque port d'une
  position — sur une écriture, cela réaffecte du trafic réel à la mauvaise
  prise. C'est la partie la plus testée du code.
- **Les CGI répondent 200 quoi qu'il arrive.** La réponse ne prouve rien :
  chaque écriture est relue et vérifiée contre ce qui était demandé.
- **Son TLS date.** Le GS1200 n'accepte qu'une seule suite : TLS 1.2 avec
  `AES128-GCM-SHA256` sur un échange de clés RSA. Go 1.22 a retiré toutes les
  suites à échange RSA de sa liste par défaut, faute de confidentialité
  persistante — un client Go standard et ce switch n'ont donc plus rien en
  commun, et la connexion meurt sur `tls: handshake failure` sans jamais
  mentionner de chiffrement. `curl` les propose encore, d'où l'illusion que le
  réseau va bien. Le provider nomme cette suite explicitement, en dernier
  recours après les suites modernes.

## Le garde-fou

Une modification qui retirerait un port de la VLAN de management du switch est
refusée :

```
Error: Cannot write VLAN 1

unsafe change refused: this would remove port(s) [1] from the management
VLAN 1; the switch could become unreachable

Set `force = true` on this resource if you are certain, and have physical
access to the switch should it become unreachable.
```

`force = true` lève le refus. Ne le mets que si tu peux atteindre le switch
physiquement, parce que c'est ce que coûtera l'erreur.

## Installation (mirror filesystem)

Ce provider n'est pas publié sur un registry. OpenTofu le résout depuis un
répertoire organisé comme le serait un mirror.

```bash
make install
```

Puis dans `~/.tofurc` :

```hcl
provider_installation {
  filesystem_mirror {
    path    = "/Users/<toi>/.local/share/tofu-plugins"
    include = ["registry.opentofu.org/fernschreiberdev/*"]
  }
  direct {
    exclude = ["registry.opentofu.org/fernschreiberdev/*"]
  }
}
```

Le bloc `direct` est indispensable : sans lui, OpenTofu ira quand même
interroger le registry public pour ce provider, et échouera.

Pour le runner CI, `make dist` construit `darwin_arm64`, `linux_amd64` et
`linux_arm64` dans `./dist`, déjà arborés comme un mirror — l'arbre se copie
tel quel.

## Utilisation

Une instance de provider par switch, via un alias : le matériel est l'unité de
contention, donc aussi l'unité de configuration.

```hcl
provider "schaltwerk" {
  alias    = "gs1200"
  host     = "192.168.2.6"
  password = var.gs1200_password
}

resource "schaltwerk_zyxel_vlan" "iot" {
  provider = schaltwerk.gs1200

  vid      = 1003
  tagged   = [1, 2]
  untagged = [3, 4]
}

resource "schaltwerk_zyxel_pvid" "port3" {
  provider = schaltwerk.gs1200

  port = 3
  vid  = schaltwerk_zyxel_vlan.iot.vid
}
```

Exemple complet dans [`examples/main.tf`](examples/main.tf).

### `provider "schaltwerk"`

| Argument | Défaut | Rôle |
|---|---|---|
| `host` | `$SCHALTWERK_HOST` | Adresse de l'interface web, sans schéma. |
| `password` | `$SCHALTWERK_PASSWORD` | Mot de passe web. Haché en SHA-256 avant envoi, comme le fait la page de login : le clair ne passe jamais sur le fil. |
| `scheme` | `https` | `https` ou `http`. |
| `verify_tls` | `false` | Ces switchs portent un certificat auto-signé non remplaçable. |
| `timeout` | `10` | Secondes par requête. Le CPU du GS1200 est lent. |

### `schaltwerk_zyxel_vlan`

`vid` (1-4094, remplace la ressource si modifié), `tagged`, `untagged`,
`force`. `index` est calculé : c'est l'emplacement de la VLAN dans la table du
constructeur, qui l'adresse par slot et non par identifiant.

Un port présent dans `tagged` **et** `untagged` est traité comme tagué, ce que
fait aussi le firmware.

Créer une VLAN qui existe déjà est refusé plutôt que silencieusement absorbé :

```bash
tofu import 'schaltwerk_zyxel_vlan.mgmt' 1
```

### `schaltwerk_zyxel_pvid`

`port` (remplace la ressource si modifié), `vid`, `force`. Import par numéro
de port.

**Détruire cette ressource ne change rien sur le switch.** Un port a toujours
un PVID ; il n'existe pas d'état « aucun ». Le remettre à 1 déplacerait
silencieusement le port dans la VLAN de management, alors OpenTofu se contente
d'arrêter de le suivre, avec un avertissement.

Conséquence à connaître : détruire d'un coup une VLAN et le PVID qui la vise
échoue, parce que le PVID reste en place. Vise une autre VLAN, applique, puis
supprime.

### `data "schaltwerk_zyxel_switch"`

Tout ce que le switch accepte de dire : `model`, `firmware`, `port_count`,
`vlan_enabled`, `management_vlan`, `vlans`, `pvids`, et `partial` (vrai quand
aucun mot de passe n'est configuré, auquel cas seule la table non
authentifiée a pu être lue).

## Développement

```bash
make          # fmt, vet, test, build
```

Les tests ne touchent aucun matériel. Deux choses les rendent sérieux :

- `internal/zyxel/live_fleet_test.go` compare le parseur Go au pilote Python
  de switchboard **sur les octets réellement servis par les deux GS1200**, le
  27 août 2026. Deux implémentations indépendantes d'un format non documenté
  qui se vérifient l'une l'autre — un parseur seul ne peut que se prouver
  cohérent avec lui-même.
- `internal/fakeswitch` émule un GS1200-5 v3 avec état : session unique, CGI
  qui répondent 200 sans rien appliquer, refus de supprimer une VLAN encore
  utilisée comme PVID. De quoi faire tourner un vrai `tofu apply` :

```bash
go run ./cmd/fakeswitch -addr 127.0.0.1:8099 -password s3cret
```

En reconstruisant la même version, `.terraform.lock.hcl` épingle l'ancien
binaire : supprime-le et relance `tofu init`.
