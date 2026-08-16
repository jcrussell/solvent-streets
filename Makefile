BINARY ?= pvmt
PREFIX ?= $(HOME)/.local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# go.mod declares the floor Go version. WASM builds embedded into the
# main binary via go:embed must use a matching toolchain or runtime
# semantics may drift between cmd/wasm/pvmt and cmd/pvmt.
GO_MOD_VERSION := $(shell awk '/^go /{print $$2; exit}' go.mod)

LDFLAGS := -X github.com/jcrussell/solvent-streets/internal/build.Version=$(VERSION) \
	-X github.com/jcrussell/solvent-streets/internal/build.Commit=$(COMMIT) \
	-X github.com/jcrussell/solvent-streets/internal/build.Date=$(DATE)

.PHONY: build test e2e clean wasm lint lint-js gendocs release-dry-run site site-clean site-report deploy \
	fmt vet tidy cover help install pre-commit

wasm:
	@want="$(GO_MOD_VERSION)"; \
	  host=$$(go env GOVERSION | sed 's/^go//'); \
	  if [ "$${host%.*}" != "$${want%.*}" ]; then \
	    echo "warning: go.mod declares go $$want; host has go $$host — WASM may drift from main build"; \
	  fi
	@mkdir -p internal/export/wasm
	GOOS=js GOARCH=wasm go build -o internal/export/wasm/pvmt.wasm ./cmd/wasm/pvmt
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

# Lint + type-check the browser JS extracted from the export templates
# (internal/export/templates/app.js, game.js). Installs the pinned devDeps on
# first run only. typecheck is `tsc --checkJs` over the JSDoc-annotated sources
# (see tsconfig.json); both are hard gates.
lint-js:
	@test -d node_modules || npm ci
	npm run lint
	npm run typecheck

fmt:
	gofmt -w ./cmd ./internal ./pkg

vet:
	go vet ./...

tidy:
	go mod tidy

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

pre-commit: fmt vet lint lint-js

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
	@echo "  lint-js       eslint + tsc --checkJs the export template JS (app.js/game.js)"
	@echo "  fmt           gofmt -w on cmd/internal/pkg"
	@echo "  vet           go vet ./..."
	@echo "  tidy          go mod tidy"
	@echo "  cover         coverage report (writes coverage.out)"
	@echo "  pre-commit    fmt + vet + lint + lint-js (link to .git/hooks/pre-commit)"
	@echo "  wasm          rebuild forecast WASM (embedded into binary)"
	@echo "  gendocs       regenerate docs/reference/ from cobra"
	@echo "  site          render the combined tagged static site to \$$SITE_DIR"
	@echo "                (needs each included example ingested + computed — see examples/all/pvmt.toml)"
	@echo "  site-report   per-file size totals and per-city budgets over \$$SITE_DIR"
	@echo "  deploy        push existing \$$SITE_DIR to gh-pages (run 'make site' first)"
	@echo "  clean         remove build outputs"

SITE_DIR := site
# Directory holding the combined [[include]] config that unions every example
# into one tagged site (see examples/all/pvmt.toml). `pvmt` resolves its config
# from the working directory, so `make site` runs the exporter from there.
SITE_CONFIG_DIR := examples/all

site: wasm
	cd $(SITE_CONFIG_DIR) && go run -ldflags "$(LDFLAGS)" $(CURDIR)/cmd/pvmt export -o "$(CURDIR)/$(SITE_DIR)" --clean

site-clean:
	rm -rf $(SITE_DIR)

# Where the site's bytes live: the SIZES check of `pvmt check-site`, which
# reports a per-city figure for each data file against its budget and a total
# for the shared assets. The rest of the audit runs too (it is read-only and
# cheap), so this target exits with check-site's own status — a tree that FAILs
# the audit must not report success just because the size lines printed. The
# full output is dumped whenever the run failed or the size report is missing.
site-report:
	@if [ ! -d "$(SITE_DIR)" ]; then \
		echo "ERROR: $(SITE_DIR)/ does not exist — run 'make site' first"; exit 1; \
	fi
	@out=$$(go run ./cmd/pvmt check-site "$(SITE_DIR)" 2>&1); rc=$$?; \
		if printf '%s\n' "$$out" | grep 'sizes:'; then \
			[ $$rc -eq 0 ] || printf '%s\n' "$$out"; \
		else \
			printf '%s\n' "$$out"; [ $$rc -ne 0 ] || rc=1; \
		fi; \
		exit $$rc

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
