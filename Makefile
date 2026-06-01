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

container-sandbox-server:
	@docker build -t sandbox-server -f cmd/sandbox-server/Dockerfile .

container-webhook-listener:
	@docker build -t webhook-listener -f cmd/webhook-listener/Dockerfile .

container-orchestrator:
	@docker build -t orchestrator -f cmd/orchestrator/Dockerfile .

container-agent-pi:
	@docker build -t agent-pi -f sandboxes/agents/pi/Dockerfile .

container-agent-opencode:
	@docker build -t agent-opencode -f sandboxes/agents/opencode/Dockerfile .
