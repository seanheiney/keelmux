.PHONY: build test check clean release
VERSION ?= development
COMMIT ?= $(shell git rev-parse HEAD)
BUILT_AT ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LD_FLAGS = -X github.com/seanheiney/keelmux/internal/buildinfo.Version=$(VERSION) -X github.com/seanheiney/keelmux/internal/buildinfo.Commit=$(COMMIT) -X github.com/seanheiney/keelmux/internal/buildinfo.BuiltAt=$(BUILT_AT)

build:
	go build -trimpath -ldflags '$(LD_FLAGS)' -o bin/keelmux ./cmd/bp
	go build -trimpath -ldflags '$(LD_FLAGS)' -o bin/keelmuxd ./cmd/bpd
	go build -trimpath -ldflags '$(LD_FLAGS)' -o bin/keelmux-entry ./cmd/bpg
	go build -trimpath -ldflags '$(LD_FLAGS)' -o bin/keelmux-supervisor ./cmd/keelmux-supervisor
	ln -sfn keelmux bin/bp
	ln -sfn keelmuxd bin/bpd
	ln -sfn keelmux-entry bin/bpg
test:
	go test ./...
check:
	go test -race ./...
	go vet ./...
clean:
	rm -rf bin
release:
	./scripts/build-release.sh $(VERSION) dist
