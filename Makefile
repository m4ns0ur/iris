.PHONY: help lint build test coverage

default: lint build

all: lint build test coverage

help:
	@grep -E '^##' Makefile | sed 's/^## //'

## lint - Run linters (goimports, go vet, golangci-lint)
lint:
	goimports -w .
	go vet ./...
	golangci-lint run

## build - Clean, compile and install
build:
	go clean
	rm -rf ./bin
	go mod tidy
	mkdir -p ./bin
	go build -o ./bin/iris -ldflags="-s -w" \
		-tags netgo -trimpath .
	go install .

## test - Run all tests (with race detection)
test:
	go test -race -short ./...

## coverage - Run tests with coverage report (to ./coverage.out)
coverage:
	mkdir -p output
	COVERAGE_MODE=count go test -covermode=count \
		-coverprofile=./output/coverage.out \
		./...
