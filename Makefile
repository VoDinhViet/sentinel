VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# `make install` puts the binary in $(PREFIX)/bin; use PREFIX=/usr/local (with sudo) for all users.
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build install uninstall dist lint vuln layers test e2e e2e-engines docker clean

# Go toolchain: the one on this machine, or the golang image through docker when Go is not installed.
HAVE_GO := $(shell command -v go 2>/dev/null)

# The Go modules of the repo (see go.work). Each has its own go.mod, so the Go tools run once in each.
MODULES := pkg agent
DOCKER_GO = docker run --rm -v "$(CURDIR)":/work -w /work -v sentinel-gocache:/root/.cache -v sentinel-gopath:/go \
	-e CGO_ENABLED=0 -e GOWORK -e GOFLAGS=-buildvcs=false golang:1.27
ifeq ($(HAVE_GO),)
GO = $(DOCKER_GO) go
else
GO = go
endif

# The container writes as root, so the build hands the files back to the current user.
build:
ifeq ($(HAVE_GO),)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'go -C agent build -ldflags "-s -w -X main.version=$(VERSION)" -o ../bin/sentinel ./cmd/sentinel \
		&& chown $(shell id -u):$(shell id -g) bin bin/sentinel'
else
	CGO_ENABLED=0 go -C agent build -ldflags "-s -w -X main.version=$(VERSION)" -o ../bin/sentinel ./cmd/sentinel
endif

# Release files for deploy/install.sh: sentinel_linux_<arch>.tar.gz (the binary, README.md, deploy/) and
# checksums.txt, in dist/. Always built through the golang image so the result does not depend on this machine.
dist:
	rm -rf dist && mkdir -p dist
	$(DOCKER_GO) sh -c 'for arch in amd64 arm64; do mkdir -p dist/$$arch && \
		GOOS=linux GOARCH=$$arch go -C agent build -ldflags "-s -w -X main.version=$(VERSION)" -o ../dist/$$arch/sentinel ./cmd/sentinel || exit 1; done; \
		chown -R $(shell id -u):$(shell id -g) dist'
	for arch in amd64 arm64; do \
		cp README.md dist/$$arch/ && cp -r deploy dist/$$arch/deploy && \
		tar -czf dist/sentinel_linux_$$arch.tar.gz -C dist/$$arch sentinel README.md deploy && rm -rf dist/$$arch; \
	done
	cd dist && sha256sum sentinel_linux_*.tar.gz > checksums.txt && cat checksums.txt

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
	for m in $(MODULES); do $(GO) -C $$m run $(GOLANGCI_LINT) run ./... || exit 1; done

vuln:
	for m in $(MODULES); do $(GO) -C $$m run $(GOVULNCHECK) ./... || exit 1; done

# The agent is shipped alone to many servers: it builds without go.work, uses pkg and nothing of the
# control plane, and pkg uses neither the agent nor the server.
layers:
	GOWORK=off $(GO) -C agent build -o /dev/null ./cmd/sentinel
	! $(GO) -C agent list -deps ./... | grep '^sentinel/server'
	! $(GO) -C pkg list -deps ./... | grep -E '^sentinel/(agent|server)'

test: layers lint vuln
	for m in $(MODULES); do $(GO) -C $$m vet ./... && $(GO) -C $$m test ./... || exit 1; done

e2e: build
	./test/e2e.sh

# MySQL/MariaDB and MongoDB engines; needs only docker.
e2e-engines:
	./test/e2e-engines.sh

clean:
	rm -rf bin dist
