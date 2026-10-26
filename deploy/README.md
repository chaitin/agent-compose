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

# Select a release, directory, registry, supported UI version, or guest image.
sudo /opt/agent-compose/installer install \
  --version v1.2.3 \
  --dir /srv/agent-compose \
  --registry registry.example.com \
  --with-ui \
  --frontend-version v2 \
  --port 8080 \
  --guest-image registry.example.com/team/agent-compose-guest:v1.2.3 \
  --yes

# Skip the guest image pre-pull; the first sandbox downloads it instead.
sudo /opt/agent-compose/installer install --skip-guest-pull --yes

# Update installer-managed image references to the latest application release.
sudo /opt/agent-compose/installer upgrade --yes

# Prepare and validate files without pulling images or starting services.
sudo /opt/agent-compose/installer install --no-start --yes
```

The legacy top-level form, including `--upgrade`, `--dir`, `--version`,
`--image-prefix`, `--backend-image`, `--frontend-image`, `--no-start`, and
`--yes`, remains accepted for existing automation. These compatibility image
flags are hidden from help. New automation should use `--registry`,
`--frontend-version`, `--guest-image`, and the explicit `install`, `upgrade`,
and `uninstall` subcommands. `--registry` and `--image-prefix` are mutually
exclusive.

Environment overrides retained for automation are:

- `AGENT_COMPOSE_REPO`: GitHub repository used for downloads;
- `AGENT_COMPOSE_INSTALL_DIR`: default installation directory;
- `AGENT_COMPOSE_FRONTEND_VERSION`: supported frontend tag selected by legacy
  automation;
- `AGENT_COMPOSE_YES=1`: skip confirmation;
- `AGENT_COMPOSE_INSTALLER_RELEASE`: bootstrap release tag, primarily for
  mirrors and release verification.
- `AGENT_COMPOSE_INSTALLER_BASE_URL`: complete bootstrap asset base URL for a
  mirror or controlled test release.
- `AGENT_COMPOSE_RELEASE_BASE_URL`: complete application bundle base URL for a
  mirror or controlled test release.

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

The interactive form accepts one optional registry host. It replaces only the
registry component of all Release-managed references and preserves the full
repository path, tag, or digest. For example, `chaitin/agent-compose:v1`
becomes `registry.example.com/chaitin/agent-compose:v1`. This is not a Docker
registry mirror: transparent mirrors require separate dockerd configuration
and do not change image references.
When the field is empty, the TUI displays Docker Hub (`docker.io`) as a hint
but leaves the Release image references unchanged.

The Release manifest declares a default frontend version and an ordered list
of supported versions. The TUI presents that list beside the Web UI toggle;
`--frontend-version` provides the same validated selection for automation. An
upgrade preserves the selected version while the new Release still supports
it and stops for an explicit choice otherwise. Releases without a list expose
their default as the only choice.

The sandbox guest image remains independently configurable with
`--guest-image`. Hidden compatibility options can still override complete
backend and frontend references, and a complete reference wins over Registry
or version-derived values. The legacy `--image-prefix` retains its old
namespace-prefix behavior. Installer choices are recorded in
`.installer-state.env` so later upgrades preserve them.

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

The installer also writes `docker-compose.sandbox-network.yml` into the install
directory so Docker-mode deployments can opt into sandbox network isolation.
It is never selected by default: enable it by appending the overlay to
`COMPOSE_FILE` in `.env` (for example
`docker-compose.yml:docker-compose.sandbox-network.yml`) and running
`docker compose up -d`. It is Docker-only and cannot be combined with
`docker-compose.kvm.yml`. The overlay is embedded in the installer rather than
bundled, so a deployment upgrading with a retained installer that predates the
overlay still upgrades in place; it just does not receive the overlay file, so
opting in on such a deployment requires rerunning the bootstrap or copying the
overlay in by hand.

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

The base installation starts the daemon. The web UI stays in the optional
`with-ui` profile unless the installer was told to include it (`--with-ui`, or
the **Install web UI** form field), which persists `COMPOSE_PROFILES=with-ui`
in `.env`. To enable it afterwards:

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

It also mounts the Linux host's `/etc/localtime` read-only so daemon-local cron
schedules follow the host timezone. Set `TZ` in `.env` only when the daemon
should intentionally use another timezone, then restart the daemon.

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
The release workflow reads the optional repository variables
`AGENT_COMPOSE_FRONTEND_VERSION` and `AGENT_COMPOSE_FRONTEND_VERSIONS`; when
unset, both default to the single `latest` frontend choice.

## Contributor verification

```bash
task test:deploy
task test:scripts
task lint
```

The deterministic installer checks use fake command/network boundaries and do
not require a running Docker daemon, KVM, network access, or runtime sandboxes.

For an isolated real-Docker demonstration, including local HTTP releases, a
local OCI registry, install, upgrade, uninstall with data preservation, and
reinstall, run:

```bash
task demo:installer-docker
```

The command prints a state file and leaves the final v2 container, registry,
installation directory, and logs running for manual inspection. Use the
printed `cleanup-installer-docker-demo.sh` command when finished. Sourcing the
state file exports the local installer/application Release URLs and the demo
installation directory. It also exports a unique Compose project name so
retained demos can run side by side and the ordinary bootstrap can be rerun
directly.
