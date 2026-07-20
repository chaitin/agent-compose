# agent-compose installer

The installer deploys agent-compose with Docker Compose on Linux amd64 and
arm64. A small Bash bootstrap downloads the matching Go binary; the Go program
owns installation, upgrade, uninstall, validation, and rollback.

## Quick start

Interactive installation opens a bilingual TUI:

```bash
curl -fsSL https://github.com/chaitin/agent-compose/releases/download/installer-latest/install.sh | bash
```

The default installation directory is `/opt/agent-compose`. Run the bootstrap
with `sudo` when the current user cannot write there. The installer is retained
as `/opt/agent-compose/installer`, so later operations can run directly:

```bash
sudo /opt/agent-compose/installer upgrade
sudo /opt/agent-compose/installer uninstall
```

Docker Engine and the Docker Compose v2 plugin must already be installed. The
installer detects missing prerequisites and prints guidance; it does not modify
the host package manager or install Docker automatically.

## Non-interactive CLI

The bootstrap forwards all arguments to the downloaded binary:

```bash
# Install the latest application release.
curl -fsSL https://github.com/chaitin/agent-compose/releases/download/installer-latest/install.sh | \
  sudo bash -s -- install --yes

# Select a release, directory, UI port, or image mirror.
sudo /opt/agent-compose/installer install \
  --version v1.2.3 \
  --dir /srv/agent-compose \
  --port 8080 \
  --image-prefix registry.example.com/agent-compose \
  --yes

# Update installer-managed image references to the latest application release.
sudo /opt/agent-compose/installer upgrade --yes

# Prepare and validate files without pulling images or starting services.
sudo /opt/agent-compose/installer install --no-start --yes
```

The legacy top-level form, including `--upgrade`, `--dir`, `--version`,
`--image-prefix`, `--no-start`, and `--yes`, remains accepted. New automation
should use the explicit `install`, `upgrade`, and `uninstall` subcommands.

Environment overrides retained for automation are:

- `AGENT_COMPOSE_REPO`: GitHub repository used for downloads;
- `AGENT_COMPOSE_INSTALL_DIR`: default installation directory;
- `AGENT_COMPOSE_FRONTEND_VERSION`: frontend tag used with an image prefix;
- `AGENT_COMPOSE_YES=1`: skip confirmation;
- `AGENT_COMPOSE_INSTALLER_RELEASE`: bootstrap release tag, primarily for
  mirrors and release verification.

## Installation and upgrade behavior

The installer downloads `agent-compose-installer.tar.gz` and its checksum from
the selected application Release. It requires a matching SHA-256 entry and
accepts only the expected regular files from the archive.

Before changing the target, it validates paths, rejects symlinked managed
targets, creates candidate configuration in a temporary directory, and prints
the plan in interactive mode. Managed files are replaced atomically. If Compose
validation, image pulling, or startup fails, the previous files and modes are
restored; an existing deployment is restarted after restoration.

On first installation the installer generates `AUTH_SECRET` and an `admin`
password. The password is printed once and stored in `.env`. Existing settings
are preserved. During upgrade, image references advance only when their current
value still matches `.installer-state.env`; user overrides are never replaced.

New installations persist `AGENT_COMPOSE_DATA_DIR=./data`. If an older database
exists only under `./data/agent-compose`, that path is retained. When databases
exist in both layouts, installation stops until the operator sets
`AGENT_COMPOSE_DATA_DIR` to the authoritative location.

The installer checks `/dev/kvm` only on first selection:

- without KVM, `COMPOSE_FILE=docker-compose.yml` is persisted;
- with KVM, `COMPOSE_FILE=docker-compose.yml:docker-compose.kvm.yml` is
  persisted.

An existing explicit `COMPOSE_FILE` is preserved. This chooses deployment
topology; it does not prove KVM permissions or BoxLite/Microsandbox health.

## Uninstall

Ordinary uninstall stops the Compose project and removes installer-managed
Compose files, state, and the retained installer binary. It deliberately keeps
`.env` and persistent `data` so a later installation can recover them:

```bash
sudo /opt/agent-compose/installer uninstall
```

Permanent removal requires the explicit purge option and confirmation:

```bash
sudo /opt/agent-compose/installer uninstall --purge
```

Purge removes only recognized installer configuration and data. Unknown files
keep the installation directory in place and are reported as leftovers. Neither
uninstall form removes shared Docker image caches or Compose volumes.

## Operating the deployment

The base installation starts the daemon. The web UI remains in the optional
`with-ui` profile:

```bash
cd /opt/agent-compose
docker compose --profile with-ui up -d
docker compose ps
docker compose logs -f
docker compose down
```

The base topology mounts the Docker socket without privilege or KVM. Use the
persisted KVM overlay only on a prepared host when selecting BoxLite or
Microsandbox.

## Release model

The fixed `installer-latest` prerelease contains:

| Asset | Purpose |
| --- | --- |
| `install.sh` | Linux OS/architecture bootstrap |
| `agent-compose-installer-linux-amd64` | amd64 Go installer |
| `agent-compose-installer-linux-arm64` | arm64 Go installer |
| `SHASUMS256.txt` | installer binary checksums |

It is updated only by manually dispatching the `Publish Installer` workflow.
Because the release is marked prerelease, it does not replace the latest normal
application Release.

Normal application releases contain the architecture-independent deployment
bundle, bootstrap copy, and bundle checksum. The installer binary need not be
rebuilt for ordinary application releases unless the payload protocol changes.

## Contributor verification

```bash
task test:deploy
task test:scripts
task lint
```

The deterministic installer checks use fake command/network boundaries and do
not require a running Docker daemon, KVM, network access, or runtime sandboxes.
