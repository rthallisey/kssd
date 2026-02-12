FROM gcr.io/distroless/static
COPY bin/drain-driver /drain-driver
ENTRYPOINT ["/drain-driver"]
