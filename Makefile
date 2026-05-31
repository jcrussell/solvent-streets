BINARY ?= pvmt
PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# go.mod declares the floor Go version. WASM builds embedded into the
# main binary via go:embed must use a matching toolchain or runtime
# semantics may drift between cmd/wasm/forecast and cmd/pvmt.
GO_MOD_VERSION := $(shell awk '/^go /{print $$2; exit}' go.mod)

LDFLAGS := -X github.com/jcrussell/solvent-streets/internal/build.Version=$(VERSION) \
	-X github.com/jcrussell/solvent-streets/internal/build.Commit=$(COMMIT) \
	-X github.com/jcrussell/solvent-streets/internal/build.Date=$(DATE)

.PHONY: build test e2e clean wasm lint gendocs release-dry-run site site-clean deploy \
	fmt vet tidy cover help install pre-commit

wasm:
	@want="$(GO_MOD_VERSION)"; \
	  host=$$(go env GOVERSION | sed 's/^go//'); \
	  if [ "$${host%.*}" != "$${want%.*}" ]; then \
	    echo "warning: go.mod declares go $$want; host has go $$host — WASM may drift from main build"; \
	  fi
	@mkdir -p internal/export/wasm
	GOOS=js GOARCH=wasm go build -o internal/export/wasm/forecast.wasm ./cmd/wasm/forecast
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" internal/export/wasm/wasm_exec.js

build: wasm
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pvmt

install: build
	@mkdir -p $(PREFIX)/bin
	install -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "installed $(BINARY) to $(PREFIX)/bin/$(BINARY)"

test:
	go test -race ./...

# Real-network e2e. Gated by PVMT_E2E_NETWORK=1 so `make test` stays
# hermetic. Hits Overpass + ArcGIS; 429/504 from upstream is flakiness,
# not a code bug.
e2e:
	PVMT_E2E_NETWORK=1 go test -race -timeout=10m -run TestE2ENetwork ./integration/...

# Pinned floor lives in .golangci-version; CI pins the action to that same
# version. Local installs that drift warn but do not fail (CI is the gate).
lint:
	@floor=$$(cat .golangci-version); installed=$$(golangci-lint --version 2>/dev/null | awk '{print $$4}'); \
		if [ -n "$$installed" ] && [ "v$$installed" != "$$floor" ]; then \
			echo "warning: golangci-lint v$$installed installed, floor is $$floor (.golangci-version)"; \
		fi
	golangci-lint run

fmt:
	gofmt -w ./cmd ./internal ./pkg

vet:
	go vet ./...

tidy:
	go mod tidy

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

pre-commit: fmt vet lint

gendocs:
	go run ./cmd/gendocs

release-dry-run:
	goreleaser release --snapshot --clean --skip=publish

clean:
	rm -f $(BINARY) coverage.out
	rm -rf dist/

help:
	@echo "Targets:"
	@echo "  build         build WASM + binary (default for releases)"
	@echo "  install       build and install to \$$PREFIX/bin (default: ~/.local/bin)"
	@echo "  test          go test -race ./..."
	@echo "  lint          golangci-lint run (pinned in .golangci-version)"
	@echo "  fmt           gofmt -w on cmd/internal/pkg"
	@echo "  vet           go vet ./..."
	@echo "  tidy          go mod tidy"
	@echo "  cover         coverage report (writes coverage.out)"
	@echo "  pre-commit    fmt + vet + lint (link to .git/hooks/pre-commit)"
	@echo "  wasm          rebuild forecast WASM (embedded into binary)"
	@echo "  gendocs       regenerate docs/reference/ from cobra"
	@echo "  site          render full static site to \$$SITE_DIR"
	@echo "  deploy        push existing \$$SITE_DIR to gh-pages (run 'make site' first)"
	@echo "  clean         remove build outputs"

SITE_DIR := site

site: wasm
	go run ./cmd/gensite -o $(SITE_DIR)

site-clean:
	rm -rf $(SITE_DIR)

deploy:
	@if [ "$(SITE_DIR)" = "." ] || [ "$(SITE_DIR)" = ".." ] || [ "$(SITE_DIR)" = "/" ]; then \
		echo "ERROR: SITE_DIR must not be '.', '..', or '/'"; exit 1; \
	fi
	@if [ ! -d "$(SITE_DIR)" ]; then \
		echo "ERROR: $(SITE_DIR)/ does not exist — run 'make site' first"; exit 1; \
	fi
	@cd $(SITE_DIR) && \
		git init -q && \
		git remote add origin "$$(git -C .. remote get-url origin)" && \
		git add . && \
		git commit -q -m "Deploy site" && \
		git push -f origin HEAD:gh-pages && \
		rm -rf .git
	@echo "Deployed to gh-pages branch"
