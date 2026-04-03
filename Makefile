# SwiftCloud CLI Makefile

# Binary name
BINARY_NAME=cloud

# Version info
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod

# Build flags
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

# Directories
CMD_DIR=cmd
INTERNAL_DIR=internal

# Source files
SOURCES=$(shell find . -name '*.go' -type f)

# Default target
.PHONY: all
all: build

# Build the binary
.PHONY: build
build: $(SOURCES)
	@echo "Building $(BINARY_NAME)..."
	$(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME) .

# Build for Linux
.PHONY: build-linux
build-linux: $(SOURCES)
	@echo "Building $(BINARY_NAME) for Linux..."
	GOOS=linux GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME)-linux-amd64 .

# Build for macOS
.PHONY: build-darwin
build-darwin: $(SOURCES)
	@echo "Building $(BINARY_NAME) for macOS..."
	GOOS=darwin GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME)-darwin-arm64 .

# Build for Windows
.PHONY: build-windows
build-windows: $(SOURCES)
	@echo "Building $(BINARY_NAME) for Windows..."
	GOOS=windows GOARCH=amd64 $(GOBUILD) $(LDFLAGS) -o bin/$(BINARY_NAME)-windows-amd64.exe .

# Build all platforms
.PHONY: build-all
build-all: build-linux build-darwin build-windows

# Install the binary globally
.PHONY: install
install: $(SOURCES)
	@echo "Installing $(BINARY_NAME)..."
	$(GOBUILD) $(LDFLAGS) -o $(GOPATH)/bin/$(BINARY_NAME) .

# Run the CLI
.PHONY: run
run:
	@echo "Running $(BINARY_NAME)..."
	$(GOCMD) run . $(ARGS)

# Test the code
.PHONY: test
test:
	@echo "Running tests..."
	$(GOTEST) -v ./...

# Run tests with coverage
.PHONY: test-coverage
test-coverage:
	@echo "Running tests with coverage..."
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Lint the code
.PHONY: lint
lint:
	@echo "Running linter..."
	golangci-lint run ./...

# Format the code
.PHONY: fmt
fmt:
	@echo "Formatting code..."
	$(GOCMD) fmt ./...

# Tidy dependencies
.PHONY: deps
deps:
	@echo "Tidying dependencies..."
	$(GOMOD) tidy
	$(GOMOD) verify

# Clean build artifacts
.PHONY: clean
clean:
	@echo "Cleaning..."
	rm -rf bin/
	$(GOCLEAN)

# Show help
.PHONY: help
help:
	@echo "SwiftCloud CLI Makefile"
	@echo ""
	@echo "Usage:"
	@echo "  make              - Build the CLI"
	@echo "  make build        - Build the CLI"
	@echo "  make build-linux  - Build for Linux"
	@echo "  make build-darwin - Build for macOS"
	@echo "  make build-windows - Build for Windows"
	@echo "  make build-all    - Build for all platforms"
	@echo "  make install      - Install to GOPATH/bin"
	@echo "  make run ARGS='...' - Run the CLI"
	@echo "  make test         - Run tests"
	@echo "  make test-coverage - Run tests with coverage"
	@echo "  make lint         - Run linter"
	@echo "  make fmt          - Format code"
	@echo "  make deps         - Tidy dependencies"
	@echo "  make clean        - Clean build artifacts"
	@echo "  make help         - Show this help"
