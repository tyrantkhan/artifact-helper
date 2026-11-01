.PHONY: build install test lint clean setup

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
LDFLAGS := -ldflags "-X github.com/tyrantkhan/claude-artifact-helper/internal/cli.Version=$(VERSION)"

setup:
	mise install
	lefthook install

build:
	go build $(LDFLAGS) -o bin/claude-artifact-helper ./cmd/claude-artifact-helper

install:
	go install $(LDFLAGS) ./cmd/claude-artifact-helper

test:
	go test ./...

lint:
	golangci-lint run

clean:
	rm -rf bin
