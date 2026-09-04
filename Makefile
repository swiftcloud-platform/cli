# SwiftCloud CLI
BINARY  := cloud
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Same variables goreleaser injects, so a local build reports itself honestly.
LDFLAGS := -s -w -X main.version_=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
GOFLAGS := -trimpath
export CGO_ENABLED := 0

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build install run test cover lint fmt tidy cross snapshot clean help

all: build

build:              ## Build ./bin/cloud for this machine
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY) .

install:            ## Install into $(go env GOPATH)/bin
	go install $(GOFLAGS) -ldflags '$(LDFLAGS)' .

run:                ## Run with ARGS='...'
	go run . $(ARGS)

test:               ## Run tests with the race detector
	go test -race ./...

cover:              ## Tests with an HTML coverage report
	go test -race -coverprofile=coverage.out ./... && go tool cover -html=coverage.out -o coverage.html

lint:               ## golangci-lint (CI uses the latest; local needs one built for this Go)
	golangci-lint run ./...

fmt:                ## gofmt + goimports
	gofmt -s -w . && go run golang.org/x/tools/cmd/goimports@latest -w .

tidy:               ## go mod tidy + verify
	go mod tidy && go mod verify

cross:              ## Cross-compile every release target into ./bin
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
	  echo "  $$os/$$arch"; \
	  GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o bin/$(BINARY)_$${os}_$${arch}$$ext . || exit 1; \
	done

snapshot:           ## Full goreleaser build without publishing (needs goreleaser)
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist coverage.out coverage.html

help:
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'
