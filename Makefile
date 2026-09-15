# todobem — build/run/test. Single Go binary, stdlib only, web/ embedded (no build step).
BINARY ?= todobem
ADDR   ?= 127.0.0.1:7788
# CODEX / CLAUDE pin this run to exactly those folders (a source not named is off; the Settings
# page is then read-only); unset, the server reads the folders of ~/.todobem/settings.json, else
# ~/.codex and ~/.claude.
CODEX  ?=
CLAUDE ?=

.PHONY: build run token install test fmt vet clean deploy stop

build: ## compile the single binary (embeds web/)
	go build -o $(BINARY) ./cmd/todobem

run: build ## build then run in the foreground (loopback by default); the UI is token-locked
	./$(BINARY) -addr $(ADDR) $(if $(CODEX),-codex $(CODEX)) $(if $(CLAUDE),-claude $(CLAUDE))

token: build ## print a one-time login link for the UI (see also: ./todobem token -revoke)
	./$(BINARY) token -addr $(ADDR)

install: ## go install todobem into $GOBIN / $GOPATH/bin
	go install ./cmd/todobem

test: ## the full gate: gofmt, vet, go test, and the JS regression tests
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test ./...
	node --test cmd/todobem/app_test.js

fmt: ## gofmt the tree
	gofmt -w .

vet: ; go vet ./...

deploy: ## build and run in the background via scripts/deploy.sh (loopback)
	./scripts/deploy.sh

stop: ## stop a backgrounded instance started by scripts/deploy.sh
	./scripts/deploy.sh stop

clean: ; rm -f $(BINARY)
