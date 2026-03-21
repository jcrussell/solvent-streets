BINARY := pvmt
VERSION ?= dev
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X pvmt/internal/build.Version=$(VERSION) -X pvmt/internal/build.Date=$(DATE)"

.PHONY: build test clean wasm

wasm:
	@mkdir -p internal/export/wasm
	GOOS=js GOARCH=wasm go build -o internal/export/wasm/forecast.wasm ./cmd/wasm/forecast
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" internal/export/wasm/wasm_exec.js

build: wasm
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) ./cmd/pvmt

test:
	go test ./...

clean:
	rm -f $(BINARY)
