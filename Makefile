VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PKG     := github.com/chaseungjoon/recom/internal/buildinfo
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).Date=$(DATE)
ASSETS  := internal/helperbin/assets

.PHONY: all build helpers test test-integration lint fmt clean

all: build

## helpers: cross-compile the Linux helpers that get embedded into the client
helpers:
	@for arch in amd64 arm64; do \
		echo "helper linux/$$arch"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(ASSETS)/recom-helper-linux-$$arch ./cmd/recom-helper || exit 1; \
		gzip -9nf $(ASSETS)/recom-helper-linux-$$arch || exit 1; \
	done

## build: build bin/recom for this machine with embedded helpers
build: helpers
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/recom ./cmd/recom

test:
	go test -race ./...

## test-integration: end-to-end tests against a throwaway local sshd
test-integration:
	RECOM_E2E=1 go test -count=1 -v ./internal/e2e/

lint:
	gofmt -l . | (! grep .)
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -rf bin dist $(ASSETS)/*.gz
