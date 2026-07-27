# DataHub MCP server image. Installs the published mcp-server-datahub package
# from PyPI at build time (this needs network, once — like a docker pull).
# After `kind load`, the image is cached in the node and cluster bring-up stays
# offline.
#
# Base is the glibc (bookworm-slim) uv image, NOT alpine: the acryl-datahub /
# google-re2 dependency stack has manylinux (glibc) wheels but not musllinux,
# so alpine would fall back to compiling C/C++ from source. glibc installs with
# no build toolchain.
#
# The server is FastMCP-based and reads its HTTP bind config from FASTMCP_*
# settings (verified on fastmcp 3.4.4): FASTMCP_HOST/PORT/STREAMABLE_HTTP_PATH.
# Only the host needs changing from FastMCP's 127.0.0.1 default so the node port
# can reach it; port 8000 and path /mcp are already the defaults.
#
# MCP_SERVER_DATAHUB_SPEC pins the package; leave it as the bare name for the
# latest release (matching DataHub's documented `mcp-server-datahub@latest`),
# or override with "mcp-server-datahub==X.Y.Z" for a reproducible build.
FROM astral/uv:python3.13-bookworm-slim

WORKDIR /app

# Install into a venv on PATH so uv targets it and the console script resolves.
ENV VIRTUAL_ENV=/opt/venv \
    PATH="/opt/venv/bin:$PATH"
RUN uv venv "$VIRTUAL_ENV"

ARG MCP_SERVER_DATAHUB_SPEC=mcp-server-datahub
RUN uv pip install --no-cache "${MCP_SERVER_DATAHUB_SPEC}"

ENV FASTMCP_HOST=0.0.0.0 \
    FASTMCP_PORT=8000 \
    FASTMCP_STREAMABLE_HTTP_PATH=/mcp

EXPOSE 8000

ENTRYPOINT ["mcp-server-datahub", "--transport", "http"]
