.PHONY: all build test race vet fmt cover run loadtest clean

all: vet test build

build:
	go build -o bin/server ./cmd/server
	go build -o bin/loadtest ./cmd/loadtest

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

run:
	go run ./cmd/server

# Example: make loadtest ARGS="-n 500 -dur 15s"
loadtest:
	go run ./cmd/loadtest $(ARGS)

clean:
	rm -rf bin coverage.out
