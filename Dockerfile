# syntax=docker/dockerfile:1
#
# Container image of the mneme-mcp-stdio workbench server. The MCP client
# starts it itself and talks over stdin/stdout:
#
#   docker run -i --rm ghcr.io/fragshq/mneme-mcp-stdio --storage memory
#
# To build it yourself:
#
#   docker build --build-arg VERSION=dev -t mneme-mcp-stdio .

# Go cross-compiles for the target platform, so multi-arch builds need no
# emulation.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
ENV CGO_ENABLED=0
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION#v}" \
      -o /out/mneme-mcp-stdio .

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="mneme-mcp-stdio" \
      org.opencontainers.image.description="Workbench MCP server (stdio) exposing Mneme over Redis, Postgres or memory" \
      org.opencontainers.image.source="https://github.com/fragshq/mneme-mcp-stdio"
COPY --from=build /out/mneme-mcp-stdio /mneme-mcp-stdio
# The optional dashboard; see --dashboard in the README.
EXPOSE 47080
ENTRYPOINT ["/mneme-mcp-stdio"]
