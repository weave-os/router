# Prerequisites:
#   - Go 1.25+
#   - sqlc (go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0)
#   - golang-migrate (brew install golang-migrate)
#   - CompileDaemon for `make dev` (go install github.com/githubnemo/CompileDaemon@latest)
#
# Database:
#   Targets that touch the database read DATABASE_URL from .env.development
#   (and .env.local if present). Start Postgres via `make db` or point
#   DATABASE_URL at any Postgres you already have running.

.PHONY: generate generate-statusline build build-router install test test-verbose test-statusline test-install smoke initdb migrate-up migrate-down migrate-create seed setup full-setup local-env local-prereqs local-native-deps local-assets local-ui local-setup local-run local-dev db dev check fmt vet precommit install-hooks help install-cc uninstall-cc up up-hmm down down-hmm logs

# Load DATABASE_URL from .env files (matches docker-compose defaults).
-include .env.development
-include .env.local
export

# Host-mode development defaults. Override LOCAL_DATABASE_URL when the local
# PostgreSQL role or authentication differs from the macOS/Linux defaults.
LOCAL_PG_HOST ?= 127.0.0.1
LOCAL_PG_PORT ?= 5432
LOCAL_PG_USER ?= $(shell id -un)
LOCAL_PG_DB ?= router
LOCAL_PG_SSLMODE ?= disable
LOCAL_DATABASE_URL ?= $(if $(strip $(DATABASE_URL)),$(DATABASE_URL),postgresql://$(LOCAL_PG_USER)@$(LOCAL_PG_HOST):$(LOCAL_PG_PORT)/$(LOCAL_PG_DB)?sslmode=$(LOCAL_PG_SSLMODE))
LOCAL_MIGRATE_DATABASE_URL = $(if $(findstring ?,$(LOCAL_DATABASE_URL)),$(LOCAL_DATABASE_URL)&search_path=router,$(LOCAL_DATABASE_URL)?search_path=router)
LOCAL_PORT ?= 8088
LOCAL_BASE_URL ?= http://localhost:$(LOCAL_PORT)
LOCAL_ONNX_ASSETS_DIR ?= $(CURDIR)/assets
LOCAL_PUBSUB_DISABLED ?= true
LOCAL_HF_BASE_URL ?= https://huggingface.co
LOCAL_TOKENIZERS_DIR ?= $(CURDIR)/.local/libtokenizers
LOCAL_TOKENIZERS_URL ?= https://github.com/daulet/tokenizers/releases/download/v1.27.0/libtokenizers.darwin-arm64.tar.gz
LOCAL_ORT_LIBRARY_DIR ?= $(if $(strip $(shell brew list --versions onnxruntime 2>/dev/null)),$(shell brew --prefix onnxruntime 2>/dev/null)/lib,/opt/homebrew/lib)
LOCAL_CGO_LDFLAGS ?= $(strip $(CGO_LDFLAGS) -L$(LOCAL_TOKENIZERS_DIR))
LOCAL_UI_DIR ?= $(CURDIR)/frontend
LOCAL_UI_OUT ?= $(CURDIR)/assets/ui
LOCAL_UI_REBUILD ?= false

LOCAL_ENV = DATABASE_URL="$(LOCAL_DATABASE_URL)" ROUTER_PUBSUB_DISABLED="$(LOCAL_PUBSUB_DISABLED)"

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

generate: generate-statusline ## Regenerate all generated files (SQLC + statusline prices)
	cd db && sqlc generate

generate-statusline: ## Sync cc-statusline.sh prices block from pricing.go
	go run ./cmd/genprices

build: ## Typecheck the entire module
	go build -o /dev/null ./...

ROUTER_INSTALL_DIR ?= $(HOME)/.local/bin
ROUTER_DATA_DIR ?= $(HOME)/.local/share/weave-router
ROUTER_BIN ?= ./bin/router
ROUTER_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build-router: ## Build the installable Router CLI and service binary
	@mkdir -p "$$(dirname "$(ROUTER_BIN)")"
	CGO_LDFLAGS="$(CGO_LDFLAGS) -L$(LOCAL_TOKENIZERS_DIR) -L$(LOCAL_ORT_LIBRARY_DIR) -lonnxruntime" \
		go build -tags ORT -ldflags "-X main.cliVersion=$(ROUTER_VERSION) -X main.defaultUIAssetsDir=$(ROUTER_DATA_DIR)/ui -X main.defaultInstallerDir=$(ROUTER_DATA_DIR)/installer" -o "$(ROUTER_BIN)" ./cmd/router

install: local-ui build-router ## Install the Router CLI and dashboard (default: ~/.local/bin)
	@mkdir -p "$(ROUTER_INSTALL_DIR)"
	@mkdir -p "$(ROUTER_DATA_DIR)/ui"
	@cp -R assets/ui/. "$(ROUTER_DATA_DIR)/ui/"
	@mkdir -p "$(ROUTER_DATA_DIR)/installer"
	@cp -R install/. "$(ROUTER_DATA_DIR)/installer/"
	install -m 0755 "$(ROUTER_BIN)" "$(ROUTER_INSTALL_DIR)/router"
	@echo "✓ Installed router to $(ROUTER_INSTALL_DIR)/router"
	@echo "  Dashboard assets: $(ROUTER_DATA_DIR)/ui"
	@echo "  Start the service with: router web start"

test: ## Run all tests
	go test ./...

test-verbose: ## Run all tests with verbose output
	go test -v ./...

test-statusline: ## Run the cc-statusline.sh regression tests (offline)
	@bash install/tests/cc-statusline_test.sh

test-install: ## Run offline installer regression tests
	@bash install/tests/codex_install_test.sh
	@bash install/tests/codex-status_test.sh
	@bash install/tests/key_reuse_test.sh
	@bash install/tests/models_test.sh
	@bash install/tests/registry_test.sh
	@bash install/tests/packaging_test.sh

embed-registry: ## Re-embed install/directives.tsv + registry.sh into install.sh
	@bash install/scripts/embed-registry.sh

smoke: ## Pre-merge smoke suite: real router stack + real Anthropic (needs ANTHROPIC_API_KEY)
	./scripts/smoke/run.sh

initdb: ## Create the database and router schema (idempotent)
	@go run ./cmd/initdb

migrate-up: initdb ## Apply all pending migrations
	migrate -path db/migrations \
		-database "$(DATABASE_URL)&search_path=router" up

migrate-down: ## Roll back the last migration
	migrate -path db/migrations \
		-database "$(DATABASE_URL)&search_path=router" down 1

migrate-create: ## Create a new migration (usage: make migrate-create NAME=add-foo)
	@if [ -z "$(NAME)" ]; then echo "Usage: make migrate-create NAME=add-foo"; exit 1; fi
	migrate create -ext sql -dir db/migrations $(NAME)

seed: ## Create a local dev installation + API key and print usage instructions
	go run ./cmd/seed

setup: migrate-up seed ## Bootstrap (host DB): init DB, run migrations, seed an API key

local-env: ## Create or extend .env.local with host-mode development defaults
	@if [ -e .env.local ]; then \
		if ! grep -q '^DATABASE_URL=' .env.local; then \
			umask 077; echo 'DATABASE_URL=$(LOCAL_DATABASE_URL)' >> .env.local; \
		fi; \
		if ! grep -q '^ROUTER_ONNX_ASSETS_DIR=' .env.local; then \
			umask 077; echo 'ROUTER_ONNX_ASSETS_DIR=$(LOCAL_ONNX_ASSETS_DIR)' >> .env.local; \
		fi; \
		if ! grep -q '^ROUTER_PUBSUB_DISABLED=' .env.local; then \
			umask 077; echo 'ROUTER_PUBSUB_DISABLED=$(LOCAL_PUBSUB_DISABLED)' >> .env.local; \
		fi; \
		if ! grep -q '^PORT=' .env.local; then \
			umask 077; echo 'PORT=$(LOCAL_PORT)' >> .env.local; \
		fi; \
	else \
		umask 077; { \
			echo '# Local host-mode settings generated by make local-env.'; \
			echo 'DATABASE_URL=$(LOCAL_DATABASE_URL)'; \
			echo 'ROUTER_ONNX_ASSETS_DIR=$(LOCAL_ONNX_ASSETS_DIR)'; \
			echo 'ROUTER_PUBSUB_DISABLED=$(LOCAL_PUBSUB_DISABLED)'; \
			echo 'PORT=$(LOCAL_PORT)'; \
		} > .env.local; \
	fi
	@echo "Host-mode settings are in .env.local (ignored by git)."

local-prereqs: ## Check host-mode prerequisites without starting Docker
	@for command in go pg_isready migrate; do \
		command -v "$$command" >/dev/null 2>&1 || { echo "error: $$command is required on PATH" >&2; exit 1; }; \
	done
	@pg_isready -h "$(LOCAL_PG_HOST)" -p "$(LOCAL_PG_PORT)" >/dev/null || { \
		echo "error: PostgreSQL is not accepting connections at $(LOCAL_PG_HOST):$(LOCAL_PG_PORT)" >&2; \
		echo "       Start your local PostgreSQL service, or override LOCAL_PG_HOST/LOCAL_PG_PORT." >&2; \
		 exit 1; \
	}

local-native-deps: ## Check/install native libraries required by host-mode ONNX routing
	@command -v brew >/dev/null 2>&1 || { echo "error: Homebrew is required for host-mode ONNX Runtime" >&2; exit 1; }
	@brew list --versions onnxruntime >/dev/null 2>&1 || { \
		echo "error: ONNX Runtime is missing; run 'brew install onnxruntime'" >&2; \
		exit 1; \
	}
	@command -v curl >/dev/null 2>&1 || { echo "error: curl is required on PATH" >&2; exit 1; }
	@if [ ! -f "$(LOCAL_TOKENIZERS_DIR)/libtokenizers.a" ]; then \
		mkdir -p "$(LOCAL_TOKENIZERS_DIR)"; \
		echo "Downloading libtokenizers for Apple Silicon"; \
		curl --fail --show-error --location --retry 8 --retry-all-errors --retry-delay 3 \
			"$(LOCAL_TOKENIZERS_URL)" -o "$(LOCAL_TOKENIZERS_DIR)/libtokenizers.darwin-arm64.tar.gz"; \
		tar -xzf "$(LOCAL_TOKENIZERS_DIR)/libtokenizers.darwin-arm64.tar.gz" -C "$(LOCAL_TOKENIZERS_DIR)"; \
		found="$$(find "$(LOCAL_TOKENIZERS_DIR)" -type f -name libtokenizers.a -print -quit)"; \
		if [ -z "$$found" ]; then \
			echo "error: libtokenizers.a was not found in the downloaded archive" >&2; \
			exit 1; \
		fi; \
		if [ "$$found" != "$(LOCAL_TOKENIZERS_DIR)/libtokenizers.a" ]; then \
			cp "$$found" "$(LOCAL_TOKENIZERS_DIR)/libtokenizers.a"; \
		fi; \
	fi
	@echo "Native ONNX dependencies are ready."

local-assets: ## Download the pinned Jina ONNX assets for host-mode routing
	@command -v curl >/dev/null 2>&1 || { echo "error: curl is required on PATH" >&2; exit 1; }
	@set -eu; \
		asset_dir="$(LOCAL_ONNX_ASSETS_DIR)/jina-v2-base-code-int8"; \
		mkdir -p "$$asset_dir"; \
		download_asset() { \
			url="$$1"; dest="$$2"; minimum="$$3"; part="$$dest.part"; \
			if [ -f "$$dest" ]; then \
				size=$$(wc -c < "$$dest" | tr -d ' '); \
				if [ "$$size" -ge "$$minimum" ]; then \
					echo "Already downloaded: $$dest"; return; \
				fi; \
				if [ ! -f "$$part" ]; then mv "$$dest" "$$part"; fi; \
			fi; \
			echo "Downloading $$url (retries enabled; partial files resume)"; \
			curl --fail --show-error --location --progress-bar \
				--connect-timeout 20 --retry 10 --retry-all-errors \
				--retry-delay 3 --retry-max-time 1800 --continue-at - \
				"$$url" -o "$$part"; \
			size=$$(wc -c < "$$part" | tr -d ' '); \
			if [ "$$size" -lt "$$minimum" ]; then \
				echo "error: downloaded $$part is only $$size bytes; keeping it for a later resume" >&2; \
				exit 1; \
			fi; \
			mv "$$part" "$$dest"; \
		}; \
		download_asset \
			"$(LOCAL_HF_BASE_URL)/jinaai/jina-embeddings-v2-base-code/resolve/516f4baf13dec4ddddda8631e019b5737c8bc250/onnx/model_quantized.onnx" \
			"$$asset_dir/model.onnx" 104857600; \
		download_asset \
			"$(LOCAL_HF_BASE_URL)/jinaai/jina-embeddings-v2-base-code/resolve/516f4baf13dec4ddddda8631e019b5737c8bc250/tokenizer.json" \
			"$$asset_dir/tokenizer.json" 1024
	@echo "Jina ONNX assets are ready under $(LOCAL_ONNX_ASSETS_DIR)."

local-ui: ## Build and install the dashboard static export for host-mode routing
	@command -v npm >/dev/null 2>&1 || { echo "error: npm is required to build the local dashboard" >&2; exit 1; }
	@if [ "$(LOCAL_UI_REBUILD)" = "true" ] || [ ! -s "$(LOCAL_UI_DIR)/out/index.html" ]; then \
		echo "==> Installing dashboard dependencies"; \
		if [ ! -d "$(LOCAL_UI_DIR)/node_modules" ]; then npm --prefix "$(LOCAL_UI_DIR)" ci --prefer-offline; fi; \
		echo "==> Building dashboard"; \
		npm --prefix "$(LOCAL_UI_DIR)" run build; \
	fi
	@test -s "$(LOCAL_UI_DIR)/out/index.html" || { \
		echo "error: dashboard build did not produce $(LOCAL_UI_DIR)/out/index.html" >&2; \
		exit 1; \
	}
	@mkdir -p "$(LOCAL_UI_OUT)"
	@cp -R "$(LOCAL_UI_DIR)/out/." "$(LOCAL_UI_OUT)/"
	@echo "Dashboard assets are ready under $(LOCAL_UI_OUT)."

local-setup: local-prereqs ## Setup host PostgreSQL, seed a router key, and wire Codex OAuth
	@$(MAKE) --no-print-directory local-env
	@echo "==> Preparing PostgreSQL database $(LOCAL_PG_DB)"
	@$(LOCAL_ENV) go run ./cmd/initdb
	@echo "==> Applying migrations"
	@migrate -path db/migrations -database "$(LOCAL_MIGRATE_DATABASE_URL)" up
	@echo "==> Creating router key and wiring Codex"
	@seed_output="$$( $(LOCAL_ENV) go run ./cmd/seed )" || exit 1; \
	printf '%s\n' "$$seed_output"; \
	router_key="$$(printf '%s\n' "$$seed_output" | awk '/^  rk_[[:alnum:]_-]+$$/ { print $$1; exit }')"; \
	if [ -z "$$router_key" ]; then \
		echo "error: seed did not print a router key" >&2; \
		exit 1; \
	fi; \
	WEAVE_ROUTER_KEY="$$router_key" ./install/install.sh --codex --base-url "$(LOCAL_BASE_URL)" --non-interactive
	@echo ""
	@echo "Setup complete. Download model assets with 'make local-assets' if needed, then run 'make local-run'."
	@echo "Codex keeps its existing ChatGPT OAuth login; no OPENAI_API_KEY is required."

local-run: local-prereqs local-native-deps local-ui ## Run the router against host PostgreSQL (single-process mode)
	@test -s "$(LOCAL_ONNX_ASSETS_DIR)/jina-v2-base-code-int8/model.onnx" && \
		test -s "$(LOCAL_ONNX_ASSETS_DIR)/jina-v2-base-code-int8/tokenizer.json" || { \
		echo "error: local ONNX assets are missing; run 'make local-assets' first" >&2; \
		exit 1; \
	}
	@echo "Starting router at $(LOCAL_BASE_URL). Press Ctrl-C to stop."
	@DATABASE_URL="$(LOCAL_DATABASE_URL)" \
		ROUTER_PUBSUB_DISABLED="$(LOCAL_PUBSUB_DISABLED)" \
		ROUTER_DEPLOYMENT_MODE=selfhosted \
		ROUTER_ONNX_ASSETS_DIR="$(LOCAL_ONNX_ASSETS_DIR)" \
		ROUTER_ONNX_LIBRARY_DIR="$(LOCAL_ORT_LIBRARY_DIR)" \
		CGO_LDFLAGS="$(LOCAL_CGO_LDFLAGS)" \
		PORT="$(LOCAL_PORT)" \
		OPENAI_API_KEY= ANTHROPIC_API_KEY= \
		go run -tags ORT ./cmd/router

local-dev: local-prereqs local-native-deps local-ui ## Run the host PostgreSQL router with CompileDaemon hot reload
	@test -s "$(LOCAL_ONNX_ASSETS_DIR)/jina-v2-base-code-int8/model.onnx" && \
		test -s "$(LOCAL_ONNX_ASSETS_DIR)/jina-v2-base-code-int8/tokenizer.json" || { \
		echo "error: local ONNX assets are missing; run 'make local-assets' first" >&2; \
		exit 1; \
	}
	@$(MAKE) --no-print-directory \
		DATABASE_URL="$(LOCAL_DATABASE_URL)" \
		ROUTER_PUBSUB_DISABLED="$(LOCAL_PUBSUB_DISABLED)" \
		ROUTER_DEPLOYMENT_MODE=selfhosted \
		ROUTER_ONNX_ASSETS_DIR="$(LOCAL_ONNX_ASSETS_DIR)" \
		ROUTER_ONNX_LIBRARY_DIR="$(LOCAL_ORT_LIBRARY_DIR)" \
		CGO_LDFLAGS="$(LOCAL_CGO_LDFLAGS)" \
		PORT="$(LOCAL_PORT)" \
		OPENAI_API_KEY= ANTHROPIC_API_KEY= dev

full-setup: generate-statusline ## Bootstrap router: docker compose + seed + interactively wire Claude Code
	@if [ -n "$(KEY)" ] && [ -n "$(BASE_URL)" ]; then \
		INSTALL_CMD='WEAVE_ROUTER_KEY="$(KEY)" ./install/install.sh --claude --base-url "$(BASE_URL)"'; \
		[ -n "$(SCOPE)" ] && INSTALL_CMD="$$INSTALL_CMD --scope $(SCOPE)"; \
		[ -n "$(DIR)" ] && INSTALL_CMD="$$INSTALL_CMD --dir $(DIR)"; \
		[ "$(NON_INTERACTIVE)" = "1" ] && INSTALL_CMD="$$INSTALL_CMD --non-interactive"; \
		echo "==> Wiring Claude Code → $(BASE_URL)..."; \
		eval "$$INSTALL_CMD"; \
	else \
		if [ -n "$(KEY)" ] || [ -n "$(BASE_URL)" ]; then \
			echo "error: KEY and BASE_URL must both be provided together."; \
			exit 1; \
		fi; \
		./install/spin "Building docker compose stack (postgres, migrate, server)" \
			docker compose up --build -d || exit 1; \
		./install/spin "Waiting for router /health" bash -c '\
			for i in $$(seq 1 60); do \
				curl -fsS --max-time 2 http://localhost:8080/health >/dev/null 2>&1 && exit 0; \
				sleep 1; \
			done; \
			echo "router did not become healthy within 60s. Tail with: make logs" >&2; \
			exit 1' || exit 1; \
		SEED_CAPTURE="$$(mktemp -t full-setup-seed.XXXXXX.log)"; \
		WEAVE_SPIN_CAPTURE="$$SEED_CAPTURE" ./install/spin "Seeding Weave Router API key" \
			docker compose run --rm seed || { rm -f "$$SEED_CAPTURE"; exit 1; }; \
		WEAVE_KEY=$$(grep -oE "^  rk_[a-zA-Z0-9_-]+$$" "$$SEED_CAPTURE" | head -1 | xargs); \
		rm -f "$$SEED_CAPTURE"; \
		if [ -z "$$WEAVE_KEY" ]; then \
			echo "error: failed to extract router key from seed output."; \
			exit 1; \
		fi; \
		echo "    key: $$WEAVE_KEY"; \
		echo ""; \
		WEAVE_ROUTER_KEY="$$WEAVE_KEY" ./install/install.sh --claude --base-url http://localhost:8080; \
		echo ""; \
		echo "Done. Router on http://localhost:8080. Share with teammates: make full-setup KEY=$$WEAVE_KEY BASE_URL=<reachable-url>"; \
	fi

db: ## Start the compose Postgres only (port 5433)
	docker compose up -d postgres
	@echo ""
	@echo "Postgres is running on localhost:5433."
	@echo "Add this to .env.local if not already set:"
	@echo '  DATABASE_URL=postgresql://router:router@localhost:5433/router?sslmode=disable'

dev: ## Run with hot-reload (CompileDaemon)
	# `-tags ORT` is required for hugot v0.7+ to enable the ONNX Runtime
	# backend. Without it, cluster.NewEmbedder fails at boot and the
	# router falls open to the heuristic (Anthropic-only) — which silently
	# breaks any eval that expects v0.X-cluster routing. The Dockerfile
	# already builds with this tag; do not drop it from any production-
	# bound build either. See router/CLAUDE.md "Cluster routing (P0)".
	#
	# CGO_LDFLAGS (libtokenizers) and ROUTER_ONNX_LIBRARY_DIR
	# (libonnxruntime) come from .env.local on macOS — see the comments
	# there for setup. On Linux the brew/.local paths don't apply; the
	# Dockerfile is the production path.
	CompileDaemon \
		-build="go build -tags ORT -o ./bin/server ./cmd/router" \
		-command="./bin/server" \
		-exclude-dir="vendor" \
		-exclude-dir=".vscode" \
		-exclude-dir="bin" \
		-exclude-dir=".venv" \
		-exclude-dir="__pycache__" \
		-exclude-dir=".pytest_cache" \
		-exclude-dir=".mypy_cache" \
		-exclude-dir=".ruff_cache" \
		-exclude-dir=".bench-cache" \
		-exclude-dir=".embedding-cache" \
		-exclude-dir="node_modules" \
		-exclude-dir="results" \
		-exclude-dir="logs" \
		-exclude-dir="assets" \
		-exclude-dir=".git" \
		-exclude-dir="eval" \
		-exclude-dir="scripts" \
		-exclude-dir="docs" \
		-exclude-dir=".local" \
		-exclude-dir="install" \
		-pattern="(.+\.go|.+\.sql)$$" \
		-graceful-kill=true \
		-log-prefix=false

up: ## Start the compose stack in the background (no install.sh wiring)
	docker compose up --build -d

up-hmm: ## Start the stack with the opt-in frozen HMM policy sidecar
	docker compose -f docker-compose.yml -f sidecars/hmm/docker-compose.yml \
		--profile hmm up --build -d

down: ## Stop the compose stack, including the optional HMM sidecar (keeps the postgres volume)
	docker compose --profile hmm down

down-hmm: ## Stop the compose stack including the optional HMM sidecar
	docker compose -f docker-compose.yml -f sidecars/hmm/docker-compose.yml \
		--profile hmm down

logs: ## Tail the server logs
	docker compose logs -f server

install-cc: generate-statusline ## Wire only Claude Code at the local docker-compose router (assumes it's already running)
	./install/install.sh --claude --local

uninstall-cc: ## Remove the local Claude Code → router config
	./install/uninstall.sh

fmt: ## Check gofmt (fails on unformatted files)
	@UNFORMATTED=$$(gofmt -l .); \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "error: Go files are not formatted. Run 'gofmt -w .'"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

precommit: fmt vet build test test-statusline test-install ## Fast pre-commit check (no codegen, no DB)

install-hooks: ## Install git pre-commit hook
	@HOOK_DIR=$$(git rev-parse --git-common-dir)/hooks; \
	mkdir -p "$$HOOK_DIR"; \
	cp scripts/pre-commit "$$HOOK_DIR/pre-commit"; \
	chmod +x "$$HOOK_DIR/pre-commit"; \
	echo "Pre-commit hook installed at $$HOOK_DIR/pre-commit"

check: generate fmt vet build test test-statusline test-install ## Full CI-equivalent check
	@if ! git diff --quiet internal/sqlc/; then \
		echo "error: sqlc generation produced uncommitted changes"; \
		git diff internal/sqlc/; \
		exit 1; \
	fi
	@echo "All checks passed."
