VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint fmt cover clean

build:
	CGO_ENABLED=0 go build -ldflags '$(LDFLAGS)' -o bin/tldfinder ./cmd/tldfinder

test:
	go test -race ./...

lint:
	golangci-lint run

fmt:
	gofumpt -w .

cover:
	go test -coverprofile=cover.out ./...
	go tool cover -html=cover.out

clean:
	rm -rf bin cover.out
