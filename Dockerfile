FROM golang:1.21-alpine AS builder

# Install dependencies
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o minerva ./cmd/minerva

FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates sqlite

WORKDIR /root/

# Copy the binary from builder
COPY --from=builder /app/minerva .

# Create data directory
RUN mkdir -p /data

# Set environment variables
ENV DATABASE_PATH=/data/minerva.db

# Expose any necessary ports (if needed for health checks)
EXPOSE 8080

# Run the application
CMD ["./minerva"]