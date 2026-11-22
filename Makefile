# Makefile for garp Go application

BINARY_NAME=garp
# Local build output (gitignored). Tracked bin/garp + bin/garp.exe are written only by `make release`.
BINARY_PATH=bin/$(BINARY_NAME)-local

# Version embedding
VERSION=0.11.0
# -s -w strip the symbol table and DWARF debug info (~29% smaller binary).
LDFLAGS=-s -w -X garp/app.version=$(VERSION)
# Pure Go (CGO_ENABLED=0): static Linux ELF, no glibc dependency.
BUILD_FLAGS=-trimpath -ldflags "$(LDFLAGS)"
TRACKED_BINS=bin/garp bin/garp.exe
EVAL_CACHE ?= $(HOME)/.cache/garp-eval
EVAL_BASELINE_REF ?= v0.10.0
DIST_PLATFORMS=linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
# Go stamps vcs.modified=true (+dirty) if ANY tracked or untracked file differs from HEAD,
# so every build first restores the tracked release binaries (regenerate with `make release`).
RESTORE_BINS=git checkout -- $(TRACKED_BINS) 2>/dev/null || true

# Default target
all: build

# Build the binary (always runs: go's build cache keeps it fast and the VCS stamp current)
build:
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p bin
	@$(RESTORE_BINS)
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(BINARY_PATH) .
	@echo "Build completed: $(BINARY_PATH)"

# Clean build artifacts (never the tracked release binaries)
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BINARY_PATH) bin/$(BINARY_NAME)-dev dist
	@echo "Clean completed"

# Cross-compile every GitHub Release asset into dist/ (gitignored).
dist:
	git checkout -- $(TRACKED_BINS)
	@test -z "$$(git status --porcelain)" || { echo "dist: commit or stash changes first (go would stamp +dirty):" >&2; git status --short >&2; exit 1; }
	@mkdir -p dist
	@for p in $(DIST_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		echo "  dist/garp-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(BUILD_FLAGS) -o dist/garp-$$os-$$arch$$ext . || exit 1; \
	done

# Refresh the tracked release binaries from the clean dist/ builds; commit them immediately.
release: dist
	cp dist/garp-linux-amd64 bin/garp
	cp dist/garp-windows-amd64.exe bin/garp.exe

# Test the application
test:
	@echo "Running tests..."
	go test ./...
	@echo "Tests completed"

# Test the nested eval harness without fetching a corpus
eval-test:
	cd eval && go test ./...

# Fetch pinned corpora and build the archived previous release, never this tree.
eval-fetch:
	cd eval && go mod download
	go mod download
	cd eval && go run ./cmd/fetch -cache "$(abspath $(EVAL_CACHE))"
	rm -rf "$(abspath $(EVAL_CACHE))/src/$(EVAL_BASELINE_REF)"
	mkdir -p "$(abspath $(EVAL_CACHE))/src/$(EVAL_BASELINE_REF)" "$(abspath $(EVAL_CACHE))/bin"
	git archive "$(EVAL_BASELINE_REF)" | tar -x -C "$(abspath $(EVAL_CACHE))/src/$(EVAL_BASELINE_REF)"
	cd "$(abspath $(EVAL_CACHE))/src/$(EVAL_BASELINE_REF)" && go mod download && CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w -X garp/app.version=$(EVAL_BASELINE_REF:v%=%)" -o "$(abspath $(EVAL_CACHE))/bin/garp-$(EVAL_BASELINE_REF)" .

# Run the offline evaluation with a built harness to preserve its exit code.
eval:
	@mkdir -p bin
	@$(RESTORE_BINS)
	CGO_ENABLED=0 GOPROXY=off go build $(BUILD_FLAGS) -o bin/garp-eval .
	cd eval && GOPROXY=off go build -o ../bin/eval-harness ./cmd/eval
	@cd eval && ../bin/eval-harness -cache "$(abspath $(EVAL_CACHE))" -candidate ../bin/garp-eval -previous "$(abspath $(EVAL_CACHE))/bin/garp-$(EVAL_BASELINE_REF)" -previous-ref "$(EVAL_BASELINE_REF)" -baseline "results/$(EVAL_BASELINE_REF).json" -out results/latest; s=$$?; [ $$s -eq 0 ] || echo "eval: harness exit $$s (1 = quality regression, 2 = harness or setup error)" >&2; exit $$s

# Compare synonym expansion with original wording on the same binary.
eval-or:
	@mkdir -p bin
	@$(RESTORE_BINS)
	CGO_ENABLED=0 GOPROXY=off go build $(BUILD_FLAGS) -o bin/garp-eval .
	cd eval && GOPROXY=off go build -o ../bin/eval-harness ./cmd/eval
	@cd eval && ../bin/eval-harness -cache "$(abspath $(EVAL_CACHE))" -candidate ../bin/garp-eval -previous ../bin/garp-eval -previous-ref single-wording -synonym-or queries/synonym_or.json -out results/latest.or; s=$$?; [ $$s -eq 0 ] || echo "eval-or: harness exit $$s (1 = quality regression, 2 = harness or setup error)" >&2; exit $$s

# Record the previous release as candidate without applying the quality gate.
eval-baseline:
	@mkdir -p bin
	@$(RESTORE_BINS)
	cd eval && GOPROXY=off go build -o ../bin/eval-harness ./cmd/eval
	@cd eval && ../bin/eval-harness -cache "$(abspath $(EVAL_CACHE))" -candidate "$(abspath $(EVAL_CACHE))/bin/garp-$(EVAL_BASELINE_REF)" -previous "$(abspath $(EVAL_CACHE))/bin/garp-$(EVAL_BASELINE_REF)" -previous-ref "$(EVAL_BASELINE_REF)" -out "results/$(EVAL_BASELINE_REF)" -no-gate; s=$$?; [ $$s -eq 0 ] || echo "eval: harness exit $$s (1 = quality regression, 2 = harness or setup error)" >&2; exit $$s

# Format Go code
fmt:
	@echo "Formatting Go code..."
	go fmt ./...
	@echo "Formatting completed"

# Run go mod tidy
tidy:
	@echo "Running go mod tidy..."
	go mod tidy
	@echo "Go mod tidy completed"

# Install dependencies
deps:
	@echo "Installing dependencies..."
	go mod download
	@echo "Dependencies installed"

# Development build with race detection
dev:
	@echo "Building development version with race detection..."
	@mkdir -p bin
	@$(RESTORE_BINS)
	go build -race -ldflags "$(LDFLAGS)" -o bin/$(BINARY_NAME)-dev .
	@echo "Development build completed: bin/$(BINARY_NAME)-dev"

# Run the application with sample arguments
run: build
	@echo "Running $(BINARY_NAME) with sample arguments..."
	./$(BINARY_PATH)

# Install to user's local bin directory
install: tidy build
	@echo "Installing $(BINARY_NAME) to ~/.local/bin..."
	@mkdir -p ~/.local/bin
	cp $(BINARY_PATH) ~/.local/bin/$(BINARY_NAME)
	@echo "Installation completed: ~/.local/bin/$(BINARY_NAME)"
	@echo "Make sure ~/.local/bin is in your PATH"

# Backward-compatible alias; PDF support is included in the default pure-Go build.
install-pdfcpu: install
	@echo "PDF support is included in the default pure-Go build; pdfcpu is no longer required."


# Uninstall from user's local bin directory
uninstall:
	@echo "Removing $(BINARY_NAME) from ~/.local/bin..."
	rm -f ~/.local/bin/$(BINARY_NAME)
	@echo "Uninstallation completed"

# Show help
help:
	@echo "Available targets:"
	@echo "  build    - Build bin/garp-local (static, for local use)"
	@echo "  dist     - Build all release assets into dist/ (clean tree required)"
	@echo "  release  - dist + refresh tracked bin/garp and bin/garp.exe"
	@echo "  clean    - Remove build artifacts"
	@echo "  test     - Run tests"
	@echo "  eval-test - Unit tests of the eval harness (no corpus)"
	@echo "  eval-fetch - Fetch pinned eval corpora and build the previous release"
	@echo "  eval     - Run offline retrieval evaluation and regression gate"
	@echo "  eval-or  - Run paired synonym OR-expansion evaluation and gate"
	@echo "  eval-baseline - Record the previous-release evaluation without gating"
	@echo "  fmt      - Format Go code"
	@echo "  tidy     - Run go mod tidy"
	@echo "  deps     - Install dependencies"
	@echo "  dev      - Build development version with race detection"
	@echo "  run      - Build and run with help"
	@echo "  install  - Install to ~/.local/bin"
	@echo "  install-pdfcpu - Alias for install (PDF support is built in)"
	@echo "  uninstall- Remove from ~/.local/bin"
	@echo "  help     - Show this help"

.PHONY: all build clean dist release test eval-test eval-fetch eval eval-or eval-baseline fmt tidy deps dev run install install-pdfcpu uninstall help
