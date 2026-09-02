# Security Policy

agent-compose is an experimental project. Please review its security model
before exposing it outside a local development environment.

## Reporting Vulnerabilities

Please report suspected vulnerabilities privately. Do not open a public issue
with exploit details, credentials, tokens, or private infrastructure data.

Use a private GitHub security advisory for this repository:
https://github.com/chaitin/agent-compose/security/advisories/new

Include:

- Affected version or commit.
- Configuration and runtime driver in use.
- Reproduction steps.
- Impact assessment.
- Any logs or traces with secrets redacted.

## Supported Versions

The public release is a preview. Security fixes are expected to target the main
development branch until versioned release support is documented.

## Deployment Guidance

- Expose browser access through the agent-compose-ui server, not directly
  through the daemon.
- Set `AUTH_PASSWORD` and a stable, high-entropy `AUTH_SECRET` for the UI server
  before exposing the Web UI to other users.
- Terminate HTTPS before the UI server in production-like deployments.
- Non-loopback `HTTP_LISTEN` requires `AGENT_COMPOSE_AUTH_TOKEN`,
  `HTTP_TLS_CERT_FILE`, and `HTTP_TLS_KEY_FILE`; the daemon refuses to start an
  exposed plaintext h2c listener. Keep the TLS private key readable only by the
  daemon account and use a high-entropy token.
- Do not expose guest Jupyter ports directly. Use the agent-compose proxy.
- Treat workspace uploads, Git credentials, environment variables, webhook
  tokens, and LLM API keys as secrets.
- Review runtime driver network behavior before running untrusted workloads.

## Runtime Isolation

agent-compose can run guest workloads with Docker, BoxLite, or Microsandbox.
Isolation properties vary by driver and host configuration. Do not assume a
driver is suitable for hostile code without a separate threat model and runtime
hardening review.

## Webhooks

Webhook endpoints intentionally do not rely on browser UI session cookies.
Configure webhook source tokens or signatures, keep body limits enabled, and
rotate tokens when they may have been exposed.
