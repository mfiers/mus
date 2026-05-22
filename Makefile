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

# VERSION file is the single source of truth. During development it carries
# a `-dev` suffix (e.g. 0.1.1-dev) — that's the version that WILL be shipped
# on the next `make ship`. Releases strip the suffix; `make ship` bumps the
# file afterwards so the next development cycle has a fresh -dev tag.
VERSION_FILE := VERSION
VERSION      := $(shell cat $(VERSION_FILE) 2>/dev/null || echo 0.0.1-dev)
# RELEASE_VERSION is VERSION minus any -dev suffix — what `make ship` actually
# ships. Useful for inspecting state: `make show-version`.
RELEASE_VERSION := $(shell cat $(VERSION_FILE) 2>/dev/null | sed 's/-dev$$//' || echo 0.0.1)

COMMIT   := $(shell git -C . rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X 'codeberg.org/atrxia/mus/internal/cli.Version=$(VERSION)' \
            -X 'codeberg.org/atrxia/mus/internal/cli.Commit=$(COMMIT)' \
            -X 'codeberg.org/atrxia/mus/internal/cli.BuildDate=$(DATE)'

PKG := ./cmd/mus

# Signing — uses git's built-in SSH signing (gpg.format=ssh).
# Override at the command line if you keep your signing key elsewhere:
#   make release SIGNING_KEY=~/.ssh/some_other_key
SIGNING_KEY ?= $(HOME)/.ssh/id_ed25519

# Bump level for `make bump` / `make ship`. patch (default), minor, or major.
LEVEL ?= patch

.PHONY: build build-all test lint clean tidy run release release-verify \
        sign publish show-version bump ship

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
# Version management
#
# `make show-version`    Print current VERSION file + what `ship` would do.
# `make bump`            Increment patch in VERSION file (no release).
# `make bump LEVEL=minor`  Bump minor (resets patch).
# `make bump LEVEL=major`  Bump major (resets minor + patch).
# `make ship`            Full release+publish: tag, sign, push, upload, bump.
# ---------------------------------------------------------------------------

show-version:
	@echo "VERSION file:      $(VERSION)"
	@echo "Would ship as:     v$(RELEASE_VERSION)"
	@next=$$(scripts/next-version.sh $(RELEASE_VERSION) $(LEVEL)); \
	  echo "Next $(LEVEL):         $$next-dev (after ship)"

bump:
	@next=$$(scripts/next-version.sh $(RELEASE_VERSION) $(LEVEL))-dev; \
	  if [ "$$next" = "$(VERSION)" ]; then \
	    echo "VERSION already at $$next"; \
	  else \
	    echo "$$next" > $(VERSION_FILE); \
	    echo "$(VERSION) -> $$next"; \
	  fi

# ship: the maintainer's one-shot publish pipeline.
#
#   1. Strip -dev from VERSION file so the released build carries a clean tag.
#   2. Commit the version pin.
#   3. `make release` — cross-build, ed25519-sign each binary (pika prompts
#      for the passphrase ONCE here), generate + ssh-sign SHA256SUMS, create
#      signed git tag. This is the only interactive step.
#   4. Bump VERSION to next $(LEVEL)-dev and commit.
#   5. Push main + the new tag.
#   6. `make publish` — upload assets to Codeberg via the API
#      (CODEBERG_TOKEN/CODEBERG_GENERIC_TOKEN must be set).
#
# Aborts before any irreversible step if the working tree is dirty, the tag
# already exists, or the pubkey/token is missing.
ship:
	@if [ -n "$$(git status --porcelain)" ]; then \
	  echo "refusing to ship: working tree is dirty"; \
	  git status --short; exit 1; \
	fi
	@if git rev-parse --verify --quiet "v$(RELEASE_VERSION)" >/dev/null; then \
	  echo "refusing to ship: tag v$(RELEASE_VERSION) already exists"; \
	  echo "   run 'make bump' first if you want the next version"; \
	  exit 1; \
	fi
	@token=$${CODEBERG_TOKEN:-$${CODEBERG_GENERIC_TOKEN:-}}; \
	if [ -z "$$token" ]; then \
	  echo "CODEBERG_TOKEN (or CODEBERG_GENERIC_TOKEN) must be set for publish"; \
	  exit 1; \
	fi
	@echo "==> shipping v$(RELEASE_VERSION)"
	@# 1. Pin VERSION to the release (strip -dev) and commit if changed.
	@echo "$(RELEASE_VERSION)" > $(VERSION_FILE)
	@if ! git diff --quiet $(VERSION_FILE); then \
	  git add $(VERSION_FILE); \
	  git commit -m "release $(RELEASE_VERSION)" -q; \
	fi
	@# 2. Sign + tag (interactive — pika prompts for passphrase).
	$(MAKE) release VERSION=$(RELEASE_VERSION)
	@# 3. Bump VERSION for the next dev cycle and commit.
	@next=$$(scripts/next-version.sh $(RELEASE_VERSION) $(LEVEL))-dev; \
	  echo "$$next" > $(VERSION_FILE); \
	  git add $(VERSION_FILE); \
	  git commit -m "bump version to $$next" -q
	@# 4. Push branch + tag.
	git push origin main v$(RELEASE_VERSION)
	@# 5. Upload assets to Codeberg.
	$(MAKE) publish VERSION=$(RELEASE_VERSION)
	@echo
	@echo "shipped v$(RELEASE_VERSION). VERSION file is now $$(cat $(VERSION_FILE))."

# ---------------------------------------------------------------------------
# Release: cross-compile, generate signed SHA256SUMS, create signed git tag.
#
# Called by `make ship`; you can also run it directly:
#   make release VERSION=0.1.0
#
# Pushes are NOT automatic — `make ship` handles the push + publish.
# ---------------------------------------------------------------------------
release:
	@if [ -z "$(VERSION)" ] || echo "$(VERSION)" | grep -qE '(-dev$$|^0\.0\.1-dev$$)'; then \
	  echo "make release VERSION=x.y.z   (cannot release with -dev version)"; \
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

# Publish the release built by `make release VERSION=$(VERSION)` to Codeberg.
# Creates the release object (idempotent if it already exists) and uploads
# every binary + .sig + SHA256SUMS + SHA256SUMS.sig as an asset.
#
# Requires:
#   - dist/ populated by `make release VERSION=$(VERSION)`
#   - git tag v$(VERSION) pushed to origin
#   - CODEBERG_TOKEN (or CODEBERG_GENERIC_TOKEN) in the environment with
#     `write:repository` scope on atrxia/mus
publish:
	@case "$(VERSION)" in \
	  ""|*-dev) echo "make publish VERSION=x.y.z  (got '$(VERSION)')"; exit 1 ;; \
	esac
	bash scripts/publish-release.sh "$(VERSION)"

# Verify the most recent build's signature (smoke test for verifiers).
release-verify:
	@if [ ! -f dist/SHA256SUMS.sig ]; then \
	  echo "no dist/SHA256SUMS.sig — run 'make release' first"; exit 1; \
	fi
	ssh-keygen -Y verify -f .gitsigners \
	    -I "$$(git config user.email)" \
	    -n file -s dist/SHA256SUMS.sig < dist/SHA256SUMS
	@cd dist && sha256sum -c SHA256SUMS
