#!/usr/bin/env bash
#
# agent-compose installer
#
# Configures and starts the agent-compose docker-compose stack, pulling the
# multi-arch container images from the registry (ghcr.io by default; Docker
# selects the right architecture automatically).
#
# Run it from an extracted installer archive or via `curl ... | bash`. On first
# run it generates an admin password and prints it to the terminal.

set -euo pipefail

# --------------------------------------------------------------------------
# Defaults (override via flags or env)
# --------------------------------------------------------------------------
REPO="${AGENT_COMPOSE_REPO:-chaitin/agent-compose}"
INSTALL_DIR="${AGENT_COMPOSE_INSTALL_DIR:-./agent-compose}"
VERSION="latest"
FRONTEND_VERSION="${AGENT_COMPOSE_FRONTEND_VERSION:-latest}"
IMAGE_PREFIX=""
PORT=""
NO_START=0
UPGRADE=0
YES="${AGENT_COMPOSE_YES:-0}"

log()  { printf '\033[0;36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[0;33mwarn:\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[0;31merror:\033[0m %s\n' "$*" >&2; }
die()  { err "$@"; exit 1; }

usage() {
  cat <<'EOF'
agent-compose installer

Usage: install.sh [options]

Options:
  --dir <path>           Install directory (default: ./agent-compose)
  --port <port>          Host port for the web UI (default: 80)
  --version <vX.Y.Z>     Release version to install in remote mode (default: latest)
  --image-prefix <ref>   Pull images from this prefix (mirror / private registry)
                         instead of the default registry
  --upgrade              Update existing image refs to this installer version
  --no-start             Lay down files but do not pull images or start
  -y, --yes              Run without the interactive confirmation prompt
  -h, --help             Show this help

Environment:
  AGENT_COMPOSE_REPO          GitHub repo for downloads (owner/name)
  AGENT_COMPOSE_INSTALL_DIR   Same as --dir
  AGENT_COMPOSE_FRONTEND_VERSION
                              Frontend image tag to use with --image-prefix
                              when no bundle manifest is available (default: latest)
  AGENT_COMPOSE_YES=1         Same as --yes
EOF
}

# --------------------------------------------------------------------------
# Arg parsing
# --------------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --dir)          INSTALL_DIR="${2:?--dir needs a value}"; shift 2 ;;
    --port)         PORT="${2:?--port needs a value}"; shift 2 ;;
    --version)      VERSION="${2:?--version needs a value}"; shift 2 ;;
    --image-prefix) IMAGE_PREFIX="${2:?--image-prefix needs a value}"; shift 2 ;;
    --upgrade)      UPGRADE=1; shift ;;
    --no-start)     NO_START=1; shift ;;
    -y|--yes)       YES=1; shift ;;
    -h|--help)      usage; exit 0 ;;
    *)              die "unknown option: $1 (try --help)" ;;
  esac
done

[ "$(uname -s)" = "Linux" ] || warn "this installer targets Linux; '$(uname -s)' may not work for the docker-compose stack"

# --------------------------------------------------------------------------
# Secret generation / env helpers
# --------------------------------------------------------------------------
gen_hex() { # $1 = byte count
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$1"
  else
    head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'
  fi
}

gen_password() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | cut -c1-24
  else
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' | cut -c1-24
  fi
}

set_env() { # $1=file $2=key $3=value
  local file="$1" key="$2" value="$3"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    awk -v k="$key" -v v="$value" '
      BEGIN { FS=OFS="=" }
      $1 == k { print k "=" v; next }
      { print }
    ' "$file" > "${file}.tmp" && mv "${file}.tmp" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >> "$file"
  fi
}

get_env() { # $1=file $2=key -> value (may be empty)
  grep "^$2=" "$1" 2>/dev/null | head -n1 | cut -d= -f2- || true
}

truthy() {
  case "${1:-}" in
    1|yes|YES|true|TRUE|y|Y) return 0 ;;
    *) return 1 ;;
  esac
}

apply_image_refs() { # $1=file $2=mode(set-missing|overwrite)
  local file="$1" mode="$2" key value pair current
  if [ -n "$IMAGE_PREFIX" ]; then
    for pair in \
      "AGENT_COMPOSE_IMAGE=${IMAGE_PREFIX}/agent-compose:${IMAGE_VERSION}" \
      "AGENT_COMPOSE_FRONTEND_VERSION=${FRONTEND_VERSION}" \
      "AGENT_COMPOSE_FRONTEND_IMAGE=${IMAGE_PREFIX}/agent-compose-ui:${FRONTEND_VERSION}" \
      "DEFAULT_IMAGE=${IMAGE_PREFIX}/agent-compose-guest:${IMAGE_VERSION}"; do
      key="${pair%%=*}"
      value="${pair#*=}"
      current="$(get_env "$file" "$key")"
      if [ "$mode" = "overwrite" ] || [ -z "$current" ]; then
        set_env "$file" "$key" "$value"
        log "Set $key in $file"
      elif [ "$current" != "$value" ]; then
        warn "$key already set; keeping existing image ref"
      fi
    done
  elif [ -f "$MANIFEST" ]; then
    while IFS='=' read -r key value; do
      case "$key" in
        AGENT_COMPOSE_IMAGE|AGENT_COMPOSE_FRONTEND_VERSION|AGENT_COMPOSE_FRONTEND_IMAGE|DEFAULT_IMAGE)
          current="$(get_env "$file" "$key")"
          if [ "$mode" = "overwrite" ] || [ -z "$current" ]; then
            set_env "$file" "$key" "$value"
            log "Set $key in $file"
          elif [ "$current" != "$value" ]; then
            warn "$key already set; keeping existing image ref"
          fi
          ;;
      esac
    done < "$MANIFEST"
  fi
}

# --------------------------------------------------------------------------
# Locate bundle source (bundled vs remote)
# --------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || echo "")"
BUNDLE_SRC=""
TMP_DIR=""

cleanup() { if [ -n "$TMP_DIR" ]; then rm -rf "$TMP_DIR"; fi; }
trap cleanup EXIT

# images/manifest.env only exists in a real installer bundle, so it
# disambiguates from a source checkout that also has a docker-compose.yml.
if [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/docker-compose.yml" ] \
   && [ -f "$SCRIPT_DIR/nginx/nginx.conf" ] && [ -f "$SCRIPT_DIR/images/manifest.env" ]; then
  BUNDLE_SRC="$SCRIPT_DIR"
  log "Running from extracted bundle: $BUNDLE_SRC"
else
  command -v curl >/dev/null 2>&1 || die "curl is required for remote install"
  command -v tar  >/dev/null 2>&1 || die "tar is required for remote install"
  asset="agent-compose-installer.tar.gz"
  if [ "$VERSION" = "latest" ]; then
    url="https://github.com/${REPO}/releases/latest/download/${asset}"
  else
    url="https://github.com/${REPO}/releases/download/${VERSION}/${asset}"
  fi
  TMP_DIR="$(mktemp -d)"
  log "Downloading ${asset} (${VERSION})"
  curl -fsSL -o "$TMP_DIR/$asset" "$url" || die "download failed: $url"
  shasums_url="${url%/*}/SHASUMS256.txt"
  if curl -fsSL -o "$TMP_DIR/SHASUMS256.txt" "$shasums_url" 2>/dev/null; then
    if ( cd "$TMP_DIR" && awk -v a="$asset" '$2 == a || $2 == "./" a' SHASUMS256.txt | sha256sum -c - >/dev/null 2>&1 ); then
      log "Checksum OK"
    else
      die "checksum verification failed for $asset"
    fi
  else
    warn "checksum file unavailable; skipping verification"
  fi
  tar -xzf "$TMP_DIR/$asset" -C "$TMP_DIR"
  BUNDLE_SRC="$(find "$TMP_DIR" -maxdepth 2 -name docker-compose.yml -exec dirname {} \; | head -n1)"
  [ -n "$BUNDLE_SRC" ] || die "extracted bundle is missing docker-compose.yml"
fi

# --------------------------------------------------------------------------
# Prerequisites
# --------------------------------------------------------------------------
command -v docker >/dev/null 2>&1 || die "docker is not installed or not on PATH"
if docker compose version >/dev/null 2>&1; then
  COMPOSE="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE="docker-compose"
else
  die "docker compose (v2) is required"
fi
[ -e /dev/kvm ] || warn "/dev/kvm not present; the boxlite/microsandbox drivers need it (the default docker driver does not)"

# --------------------------------------------------------------------------
# Explicit confirmation before changing the installation directory
# --------------------------------------------------------------------------
if [ -f "$INSTALL_DIR/.env" ]; then
  ENV_STATE="existing .env will be preserved"
else
  ENV_STATE="new .env will be created with generated credentials"
fi
if [ "$UPGRADE" -eq 1 ]; then
  IMAGE_STATE="image refs in .env will be updated to this installer version"
else
  IMAGE_STATE="existing image refs in .env will be kept"
fi
if [ "$NO_START" -eq 1 ]; then
  START_STATE="stack will not be started (--no-start)"
else
  START_STATE="images will be pulled and the stack will be started"
fi

cat <<EOF

agent-compose deployment plan
  Source:       $BUNDLE_SRC
  Target dir:   $INSTALL_DIR
  Data dir:     $INSTALL_DIR/data/agent-compose
  Config:       $ENV_STATE
  Images:       $IMAGE_STATE
  Start:        $START_STATE

EOF

if ! truthy "$YES"; then
  if [ ! -r /dev/tty ]; then
    die "confirmation requires a TTY; rerun with --yes or AGENT_COMPOSE_YES=1"
  fi
  printf 'Continue? [y/N] ' > /dev/tty
  read -r answer < /dev/tty
  case "$answer" in
    y|Y|yes|YES) ;;
    *) die "installation cancelled" ;;
  esac
fi

# --------------------------------------------------------------------------
# Lay down files
# --------------------------------------------------------------------------
mkdir -p "$INSTALL_DIR/nginx" "$INSTALL_DIR/data/agent-compose"
cp "$BUNDLE_SRC/docker-compose.yml" "$INSTALL_DIR/docker-compose.yml"
cp "$BUNDLE_SRC/nginx/nginx.conf"   "$INSTALL_DIR/nginx/nginx.conf"
if [ -f "$BUNDLE_SRC/install.sh" ] && [ "$BUNDLE_SRC/install.sh" -ef "$INSTALL_DIR/install.sh" ] 2>/dev/null; then
  : # already the same file; nothing to copy
elif [ -f "$BUNDLE_SRC/install.sh" ]; then
  cp "$BUNDLE_SRC/install.sh" "$INSTALL_DIR/install.sh"
fi
INSTALL_DIR="$(cd "$INSTALL_DIR" && pwd)"
ENV_FILE="$INSTALL_DIR/.env"

# Manifest pins the compose image refs to this release's version.
MANIFEST="$BUNDLE_SRC/images/manifest.env"
IMAGE_VERSION="$VERSION"
if [ -f "$MANIFEST" ]; then
  ref="$(get_env "$MANIFEST" AGENT_COMPOSE_IMAGE)"
  [ -n "$ref" ] && IMAGE_VERSION="${ref##*:}"
fi

GENERATED_PASSWORD=""
if [ ! -f "$ENV_FILE" ]; then
  cp "$BUNDLE_SRC/.env.example" "$ENV_FILE"
  log "Created $ENV_FILE"

  set_env "$ENV_FILE" AUTH_SECRET "$(gen_hex 32)"

  GENERATED_PASSWORD="$(gen_password)"
  set_env "$ENV_FILE" AUTH_PASSWORD "$GENERATED_PASSWORD"

  apply_image_refs "$ENV_FILE" overwrite
  [ -n "$PORT" ] && set_env "$ENV_FILE" AGENT_COMPOSE_HTTP_PORT "$PORT"
else
  warn "$ENV_FILE already exists; preserving configured settings"
  if [ -z "$(get_env "$ENV_FILE" AUTH_SECRET)" ]; then
    set_env "$ENV_FILE" AUTH_SECRET "$(gen_hex 32)"
    log "Generated missing AUTH_SECRET in $ENV_FILE"
  fi
  if [ -z "$(get_env "$ENV_FILE" AUTH_PASSWORD)" ]; then
    GENERATED_PASSWORD="$(gen_password)"
    set_env "$ENV_FILE" AUTH_PASSWORD "$GENERATED_PASSWORD"
    log "Generated missing AUTH_PASSWORD in $ENV_FILE"
  fi
  if [ "$UPGRADE" -eq 1 ]; then
    apply_image_refs "$ENV_FILE" overwrite
  else
    apply_image_refs "$ENV_FILE" set-missing
  fi
  [ -n "$PORT" ] && set_env "$ENV_FILE" AGENT_COMPOSE_HTTP_PORT "$PORT"
fi

# --------------------------------------------------------------------------
# Start
# --------------------------------------------------------------------------
HTTP_PORT="$(get_env "$ENV_FILE" AGENT_COMPOSE_HTTP_PORT)"; HTTP_PORT="${HTTP_PORT:-80}"
USERNAME="$(get_env "$ENV_FILE" AUTH_USERNAME)"; USERNAME="${USERNAME:-admin}"

if [ "$NO_START" -eq 1 ]; then
  log "Skipping start (--no-start). Run later with: cd $INSTALL_DIR && $COMPOSE up -d"
else
  log "Pulling images and starting the stack"
  ( cd "$INSTALL_DIR" && $COMPOSE pull && $COMPOSE up -d )
fi

# --------------------------------------------------------------------------
# Summary
# --------------------------------------------------------------------------
printf '\n'
printf '\033[0;32m================ agent-compose is ready ================\033[0m\n'
printf '  URL:        http://localhost:%s\n' "$HTTP_PORT"
printf '  Directory:  %s\n' "$INSTALL_DIR"
if [ -n "$GENERATED_PASSWORD" ]; then
  printf '\n  \033[1mLogin credentials (generated, shown only once):\033[0m\n'
  printf '    Username: %s\n' "$USERNAME"
  printf '    Password: \033[1m%s\033[0m\n' "$GENERATED_PASSWORD"
  printf '  Stored in %s (key AUTH_PASSWORD).\n' "$ENV_FILE"
else
  printf '\n  Credentials unchanged; see AUTH_PASSWORD in %s.\n' "$ENV_FILE"
fi
printf '\n  Manage: cd %s && %s [ps|logs -f|down]\n' "$INSTALL_DIR" "$COMPOSE"
printf '\033[0;32m========================================================\033[0m\n'
