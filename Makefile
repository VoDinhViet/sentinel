VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build lint vuln test e2e e2e-engines docker clean

build:
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sentinel ./cmd/sentinel

# Container image with the binary plus restic, rclone, pg_dump, mariadb-dump and mongodump.
docker: build
	docker build -t sentinel:$(VERSION) -t sentinel:latest .

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

# MySQL/MariaDB and MongoDB engines; needs only docker.
e2e-engines:
	./test/e2e-engines.sh

clean:
	rm -rf bin dist
