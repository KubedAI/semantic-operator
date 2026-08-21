# Multi-stage build producing two runtime images from one build:
#   --target manager  the operator controller-manager
#   --target server   the semantic-server (planner + MCP + REST)

FROM golang:1.26 AS deps
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM deps AS manager-build
COPY api/ ./api/
COPY internal/ ./internal/
COPY controllers/ ./controllers/
COPY cmd/manager/ ./cmd/manager/
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static:nonroot AS manager
WORKDIR /
COPY --from=manager-build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

FROM deps AS server-build
COPY api/ ./api/
COPY internal/ ./internal/
COPY cmd/server/ ./cmd/server/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server

FROM gcr.io/distroless/static:nonroot AS server
WORKDIR /
COPY --from=server-build /out/server /server
USER 65532:65532
ENTRYPOINT ["/server"]
