# Prebuilt-binary images for the local demo. Fully offline: the static
# manager/server binaries are compiled on the host from the repo root (CGO off,
# using the host Go module cache) and copied into the cached distroless base,
# so this build never runs `go mod download`. Build context is the directory
# holding the two binaries. Mirrors the runtime shape of the repo root Dockerfile.
FROM gcr.io/distroless/static:nonroot AS manager
WORKDIR /
COPY manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

FROM gcr.io/distroless/static:nonroot AS server
WORKDIR /
COPY server /server
USER 65532:65532
ENTRYPOINT ["/server"]
