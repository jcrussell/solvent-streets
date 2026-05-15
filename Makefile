BINARY ?= pvmt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -X github.com/jcrussell/solvent-streets/internal/build.Version=$(VERSION) \
	-X github.com/jcrussell/solvent-streets/internal/build.Commit=$(COMMIT) \
	-X github.com/jcrussell/solvent-streets/internal/build.Date=$(DATE)

.PHONY: build test clean wasm lint gendocs release-dry-run site site-clean deploy

wasm:
	@mkdir -p internal/export/wasm
	GOOS=js GOARCH=wasm go build -o internal/export/wasm/forecast.wasm ./cmd/wasm/forecast
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" internal/export/wasm/wasm_exec.js

build: wasm
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pvmt

test:
	go test -race ./...

# Pinned floor lives in .golangci-version; CI pins the action to that same
# version. Local installs that drift warn but do not fail (CI is the gate).
lint:
	@floor=$$(cat .golangci-version); installed=$$(golangci-lint --version 2>/dev/null | awk '{print $$4}'); \
		if [ -n "$$installed" ] && [ "v$$installed" != "$$floor" ]; then \
			echo "warning: golangci-lint v$$installed installed, floor is $$floor (.golangci-version)"; \
		fi
	golangci-lint run

gendocs:
	go run ./cmd/gendocs

release-dry-run:
	goreleaser release --snapshot --clean --skip=publish

clean:
	rm -f $(BINARY)
	rm -rf dist/

SITE_DIR := site

site: wasm
	go run ./cmd/gensite -o $(SITE_DIR)

site-clean:
	rm -rf $(SITE_DIR)

deploy: site
	@if [ "$(SITE_DIR)" = "." ] || [ "$(SITE_DIR)" = ".." ] || [ "$(SITE_DIR)" = "/" ]; then \
		echo "ERROR: SITE_DIR must not be '.', '..', or '/'"; exit 1; \
	fi
	@cd $(SITE_DIR) && \
		git init -q && \
		git remote add origin "$$(git -C .. remote get-url origin)" && \
		git add . && \
		git commit -q -m "Deploy site" && \
		git push -f origin HEAD:gh-pages && \
		rm -rf .git
	@echo "Deployed to gh-pages branch"
