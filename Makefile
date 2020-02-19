.PHONY: test deps docs
.EXPORT_ALL_VARIABLES:

GO111MODULE ?= on
LOCALS      := $(shell find . -type f -name '*.go')
BIN         ?= bin/flak-$(shell go env GOOS)-$(shell go env GOARCH)

all: deps fmt test build

deps:
	go get ./...
	-go mod tidy

fmt:
	go generate -x ./...
	gofmt -w $(LOCALS)
	go vet ./...

test:
	go test -count=1 ./...

build: fmt
	go build --ldflags '-extldflags "-static"' -installsuffix cgo -ldflags '-s' -o $(BIN) *.go
	which flak && cp -v $(BIN) $(shell which flak) || cp -v $(BIN) ~/bin/