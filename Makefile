BUILD_SHA := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: build test clean

build:
	CGO_ENABLED=0 go build -ldflags="-X sync.sstools.co/cmd/issued.BuildSHA=$(BUILD_SHA)" -o issued ./cmd/issued

test:
	go test ./...

clean:
	rm -f issued
