BINARY ?= pvmt
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS := -s -w \
	-X pvmt/internal/build.Version=$(VERSION) \
	-X pvmt/internal/build.Commit=$(COMMIT) \
	-X pvmt/internal/build.Date=$(DATE)

.PHONY: build test clean wasm lint gendocs release-dry-run

wasm:
	@mkdir -p internal/export/wasm
	GOOS=js GOARCH=wasm go build -o internal/export/wasm/forecast.wasm ./cmd/wasm/forecast
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" internal/export/wasm/wasm_exec.js

build: wasm
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pvmt

test:
	go test -race ./...

lint:
	golangci-lint run

gendocs:
	go run ./cmd/gendocs

release-dry-run:
	goreleaser release --snapshot --clean --skip=publish

clean:
	rm -f $(BINARY)
	rm -rf dist/
