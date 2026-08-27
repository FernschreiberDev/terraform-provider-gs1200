# Build and install terraform-provider-schaltwerk into a filesystem mirror.
#
# There is no registry involved: OpenTofu resolves the provider from a
# directory laid out the way a registry mirror would be, which is what
# `filesystem_mirror` in a .tofurc points at.

VERSION ?= 0.1.1
BINARY  := terraform-provider-schaltwerk
# The address in required_providers. Nothing is fetched from it; it is the key
# the binary is filed under.
ADDRESS := registry.opentofu.org/fernschreiberdev/schaltwerk

# Where a developer's own OpenTofu looks. Override for the CI runner.
MIRROR ?= $(HOME)/.local/share/tofu-plugins

# The platforms worth shipping: this Mac, and the self-hosted runner.
PLATFORMS := darwin_arm64 linux_amd64 linux_arm64

LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test fmt vet install dist clean

all: fmt vet test build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

# install puts this platform's binary where the local OpenTofu will find it.
#
# CGO_ENABLED=0 matches `dist` exactly. Without it the two targets produce
# different bytes for the same platform, and the .terraform.lock.hcl generated
# from one rejects the binary produced by the other — a mismatch whose error
# message says nothing about the cause.
install: test
	@set -e; \
	os=$$(go env GOOS); arch=$$(go env GOARCH); \
	dir="$(MIRROR)/$(ADDRESS)/$(VERSION)/$${os}_$${arch}"; \
	mkdir -p "$$dir"; \
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o "$$dir/$(BINARY)_v$(VERSION)" .; \
	echo "installed $(VERSION) for $${os}_$${arch} in $$dir"

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
	    go build -ldflags "$(LDFLAGS)" -o "$$dir/$(BINARY)_v$(VERSION)" .; \
	  echo "built $$platform"; \
	done; \
	find dist -type f -exec shasum -a 256 {} \; > dist/SHA256SUMS

clean:
	rm -rf dist $(BINARY)
