FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux make build

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /workspace/bin/drain-driver /drain-driver
USER 65532:65532
ENTRYPOINT ["/drain-driver"]
