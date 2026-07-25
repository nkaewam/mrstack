.PHONY: build test test-race test-all lint

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o bin/mrstack ./cmd/mrstack

test:
	go test -shuffle=on -count=1 ./...

test-race:
	go test -race -shuffle=on -count=1 ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...

test-all: lint test test-race build
