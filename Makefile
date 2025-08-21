BIN_MAIN=drd4
BIN_TUI=drd4-tui

.PHONY: build build-tui test vet fmt

build: build-main build-tui

build-main:
	go build -o $(BIN_MAIN) ./cmd

build-tui:
	go build -o $(BIN_TUI) ./cmd/tui

vet:
	go vet ./...

test:
	go test ./... -v

fmt:
	gofmt -w .
