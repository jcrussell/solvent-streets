BINARY := pvmt
VERSION ?= dev
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X pvmt/internal/build.Version=$(VERSION) -X pvmt/internal/build.Date=$(DATE)"

.PHONY: build test clean

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) ./cmd/pvmt

test:
	go test ./...

clean:
	rm -f $(BINARY)
