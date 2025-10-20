# Makefile for building and installing the manga tool
# Provides build, install, and clean targets

# Configuration variables
BINARY_NAME=manga-tool
GO=go
INSTALL_DIR=/usr/local/bin
INSTALL_USER=$(shell whoami)

# Base build rule
build:
	@echo "Building $(BINARY_NAME)..."
	$(GO) build -o $(BINARY_NAME) ./cmd/manga-tool
	@echo "Build complete."

# Install to system
install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_DIR)..."
	sudo install -m 755 -o $(INSTALL_USER) $(BINARY_NAME) $(INSTALL_DIR)
	@echo "Installation complete."

# Clean built files
clean:
	@echo "Cleaning build artifacts..."
	rm -f $(BINARY_NAME)
	@echo "Clean complete."

# Run tests
test:
	@echo "Running tests..."
	$(GO) test -v ./...

# Run with default settings
run:
	@echo "Running $(BINARY_NAME)..."
	$(GO) run ./cmd/manga-tool

# Prepare a production release
release: clean
	@echo "Building for production..."
	CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(BINARY_NAME) ./cmd/manga-tool
	@echo "Production build complete."

.PHONY: build install clean test run release

