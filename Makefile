VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build lint vuln test e2e clean

build:
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sentinel ./cmd/sentinel

GOLANGCI_LINT = github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK   = golang.org/x/vuln/cmd/govulncheck@v1.10.0

lint:
	go run $(GOLANGCI_LINT) run ./...

vuln:
	go run $(GOVULNCHECK) ./...

test: lint vuln
	go vet ./...
	go test ./...

e2e: build
	./test/e2e.sh

clean:
	rm -rf bin dist
