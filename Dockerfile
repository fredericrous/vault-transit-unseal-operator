# Build stage
FROM golang:1.25.1-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git

WORKDIR /workspace

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go .
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build with proper flags for smaller binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} \
    go build -a -trimpath -ldflags="-w -s" -o manager main.go

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]