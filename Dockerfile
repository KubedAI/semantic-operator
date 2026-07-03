# Multi-stage build producing two runtime images from one build:
#   --target manager  the operator controller-manager
#   --target server   the semantic-server (planner + MCP + REST)

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /out/manager ./cmd/manager \
 && CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server

FROM gcr.io/distroless/static:nonroot AS manager
WORKDIR /
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]

FROM gcr.io/distroless/static:nonroot AS server
WORKDIR /
COPY --from=build /out/server /server
USER 65532:65532
ENTRYPOINT ["/server"]
