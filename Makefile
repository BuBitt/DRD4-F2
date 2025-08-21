BINARY=drd4

.PHONY: build test vet fmt

build:
	go build -o $(BINARY) ./cmd

vet:
	go vet ./...

test:
	go test ./... -v

fmt:
	gofmt -w .
