# Ferramentas do portão ficam em .tools/, compiladas com a toolchain do go.mod,
# para não depender nem alterar as versões globais em $GOPATH/bin.
TOOLS    := $(CURDIR)/.tools
LINT     := $(TOOLS)/golangci-lint
VULN     := $(TOOLS)/govulncheck
LINT_VERSION := v2.15.0
VULN_VERSION := v1.9.0
GOVERSION := $(shell go env GOVERSION)
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: check build test test-race lint vet vuln cover evals tools clean

# Pacotes do core medidos na cobertura (fakes e contract são apoio de teste).
CORE_PKGS = $(shell go list ./internal/core/... | grep -vE '/(fakes|contract)$$' | paste -sd, -)
CORE_MIN  := 80

## check: portão de qualidade de todo commit
check: vet lint test-race cover vuln
	@echo "make check: ok"

## cover: cobertura do core >= CORE_MIN
cover:
	@go test -count=1 -coverpkg=$(CORE_PKGS) -coverprofile=.cover.out ./internal/core/... >/dev/null
	@go tool cover -func=.cover.out | awk -v min=$(CORE_MIN) '/^total:/ { sub("%","",$$3); printf "cobertura do core: %s%% (mínimo %s%%)\n", $$3, min; if ($$3+0 < min) exit 1 }'

## evals: tool calling e injeção com modelo real (NEXUS_EVAL_URL, padrão :8081)
evals:
	NEXUS_EVAL_URL=$${NEXUS_EVAL_URL:-http://127.0.0.1:8081} go test -tags evals -count=1 -v -timeout 40m ./test/evals/ | grep -vE '^=== (RUN|PAUSE|CONT)'

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/nexus ./cmd/nexus

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

vet:
	go vet ./...

## lint: golangci-lint (staticcheck, depguard do core, gofmt, ...)
lint: $(LINT)
	$(LINT) run ./...

vuln: $(VULN)
	$(VULN) ./...

## tools: (re)instala as ferramentas nas versões fixadas
tools:
	rm -f $(LINT) $(VULN)
	$(MAKE) $(LINT) $(VULN)

$(LINT):
	GOTOOLCHAIN=$(GOVERSION) GOBIN=$(TOOLS) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION)

$(VULN):
	GOTOOLCHAIN=$(GOVERSION) GOBIN=$(TOOLS) go install golang.org/x/vuln/cmd/govulncheck@$(VULN_VERSION)

clean:
	rm -rf bin
