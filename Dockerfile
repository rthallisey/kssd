FROM gcr.io/distroless/static
COPY bin/kssd-driver /kssd-driver
ENTRYPOINT ["/kssd-driver"]
