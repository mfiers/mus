# mus — research data management CLI (Go)
#
# Targets:
#   make build        host binary into ./bin/mus
#   make build-all    cross-compile darwin/arm64 + linux/{amd64,arm64} into ./dist/
#   make test         go test ./...
#   make lint         go vet ./... + gofmt -l (errors if any unformatted file)
#   make clean        remove ./bin and ./dist
#   make tidy         go mod tidy

# Prefer a known-recent Go install if the system Go is too old.
GO := $(shell if [ -x /usr/local/go/bin/go ]; then echo /usr/local/go/bin/go; else echo go; fi)

VERSION  := $(shell cat VERSION 2>/dev/null || echo 0.0.1-dev)
COMMIT   := $(shell git -C . rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X 'codeberg.org/atrxia/mus/internal/cli.Version=$(VERSION)' \
            -X 'codeberg.org/atrxia/mus/internal/cli.Commit=$(COMMIT)' \
            -X 'codeberg.org/atrxia/mus/internal/cli.BuildDate=$(DATE)'

PKG := ./cmd/mus

# Signing — uses git's built-in SSH signing (gpg.format=ssh).
# Override at the command line if you keep your signing key elsewhere:
#   make release VERSION=0.1.0 SIGNING_KEY=~/.ssh/some_other_key
SIGNING_KEY ?= $(HOME)/.ssh/id_ed25519

.PHONY: build build-all test lint clean tidy run release release-verify sign

# Binaries that get signed by `pika _sign`. Must match what `make build-all`
# emits in dist/. Update both lists together if a platform is added/dropped.
RELEASE_BINARIES := \
    dist/mus-linux-amd64 \
    dist/mus-linux-arm64 \
    dist/mus-darwin-arm64

build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/mus $(PKG)
	@echo "built bin/mus ($$($(GO) version | awk '{print $$3}'))"

build-all:
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/arm64; do \
	  os=$${target%/*}; arch=$${target#*/}; \
	  out=dist/mus-$$os-$$arch; \
	  echo "==> $$out"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@ls -lh dist/

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...
	@unformatted=$$($(GO) run -mod=mod cmd/gofmt 2>/dev/null || gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "unformatted files:"; echo "$$unformatted"; exit 1; fi

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin dist

run: build
	./bin/mus $(ARGS)

# ---------------------------------------------------------------------------
# Release: cross-compile, generate signed SHA256SUMS, create signed git tag.
#
# Requires:
#   - a working SSH signing key (see SIGNING_KEY above)
#   - clean working tree (git status reports nothing)
#
# Usage:
#   make release VERSION=0.1.0
#
# Pushes are NOT automatic — push manually after inspecting the tag:
#   git push origin main v0.1.0
# ---------------------------------------------------------------------------
release:
	@if [ "$(VERSION)" = "0.0.1-dev" ] || [ -z "$(VERSION)" ]; then \
	  echo "make release VERSION=x.y.z   (cannot release with dev version)"; \
	  exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "refusing to release: working tree is dirty"; \
	  git status --short; \
	  exit 1; \
	fi
	@if git rev-parse --verify --quiet "v$(VERSION)" >/dev/null; then \
	  echo "refusing to release: tag v$(VERSION) already exists"; \
	  exit 1; \
	fi
	@if [ ! -f "$(SIGNING_KEY)" ]; then \
	  echo "signing key not found at $(SIGNING_KEY)"; \
	  exit 1; \
	fi
	@echo "==> building v$(VERSION)"
	rm -rf dist
	$(MAKE) build-all VERSION=$(VERSION)
	@echo "==> ed25519-signing each binary via pika (programmatic upgrade path)"
	$(MAKE) sign
	@echo "==> generating SHA256SUMS (covers binaries and per-binary .sig files)"
	cd dist && sha256sum mus-* > SHA256SUMS
	@echo "==> ssh-signing SHA256SUMS (manual verification path) with $(SIGNING_KEY)"
	ssh-keygen -Y sign -f $(SIGNING_KEY) -n file dist/SHA256SUMS
	@echo "==> verifying ssh signature locally"
	ssh-keygen -Y verify -f .gitsigners \
	    -I "$$(git config user.email)" \
	    -n file -s dist/SHA256SUMS.sig < dist/SHA256SUMS
	@echo "==> creating signed git tag v$(VERSION)"
	git tag -s -m "mus $(VERSION)" "v$(VERSION)"
	@echo
	@echo "release artifacts in ./dist/:"
	@ls -lh dist/
	@echo
	@echo "to publish:"
	@echo "  git push origin main 'v$(VERSION)'"
	@echo "  # then upload dist/mus-* + dist/SHA256SUMS + dist/SHA256SUMS.sig"
	@echo "  # to https://codeberg.org/atrxia/mus/releases/tag/v$(VERSION)"

# Sign each release binary with the shared ed25519 release key via the pika
# CLI. pika lives in the sibling project and owns the only private key file
# (see /data/1/users/mark/pika/doc/signing.md). This target produces a
# `<binary>.sig` next to each binary; `mus upgrade` later verifies these
# against the embedded pubkey in internal/signing.PubkeyB64.
#
# Standalone: run after `make build-all` if you only want to re-sign without
# rebuilding (e.g. after rotating to a new key). Otherwise `make release`
# invokes it automatically.
sign:
	@if ! command -v pika >/dev/null 2>&1; then \
	  echo "pika not found on PATH — install from the sibling project to sign releases"; \
	  exit 1; \
	fi
	@for b in $(RELEASE_BINARIES); do \
	  if [ ! -f $$b ]; then \
	    echo "missing $$b — run 'make build-all' first"; exit 1; \
	  fi; \
	done
	pika _sign $(RELEASE_BINARIES)
	@for b in $(RELEASE_BINARIES); do \
	  test -f $$b.sig || { echo "pika did not produce $$b.sig"; exit 1; }; \
	done
	@echo "signed: $(RELEASE_BINARIES)"

# Verify the most recent build's signature (smoke test for verifiers).
release-verify:
	@if [ ! -f dist/SHA256SUMS.sig ]; then \
	  echo "no dist/SHA256SUMS.sig — run 'make release' first"; exit 1; \
	fi
	ssh-keygen -Y verify -f .gitsigners \
	    -I "$$(git config user.email)" \
	    -n file -s dist/SHA256SUMS.sig < dist/SHA256SUMS
	@cd dist && sha256sum -c SHA256SUMS
