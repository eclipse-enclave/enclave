# Copyright (C) 2026 EclipseSource GmbH and others.
#
# This program and the accompanying materials are made available under the
# terms of the MIT License, which is available in the project root.
#
# SPDX-License-Identifier: MIT

.PHONY: build cross-build install install-binary clean-legacy-assets uninstall clean clean-lint-cache test deb lint lint-changed lint-report fmt vet generate lint-tools check-go completions check-license-headers

BIN_DIR ?= ./bin
BINARY ?= enclave
UNAME_S := $(shell uname -s)
CMD_DIR := ./cmd/enclave
COMPLETIONS_DIR ?= ./completions
VERSION ?= 0.1.0
REPORTS_DIR := ./reports
REQUIRED_LINT_TOOLS := golangci-lint gosec shellcheck
LINT_GO_DIRS := cmd internal extensions/tools
LINT_TMP_DIR := ./.tmp
LINT_GO_CACHE := $(LINT_TMP_DIR)/go-build
LINT_GOLANGCI_CACHE := $(LINT_TMP_DIR)/golangci-lint-cache
BASE_REF ?=

check-go:
	@command -v go >/dev/null 2>&1 || { echo "Go is not installed. Install Go 1.24+ from https://go.dev/dl/"; exit 1; }
	@GO_VER=$$(go version | awk '{split($$3, a, "."); print a[2]}'); \
	if [ "$$GO_VER" -lt 24 ] 2>/dev/null; then \
		echo "Go 1.24+ is required (found $$(go version))"; \
		exit 1; \
	fi

build: check-go
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(CMD_DIR)

completions:
	mkdir -p $(COMPLETIONS_DIR)
	$(BIN_DIR)/$(BINARY) completion bash > $(COMPLETIONS_DIR)/enclave

# make install now installs only the self-contained binary. This fixed path is
# used solely for conservative cleanup of assets staged by older source
# installs; unlike the former INSTALL_DIR, it is intentionally not overridable.
ifeq ($(UNAME_S),Darwin)
override LEGACY_INSTALL_DIR := $(HOME)/Library/Application Support/org.eclipse.enclave/data
INSTALL_BIN ?= /usr/local/bin
else
override LEGACY_INSTALL_DIR := $(HOME)/.local/share/enclave
INSTALL_BIN ?= $(HOME)/.local/bin
endif

install: build completions
ifeq ($(filter Linux Darwin,$(UNAME_S)),)
	$(error make install supports Linux and macOS hosts only; on Windows use WSL2)
endif
	$(MAKE) install-binary INSTALL_BINARY_LABEL="$(BINARY)"

INSTALL_BINARY_LABEL ?= $(BINARY)

install-binary: clean-legacy-assets
	mkdir -p "$(INSTALL_BIN)"
	cp $(BIN_DIR)/$(BINARY) "$(INSTALL_BIN)/$(BINARY).new"
	mv -f "$(INSTALL_BIN)/$(BINARY).new" "$(INSTALL_BIN)/$(BINARY)"
ifeq ($(UNAME_S),Linux)
	# freedesktop shell completion (Linux only).
	mkdir -p "$(HOME)/.local/share/bash-completion/completions"
	cp $(COMPLETIONS_DIR)/enclave "$(HOME)/.local/share/bash-completion/completions/enclave"
endif
	@echo "Installed $(INSTALL_BINARY_LABEL) to $(INSTALL_BIN)/$(BINARY)"

clean-legacy-assets:
	go run ./cmd/enclave-clean-legacy-assets -root "$(LEGACY_INSTALL_DIR)"

uninstall:
	rm -f "$(INSTALL_BIN)/$(BINARY)"
ifeq ($(UNAME_S),Linux)
	rm -f "$(HOME)/.local/share/bash-completion/completions/enclave"
endif
	@echo "Uninstalled $(BINARY)"

clean:
	rm -rf $(BIN_DIR) $(LINT_TMP_DIR)

clean-lint-cache:
	rm -rf $(LINT_TMP_DIR)

test: check-go
	go test ./...

# Build Debian package using dpkg-buildpackage
# Output goes to dist/ directory
deb:
	mkdir -p dist
	@tmpdir=$$(mktemp -d) && \
	cp -r . "$$tmpdir/enclave" && \
	cd "$$tmpdir/enclave" && dpkg-buildpackage -us -uc -b && \
	mv "$$tmpdir"/*.deb "$(CURDIR)/dist/" && \
	( find "$$tmpdir" -type d -exec chmod u+w {} + 2>/dev/null || true ) && \
	rm -rf "$$tmpdir"
	@echo "Package built: dist/"

# Build Debian package without build dependencies check (faster for development)
deb-quick:
	mkdir -p dist
	@tmpdir=$$(mktemp -d) && \
	cp -r . "$$tmpdir/enclave" && \
	cd "$$tmpdir/enclave" && dpkg-buildpackage -us -uc -b -d && \
	mv "$$tmpdir"/*.deb "$(CURDIR)/dist/" && \
	( find "$$tmpdir" -type d -exec chmod u+w {} + 2>/dev/null || true ) && \
	rm -rf "$$tmpdir"
	@echo "Package built: dist/"

# Static analysis - run without saving (for CI)
lint: lint-tools
	@mkdir -p $(LINT_GO_CACHE) $(LINT_GOLANGCI_CACHE)
	@echo "Checking license headers..."
	@./scripts/check-license-headers.sh
	@echo "Running go vet..."
	@pkgs="$$(go list ./... | grep -v '/vendor/' || true)"; \
	if [ -n "$$pkgs" ]; then \
		go vet $$pkgs; \
	fi
	@echo "Running golangci-lint..."
	@fail=0; \
	if find . -maxdepth 1 -type f -name '*.go' -print -quit | grep -q .; then \
		echo "  -> ."; \
		GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" \
		GOLANGCI_LINT_CACHE="$(CURDIR)/$(LINT_GOLANGCI_CACHE)" \
		golangci-lint run . || fail=1; \
	fi; \
	for dir in $(LINT_GO_DIRS); do \
		if find "$$dir" -type f -name '*.go' -print -quit | grep -q .; then \
			echo "  -> $$dir"; \
			( \
				cd "$$dir" && \
				GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" \
				GOLANGCI_LINT_CACHE="$(CURDIR)/$(LINT_GOLANGCI_CACHE)" \
				golangci-lint run ./... \
			) || fail=1; \
		fi; \
	done; exit $$fail
	@echo "Running gosec..."
	GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" gosec -quiet -exclude-dir=vendor ./...
	@echo "Running shellcheck..."
	@scripts="$$(find . -type f \( -name '*.sh' -o -path '*/build-scripts/bin/*' \) -not -path './vendor/*' | sort)"; \
	if [ -z "$$scripts" ]; then \
		echo "No shell scripts found"; \
	else \
		shellcheck --severity=warning $$scripts; \
	fi

lint-tools:
	@for tool in $(REQUIRED_LINT_TOOLS); do \
		if ! command -v $$tool >/dev/null 2>&1; then \
			echo "missing required linter: $$tool"; \
			exit 1; \
		fi; \
	done

check-license-headers:
	@./scripts/check-license-headers.sh

# Incremental lint - only changed files/packages
lint-changed: lint-tools
	@mkdir -p $(LINT_GO_CACHE) $(LINT_GOLANGCI_CACHE)
	LINT_GO_DIRS="$(LINT_GO_DIRS)" \
	BASE_REF="$(BASE_REF)" \
	GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" \
	GOLANGCI_LINT_CACHE="$(CURDIR)/$(LINT_GOLANGCI_CACHE)" \
	./scripts/lint-changed.sh

# Static analysis - save reports to reports/
lint-report: lint-tools
	@mkdir -p $(REPORTS_DIR)
	@mkdir -p $(LINT_GO_CACHE) $(LINT_GOLANGCI_CACHE)
	@echo "Running golangci-lint..."
	@{ \
		if find . -maxdepth 1 -type f -name '*.go' -print -quit | grep -q .; then \
			echo "== . =="; \
			GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" \
			GOLANGCI_LINT_CACHE="$(CURDIR)/$(LINT_GOLANGCI_CACHE)" \
			golangci-lint run .; \
			echo; \
		fi; \
		for dir in $(LINT_GO_DIRS); do \
			if find "$$dir" -type f -name '*.go' | grep -q .; then \
				echo "== $$dir =="; \
				( \
					cd "$$dir" && \
					GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" \
					GOLANGCI_LINT_CACHE="$(CURDIR)/$(LINT_GOLANGCI_CACHE)" \
					golangci-lint run ./... \
				); \
				echo; \
			fi; \
		done; \
	} > $(REPORTS_DIR)/golangci-lint.txt 2>&1 || true
	@echo "  -> $(REPORTS_DIR)/golangci-lint.txt"
	@echo "Running gosec..."
	@GOCACHE="$(CURDIR)/$(LINT_GO_CACHE)" gosec -exclude-dir=vendor ./... > $(REPORTS_DIR)/gosec.txt 2>&1 || true
	@echo "  -> $(REPORTS_DIR)/gosec.txt"
	@echo "Running shellcheck..."
	@scripts="$$(find . -type f \( -name '*.sh' -o -path '*/build-scripts/bin/*' \) -not -path './vendor/*' | sort)"; \
	if [ -z "$$scripts" ]; then \
		echo "No shell scripts found" > $(REPORTS_DIR)/shellcheck.txt; \
	else \
		shellcheck --severity=warning $$scripts > $(REPORTS_DIR)/shellcheck.txt 2>&1 || true; \
	fi
	@echo "  -> $(REPORTS_DIR)/shellcheck.txt"
	@echo "Running govulncheck..."
	@command -v govulncheck >/dev/null 2>&1 && \
		govulncheck ./... > $(REPORTS_DIR)/govulncheck.txt 2>&1 || echo "govulncheck not installed, skipping"
	@echo "  -> $(REPORTS_DIR)/govulncheck.txt"
	@echo "Reports saved to $(REPORTS_DIR)/"

# Format code
fmt:
	gofmt -w .

# Run go vet only
vet:
	go vet ./...

# Cross-compile every package for representative macOS and Windows targets
# without cgo. Release artifacts remain scoped separately.
cross-build: check-go
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build ./...
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...

generate:
	go generate ./internal/config ./cmd/enclave
