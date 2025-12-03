# Variables
BINARY_NAME=aild
CMD_PATH=./cmd/aild
BUILD_DIR=.
GO=go
GOFLAGS=
LDFLAGS=
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Build flags
LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Output binary
OUTPUT=$(BUILD_DIR)/$(BINARY_NAME)

# Docker image settings
DOCKER_IMAGE?=aild
DOCKER_TAG?=latest

.PHONY: all build clean test install uninstall run fmt vet lint help docker-build docker-run deps tidy

# Default target
all: clean build

## build: Build the binary
build:
	@echo "Building static binary..."
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) $(LDFLAGS) -o $(OUTPUT) $(CMD_PATH)
	@echo "Static build complete: $(OUTPUT)"

## clean: Remove build artifacts
clean:
	@echo "Cleaning..."
	@rm -f $(OUTPUT)
	@$(GO) clean
	@echo "Clean complete"

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@sed -n 's/^##//p' Makefile | column -t -s ':' | sed -e 's/^/ /'

.DEFAULT_GOAL := help
