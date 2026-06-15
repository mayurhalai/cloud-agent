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

containers: containers-pi
containers-pi: clean container-sandbox-server container-webhook-listener container-orchestrator container-agent-pi container-agent-pi-golang
containers-opencode: clean container-sandbox-server container-webhook-listener container-orchestrator container-agent-opencode container-agent-opencode-golang

container-sandbox-server:
	@docker build -t sandbox-server -f cmd/sandbox-server/Dockerfile .

container-webhook-listener:
	@docker build -t webhook-listener -f cmd/webhook-listener/Dockerfile .
	@kind load docker-image webhook-listener:latest --name desktop

container-orchestrator:
	@docker build -t orchestrator -f cmd/orchestrator/Dockerfile .
	@kind load docker-image orchestrator:latest --name desktop

container-agent-pi:
	@docker build -t agent-pi -f sandboxes/agents/pi/Dockerfile .
	@kind load docker-image agent-pi:latest --name desktop

container-agent-opencode:
	@docker build -t agent-opencode -f sandboxes/agents/opencode/Dockerfile .
	@kind load docker-image agent-opencode:latest --name desktop

container-agent-pi-golang:
	@docker build -t agent-pi-golang -f sandboxes/golang/pi/Dockerfile .
	@kind load docker-image agent-pi-golang:latest --name desktop

container-agent-opencode-golang:
	@docker build -t agent-opencode-golang -f sandboxes/golang/opencode/Dockerfile .
	@kind load docker-image agent-opencode-golang:latest --name desktop
