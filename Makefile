# Ferramentas do portão ficam em .tools/, compiladas com a toolchain do go.mod,
# para não depender nem alterar as versões globais em $GOPATH/bin.
TOOLS    := $(CURDIR)/.tools
LINT     := $(TOOLS)/golangci-lint
VULN     := $(TOOLS)/govulncheck
LINT_VERSION := v2.15.0
VULN_VERSION := v1.9.0
GOVERSION := $(shell go env GOVERSION)
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: check build test test-race lint vet vuln tools clean

## check: portão de qualidade de todo commit
check: vet lint test-race vuln
	@echo "make check: ok"

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
