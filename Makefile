all: deps fmt build lint test

verify: build lint test

lint:
	@echo "Linting code..."
	golangci-lint run

build: clean
	@echo "Building binaries..."
	@mkdir -p bin
	go build -o bin/webhook-listener ./cmd/webhook-listener
	go build -o bin/orchestrator ./cmd/orchestrator
	go build -o bin/sandbox-server ./cmd/sandbox-server

test:
	@echo "Running tests..."
	go test -v ./...

deps:
	go mod tidy

fmt:
	@echo "Formatting code..."
	go fmt ./...

clean:
	@echo "Cleaning up..."
	rm -rf bin
