# Build the manager binary
FROM golang:1.22 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go module manifests and download dependencies first so this layer
# is cached unless go.mod/go.sum change.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the source
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Build a static binary. CGO is disabled so the result runs on a distroless base.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -a -o manager cmd/main.go

# Use distroless as the minimal runtime base image.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
