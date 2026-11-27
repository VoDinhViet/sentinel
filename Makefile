VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# `make install` puts the binary in $(PREFIX)/bin; use PREFIX=/usr/local (with sudo) for all users.
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build build-server install uninstall dist lint vuln layers test e2e e2e-engines e2e-control docker clean

# Go toolchain: the one on this machine, or the golang image through docker when Go is not installed.
HAVE_GO := $(shell command -v go 2>/dev/null)

# The Go modules of the repo (see go.work). Each has its own go.mod, so the Go tools run once in each.
MODULES := pkg agent server
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

# The control plane: bin/sentinel-server and bin/sentinelctl.
build-server:
ifeq ($(HAVE_GO),)
	mkdir -p bin
	$(DOCKER_GO) sh -c 'for cmd in sentinel-server sentinelctl; do \
		go -C server build -ldflags "-s -w -X main.version=$(VERSION)" -o ../bin/$$cmd ./cmd/$$cmd || exit 1; done \
		&& chown -R $(shell id -u):$(shell id -g) bin'
else
	for cmd in sentinel-server sentinelctl; do \
		CGO_ENABLED=0 go -C server build -ldflags "-s -w -X main.version=$(VERSION)" -o ../bin/$$cmd ./cmd/$$cmd || exit 1; done
endif

# Release files for deploy/install.sh: sentinel_linux_<arch>.tar.gz (the binary, README.md, deploy/), the bare
# binary sentinel_linux_<arch> (what agent.update downloads) and checksums.txt, in dist/. With SENTINEL_RELEASE_KEY
# (a file made by `sentinelctl release keygen`) and SENTINEL_RELEASE_URL (where the binaries will be published) it
# also writes dist/manifest-<version>-linux-<arch>.json, signed; VERSION has to be x.y.z (a tag v1.2.0 will do).
# The binaries are always built through the golang image so the result does not depend on this machine.
dist:
	rm -rf dist && mkdir -p dist
	$(DOCKER_GO) sh -c 'for arch in amd64 arm64; do mkdir -p dist/$$arch && \
		GOOS=linux GOARCH=$$arch go -C agent build -ldflags "-s -w -X main.version=$(VERSION)" -o ../dist/$$arch/sentinel ./cmd/sentinel || exit 1; done; \
		chown -R $(shell id -u):$(shell id -g) dist'
	for arch in amd64 arm64; do \
		cp dist/$$arch/sentinel dist/sentinel_linux_$$arch && cp README.md dist/$$arch/ && cp -r deploy dist/$$arch/deploy && \
		tar -czf dist/sentinel_linux_$$arch.tar.gz -C dist/$$arch sentinel README.md deploy && rm -rf dist/$$arch; \
	done
	cd dist && sha256sum sentinel_linux_*.tar.gz > checksums.txt && cat checksums.txt
ifdef SENTINEL_RELEASE_KEY
	@test -n "$(SENTINEL_RELEASE_URL)" || { echo "SENTINEL_RELEASE_URL is the https directory the binaries are published in"; exit 1; }
	go -C server build -o ../bin/sentinelctl ./cmd/sentinelctl
	for arch in amd64 arm64; do bin/sentinelctl release sign --key "$(SENTINEL_RELEASE_KEY)" --binary dist/sentinel_linux_$$arch \
		--version "$(VERSION:v%=%)" --os linux --arch $$arch --url "$(SENTINEL_RELEASE_URL)/sentinel_linux_$$arch" --out dist/manifest-$(VERSION:v%=%)-linux-$$arch.json || exit 1; done
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
	for m in $(MODULES); do $(GO) -C $$m run $(GOLANGCI_LINT) run ./... || exit 1; done

vuln:
	for m in $(MODULES); do $(GO) -C $$m run $(GOVULNCHECK) ./... || exit 1; done

# The agent is shipped alone to many servers: it builds without go.work, uses pkg and nothing of the
# control plane, and pkg uses neither the agent nor the server. The server never uses the agent either. Only pkg/pki may turn off the
# system check of a server certificate (it replaces it with a check against a pinned CA).
layers:
	GOWORK=off $(GO) -C agent build -o /dev/null ./cmd/sentinel
	GOWORK=off $(GO) -C server build -o /dev/null ./cmd/sentinel-server
	GOWORK=off $(GO) -C server build -o /dev/null ./cmd/sentinelctl
	! $(GO) -C server list -deps ./... | grep '^sentinel/agent'
	! $(GO) -C agent list -deps ./... | grep '^sentinel/server'
	! $(GO) -C pkg list -deps ./... | grep -E '^sentinel/(agent|server)'
	! grep -rn --include='*.go' InsecureSkipVerify agent pkg server 2>/dev/null | grep -v '^pkg/pki/'

test: layers lint vuln
	for m in $(MODULES); do $(GO) -C $$m vet ./... && $(GO) -C $$m test ./... || exit 1; done

e2e: build
	./test/e2e.sh

# The control plane with the real binaries, on localhost; needs no docker.
e2e-control: build build-server
	./test/control-plane.sh

# MySQL/MariaDB and MongoDB engines; needs only docker.
e2e-engines:
	./test/e2e-engines.sh

clean:
	rm -rf bin dist
