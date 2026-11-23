VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# `make install` puts the binary in $(PREFIX)/bin; use PREFIX=/usr/local (with sudo) for all users.
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build install uninstall lint vuln test e2e e2e-engines docker clean

# Go toolchain: the one on this machine, or the golang image through docker when Go is not installed.
HAVE_GO := $(shell command -v go 2>/dev/null)
DOCKER_GO = docker run --rm -v "$(CURDIR)":/work -w /work -v sentinel-gocache:/root/.cache -v sentinel-gopath:/go \
	-e CGO_ENABLED=0 -e GOFLAGS=-buildvcs=false golang:1.27
ifeq ($(HAVE_GO),)
GO = $(DOCKER_GO) go
else
GO = go
endif

# The container writes as root, so the build hands the files back to the current user.
build:
ifeq ($(HAVE_GO),)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sentinel ./cmd/sentinel \
		&& chown $(shell id -u):$(shell id -g) bin bin/sentinel'
else
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/sentinel ./cmd/sentinel
endif

install: build
	install -d "$(DESTDIR)$(BINDIR)"
	install -m 0755 bin/sentinel "$(DESTDIR)$(BINDIR)/sentinel"
	@echo "installed $(DESTDIR)$(BINDIR)/sentinel"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "note: $(BINDIR) is not in your PATH; add it to your shell profile" ;; esac

uninstall:
	rm -f "$(DESTDIR)$(BINDIR)/sentinel"

# Container image with the binary plus restic, rclone, pg_dump, mariadb-dump and mongodump.
docker: build
	docker build -t sentinel:$(VERSION) -t sentinel:latest .

GOLANGCI_LINT = github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK   = golang.org/x/vuln/cmd/govulncheck@v1.10.0

lint:
	$(GO) run $(GOLANGCI_LINT) run ./...

vuln:
	$(GO) run $(GOVULNCHECK) ./...

test: lint vuln
	$(GO) vet ./...
	$(GO) test ./...

e2e: build
	./test/e2e.sh

# MySQL/MariaDB and MongoDB engines; needs only docker.
e2e-engines:
	./test/e2e-engines.sh

clean:
	rm -rf bin dist
