ARG BASE_IMAGE=ghcr.io/chaitin/agent-compose-guest:latest
FROM ${BASE_IMAGE}
ARG EXAMPLE_MARKER=built-by-agent-compose
RUN printf '%s\n' "${EXAMPLE_MARKER}" > /opt/agent-compose-example.txt
