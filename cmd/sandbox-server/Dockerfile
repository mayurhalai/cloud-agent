# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build the sandbox-server binary
RUN CGO_ENABLED=0 GOOS=linux go build -o sandbox-server ./cmd/sandbox-server

# Runtime stage
FROM node:26-trixie-slim

# Copy the compiled binary
COPY --from=builder /app/sandbox-server /bin/sandbox-server

ONBUILD EXPOSE 8888
ONBUILD ENTRYPOINT ["/bin/sandbox-server"]
