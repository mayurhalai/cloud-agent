# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the webhook-listener binary
RUN CGO_ENABLED=0 GOOS=linux go build -o webhook-listener ./cmd/webhook-listener

# Runtime stage
FROM debian:trixie-slim

WORKDIR /app

RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*

# Copy the compiled binary
COPY --from=builder /app/webhook-listener /app/webhook-listener

# Expose the port
EXPOSE 8080

ENTRYPOINT [ "/app/webhook-listener" ]
