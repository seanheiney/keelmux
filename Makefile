.PHONY: build test check clean
build:
	go build -trimpath -o bin/bp ./cmd/bp
	go build -trimpath -o bin/bpd ./cmd/bpd
test:
	go test ./...
check:
	go test -race ./...
	go vet ./...
clean:
	rm -rf bin
