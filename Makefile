VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test e2e clean

build:
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sentinel ./cmd/sentinel

test:
	go vet ./...
	go test ./...

e2e: build
	./test/e2e.sh

clean:
	rm -rf bin dist
