# Build and install terraform-provider-gs1200 into a filesystem mirror.
#
# There is no registry involved: OpenTofu resolves the provider from a
# directory laid out the way a registry mirror would be, which is what
# `filesystem_mirror` in a .tofurc points at.

VERSION ?= 0.1.0
BINARY  := terraform-provider-gs1200
# Where the provider is filed in a mirror. Terraform resolves a short source
# to registry.terraform.io, OpenTofu to registry.opentofu.org — the same
# provider, two addresses — so `install` files it under both and either tool
# finds it.
NAMESPACE := fernschreiberdev/gs1200
REGISTRIES := registry.terraform.io registry.opentofu.org
ADDRESS := registry.terraform.io/$(NAMESPACE)

# Where a developer's own OpenTofu looks. Override for the CI runner.
MIRROR ?= $(HOME)/.local/share/tofu-plugins

# The platforms worth shipping: this Mac, and the self-hosted runner.
PLATFORMS := darwin_arm64 linux_amd64 linux_arm64

LDFLAGS := -s -w -X main.version=$(VERSION)

# Compilation reproductible : à source égale, octets égaux.
#
# -buildvcs=false parce que Go estampille par défaut le commit courant et
# l'état de l'arbre dans le binaire. Le même code compilé avant et après un
# commit donne alors des octets différents, le lockfile généré depuis l'un
# rejette l'autre, et l'erreur parle de somme de contrôle sans jamais nommer
# git. La version est déjà stampée explicitement au-dessus.
#
# -trimpath retire les chemins absolus de la machine de compilation, qui
# varient d'un poste à l'autre.
BUILDFLAGS := -trimpath -buildvcs=false

.PHONY: all build test fmt vet install dist clean

all: fmt vet test build

build:
	go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# install copies this platform's binary out of dist, where OpenTofu will
# find it.
#
# It copies rather than rebuilds on purpose. Two `go build` invocations with
# the same flags are not guaranteed to produce the same bytes, and a lockfile
# generated from dist then rejects a locally rebuilt binary with an error that
# blames the checksum rather than the duplication. One build, one artefact.
install: dist
	@set -e; \
	os=$$(go env GOOS); arch=$$(go env GOARCH); \
	src="dist/$(ADDRESS)/$(VERSION)/$${os}_$${arch}/$(BINARY)_v$(VERSION)"; \
	test -f "$$src" || { echo "dist has no $${os}_$${arch}"; exit 1; }; \
	for registry in $(REGISTRIES); do \
	  dir="$(MIRROR)/$$registry/$(NAMESPACE)/$(VERSION)/$${os}_$${arch}"; \
	  mkdir -p "$$dir"; \
	  cp "$$src" "$$dir/"; \
	  echo "installed $(VERSION) for $${os}_$${arch} in $$dir"; \
	done

# dist builds every platform into ./dist, laid out as a filesystem mirror so
# the whole tree can be copied to the runner as-is.
dist: test
	@set -e; \
	rm -rf dist; \
	for platform in $(PLATFORMS); do \
	  os=$${platform%_*}; arch=$${platform#*_}; \
	  dir="dist/$(ADDRESS)/$(VERSION)/$$platform"; \
	  mkdir -p "$$dir"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	    go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o "$$dir/$(BINARY)_v$(VERSION)" .; \
	  echo "built $$platform"; \
	done; \
	find dist -type f -exec shasum -a 256 {} \; > dist/SHA256SUMS

clean:
	rm -rf dist $(BINARY)
