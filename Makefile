.PHONY: build clean install test lint fmt vet run-example

BINARY_NAME=cagectl
BUILD_DIR=bin
GO=go
GOFLAGS=-trimpath
LDFLAGS=-s -w -X 'github.com/souvikDevloper/cagectl/internal/cli.Version=0.1.0' \
        -X 'github.com/souvikDevloper/cagectl/internal/cli.BuildDate=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)' \
        -X 'github.com/souvikDevloper/cagectl/internal/cli.GitCommit=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")'

## build: Compile the binary
build:
	@echo "==> Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/cagectl

## install: Install binary to /usr/local/bin (requires sudo)
install: build
	@echo "==> Installing $(BINARY_NAME) to /usr/local/bin..."
	@sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@sudo chmod +x /usr/local/bin/$(BINARY_NAME)
	@echo "==> Installed successfully."

## clean: Remove build artifacts
clean:
	@echo "==> Cleaning..."
	@rm -rf $(BUILD_DIR)
	@$(GO) clean

## test: Run all tests
test:
	@echo "==> Running tests..."
	$(GO) test -v -race -count=1 ./...

## lint: Run golangci-lint
lint:
	@echo "==> Linting..."
	@golangci-lint run ./...

## fmt: Format all Go source files
fmt:
	@echo "==> Formatting..."
	@$(GO) fmt ./...

## vet: Run go vet
vet:
	@echo "==> Vetting..."
	@$(GO) vet ./...

## setup-rootfs: Download Alpine Linux minimal rootfs
setup-rootfs:
	@echo "==> Setting up Alpine rootfs..."
	@bash scripts/setup-rootfs.sh

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'
