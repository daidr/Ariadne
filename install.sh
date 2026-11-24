#!/bin/sh
# Ariadne cross-platform installer for macOS and Linux (POSIX sh).
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/daidr/Ariadne/master/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/daidr/Ariadne/master/install.sh | sh -s -- --no-service

set -eu

REPO="${ARIADNE_REPO:-daidr/Ariadne}"
API_BASE="${ARIADNE_GITHUB_API_URL:-https://api.github.com}"
CHANNEL="${ARIADNE_CHANNEL:-}"
VERSION="${ARIADNE_VERSION:-}"
INSTALL_DIR="${ARIADNE_INSTALL_DIR:-$HOME/.local/bin}"
NO_ALIAS="${ARIADNE_NO_ALIAS:-0}"
NO_SERVICE="${ARIADNE_NO_SERVICE:-0}"

say() {
  printf '%s\n' "$*"
}

warn() {
  printf 'Warning: %s\n' "$*" >&2
}

die() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Install Ariadne globally and optionally register its background service.

Usage:
  install.sh [options]

Options:
  --channel <stable|preview>  Release channel (default: stable, falling back to preview)
  --version <ver>             Install a specific release version (e.g. v0.1.0-alpha.15)
  --dir <path>                Installation directory (default: ~/.local/bin)
  --no-alias                  Do not create the short 'ard' alias
  --no-service                Only install the CLI; do not register or start the service
  -h, --help                  Show this help
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --channel)
      [ "$#" -ge 2 ] || die "--channel requires a value"
      CHANNEL="$2"
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a value"
      VERSION="$2"
      shift 2
      ;;
    --dir)
      [ "$#" -ge 2 ] || die "--dir requires a value"
      INSTALL_DIR="$2"
      shift 2
      ;;
    --no-alias)
      NO_ALIAS=1
      shift
      ;;
    --no-service)
      NO_SERVICE=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "Unknown option: $1 (run with --help)"
      ;;
  esac
done

if [ -n "$CHANNEL" ] && [ "$CHANNEL" != "stable" ] && [ "$CHANNEL" != "preview" ]; then
  die "Invalid --channel '$CHANNEL' (expected 'stable' or 'preview')"
fi

detect_platform() {
  os="$(uname -s 2>/dev/null || printf 'unknown')"
  arch="$(uname -m 2>/dev/null || printf 'unknown')"

  case "$os" in
    Linux)  os_name="linux" ;;
    Darwin) os_name="darwin" ;;
    *)      die "Unsupported operating system: $os" ;;
  esac

  case "$arch" in
    x86_64|amd64)   arch_name="x64" ;;
    arm64|aarch64)  arch_name="arm64" ;;
    *)              die "Unsupported CPU architecture: $arch (supported: x64, arm64)" ;;
  esac

  ASSET_NAME="ariadne-${os_name}-${arch_name}"
}

need_downloader() {
  if command -v curl >/dev/null 2>&1; then
    DOWNLOADER="curl"
  elif command -v wget >/dev/null 2>&1; then
    DOWNLOADER="wget"
  else
    die "Either curl or wget is required to download Ariadne"
  fi
}

http_api_get() {
  url="$1"
  out="$2"
  token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  if [ "$DOWNLOADER" = "curl" ]; then
    if [ -n "$token" ]; then
      curl -fsSL \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: ariadne-installer" \
        -H "Authorization: Bearer $token" \
        "$url" -o "$out"
    else
      curl -fsSL \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: ariadne-installer" \
        "$url" -o "$out"
    fi
  else
    if [ -n "$token" ]; then
      wget -q \
        --header="Accept: application/vnd.github+json" \
        --header="User-Agent: ariadne-installer" \
        --header="Authorization: Bearer $token" \
        "$url" -O "$out"
    else
      wget -q \
        --header="Accept: application/vnd.github+json" \
        --header="User-Agent: ariadne-installer" \
        "$url" -O "$out"
    fi
  fi
}

# Download public release assets without Authorization headers so credentials
# are never forwarded across cross-origin redirects to object storage.
http_download() {
  url="$1"
  out="$2"
  if [ "$DOWNLOADER" = "curl" ]; then
    curl -fsSL \
      -H "User-Agent: ariadne-installer" \
      "$url" -o "$out"
  else
    wget -q \
      --header="User-Agent: ariadne-installer" \
      "$url" -O "$out"
  fi
}

# Parses GitHub Releases API JSON and prints 4 lines:
#   tag_name
#   prerelease (true|false)
#   browser_download_url
#   sha256_digest (hex without 'sha256:' prefix, or empty)
resolve_release() {
  json_file="$1"
  mode="$2" # tag | stable | preview | auto
  asset="$3"

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$json_file" "$mode" "$asset" <<'PY'
import json, sys

path, mode, asset_name = sys.argv[1], sys.argv[2], sys.argv[3]
with open(path, "r", encoding="utf-8") as f:
    data = json.load(f)

releases = [data] if isinstance(data, dict) else list(data)

def pick(allow_prerelease):
    for rel in releases:
        if rel.get("draft"):
            continue
        if not allow_prerelease and rel.get("prerelease"):
            continue
        for asset in rel.get("assets", []):
            if asset.get("name") == asset_name:
                digest = asset.get("digest") or ""
                if digest.startswith("sha256:"):
                    digest = digest[len("sha256:"):]
                print(rel.get("tag_name", ""))
                print("true" if rel.get("prerelease") else "false")
                print(asset.get("browser_download_url", ""))
                print(digest)
                return True
    return False

if mode == "tag":
    ok = pick(True)
elif mode == "stable":
    ok = pick(False)
elif mode == "preview":
    ok = pick(True)
else:
    ok = pick(False) or pick(True)

sys.exit(0 if ok else 1)
PY
    return $?
  fi

  if command -v node >/dev/null 2>&1 || command -v bun >/dev/null 2>&1; then
    js_bin="node"
    command -v node >/dev/null 2>&1 || js_bin="bun"
    "$js_bin" -e '
const fs = require("fs");
const [path, mode, assetName] = process.argv.slice(1);
const data = JSON.parse(fs.readFileSync(path, "utf8"));
const releases = Array.isArray(data) ? data : [data];
function pick(allowPre) {
  for (const rel of releases) {
    if (rel.draft) continue;
    if (!allowPre && rel.prerelease) continue;
    for (const a of rel.assets || []) {
      if (a.name === assetName) {
        let d = a.digest || "";
        if (d.startsWith("sha256:")) d = d.slice(7);
        console.log(rel.tag_name || "");
        console.log(rel.prerelease ? "true" : "false");
        console.log(a.browser_download_url || "");
        console.log(d);
        return true;
      }
    }
  }
  return false;
}
const ok = mode === "stable" ? pick(false) : (mode === "tag" || mode === "preview") ? pick(true) : (pick(false) || pick(true));
process.exit(ok ? 0 : 1);
' "$json_file" "$mode" "$asset"
    return $?
  fi

  # Minimal POSIX awk fallback for environments without python3/node/bun.
  # Works on GitHub API pretty-printed release JSON.
  awk -v asset="$asset" -v mode="$mode" '
    function unquote(s) {
      sub(/^[^:]*:[ \t]*/, "", s)
      sub(/,[ \t]*$/, "", s)
      sub(/^"/, "", s)
      sub(/"$/, "", s)
      return s
    }
    function emit(t, p, u, d) {
      print t
      print p
      print u
      print d
      emitted = 1
      exit 0
    }
    function consider() {
      if (!in_rel || found_url == "" || draft == "true") {
        return
      }
      if (mode == "stable") {
        if (pre != "true") {
          emit(tag, pre, found_url, found_digest)
        }
        return
      }
      if (mode == "tag" || mode == "preview") {
        emit(tag, pre, found_url, found_digest)
      }
      if (mode == "auto") {
        if (pre != "true") {
          emit(tag, pre, found_url, found_digest)
        } else if (!has_preview) {
          has_preview = 1
          preview_tag = tag
          preview_pre = pre
          preview_url = found_url
          preview_digest = found_digest
        }
      }
    }
    /"tag_name"[ \t]*:/ {
      consider()
      in_rel = 1; tag = unquote($0); draft = "false"; pre = "false"; cur_name = ""; cur_digest = ""; found_url = ""; found_digest = ""
    }
    /"draft"[ \t]*:/ { draft = unquote($0) }
    /"prerelease"[ \t]*:/ { pre = unquote($0) }
    /"name"[ \t]*:/ { cur_name = unquote($0); cur_digest = "" }
    /"digest"[ \t]*:/ {
      d = unquote($0)
      sub(/^sha256:/, "", d)
      cur_digest = d
    }
    /"browser_download_url"[ \t]*:/ {
      u = unquote($0)
      if (cur_name == asset) {
        found_url = u
        found_digest = cur_digest
      }
    }
    END {
      if (emitted) {
        exit 0
      }
      consider()
      if (mode == "auto" && has_preview) {
        emit(preview_tag, preview_pre, preview_url, preview_digest)
      }
      exit 1
    }
  ' "$json_file"
}

compute_sha256() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$file" | awk '{print $NF}'
  else
    printf ''
  fi
}

append_line_once() {
  profile="$1"
  line="$2"
  if [ -f "$profile" ] && grep -F "$line" "$profile" >/dev/null 2>&1; then
    return 0
  fi
  if ! mkdir -p "$(dirname "$profile")" 2>/dev/null || ! printf '\n# Ariadne\n%s\n' "$line" >> "$profile" 2>/dev/null; then
    warn "Could not update $profile; add '$INSTALL_DIR' to your PATH manually."
    return 0
  fi
  PATH_UPDATED_FILE="$profile"
}

ensure_path() {
  case ":$PATH:" in
    *":$INSTALL_DIR:"*)
      return 0
      ;;
  esac

  shell_name="$(basename "${SHELL:-sh}")"
  export_line="export PATH=\"$INSTALL_DIR:\$PATH\""
  PATH_UPDATED_FILE=""

  case "$shell_name" in
    zsh)
      append_line_once "$HOME/.zshrc" "$export_line"
      ;;
    bash)
      if [ -f "$HOME/.bashrc" ]; then
        append_line_once "$HOME/.bashrc" "$export_line"
      elif [ -f "$HOME/.bash_profile" ]; then
        append_line_once "$HOME/.bash_profile" "$export_line"
      else
        append_line_once "$HOME/.bashrc" "$export_line"
      fi
      ;;
    fish)
      fish_conf="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/ariadne.fish"
      append_line_once "$fish_conf" "fish_add_path -m \"$INSTALL_DIR\""
      ;;
    *)
      append_line_once "$HOME/.profile" "$export_line"
      ;;
  esac

  if [ -n "$PATH_UPDATED_FILE" ]; then
    say "Added $INSTALL_DIR to PATH in $PATH_UPDATED_FILE"
    say "Run 'export PATH=\"$INSTALL_DIR:\$PATH\"' or open a new terminal to use 'ariadne' directly."
  fi
}

main() {
  detect_platform
  need_downloader

  TMP_DIR="$(mktemp -d 2>/dev/null || mktemp -d -t ariadne-install)"
  trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

  META_JSON="$TMP_DIR/release.json"
  if [ -n "$VERSION" ]; then
    tag="$VERSION"
    case "$tag" in
      v*) ;;
      *) tag="v$tag" ;;
    esac
    say "Fetching Ariadne release $tag..."
    http_api_get "${API_BASE%/}/repos/${REPO}/releases/tags/${tag}" "$META_JSON" \
      || die "Failed to fetch release $tag from GitHub"
    resolve_mode="tag"
  else
    say "Checking latest Ariadne release for $ASSET_NAME..."
    http_api_get "${API_BASE%/}/repos/${REPO}/releases?per_page=30" "$META_JSON" \
      || die "Failed to query GitHub Releases (set GITHUB_TOKEN if rate-limited)"
    if [ -n "$CHANNEL" ]; then
      resolve_mode="$CHANNEL"
    else
      resolve_mode="auto"
    fi
  fi

  resolved="$(resolve_release "$META_JSON" "$resolve_mode" "$ASSET_NAME")" \
    || die "No matching release asset ($ASSET_NAME) found for channel '${resolve_mode}'"

  TAG_NAME="$(printf '%s\n' "$resolved" | sed -n '1p')"
  IS_PRE="$(printf '%s\n' "$resolved" | sed -n '2p')"
  DOWNLOAD_URL="$(printf '%s\n' "$resolved" | sed -n '3p')"
  EXPECTED_SHA="$(printf '%s\n' "$resolved" | sed -n '4p')"

  [ -n "$DOWNLOAD_URL" ] || die "Release metadata did not contain a download URL"

  if [ "$resolve_mode" = "auto" ] && [ "$IS_PRE" = "true" ]; then
    say "No stable release is published yet; installing latest preview ($TAG_NAME)."
  else
    say "Installing Ariadne $TAG_NAME ($ASSET_NAME)..."
  fi

  STAGED="$TMP_DIR/$ASSET_NAME"
  http_download "$DOWNLOAD_URL" "$STAGED" || die "Failed to download $DOWNLOAD_URL"

  if [ -n "$EXPECTED_SHA" ]; then
    actual_sha="$(compute_sha256 "$STAGED")"
    if [ -n "$actual_sha" ]; then
      if [ "$actual_sha" != "$EXPECTED_SHA" ]; then
        die "SHA-256 verification failed (expected $EXPECTED_SHA, got $actual_sha)"
      fi
      say "Verified SHA-256 checksum."
    else
      warn "No sha256sum/shasum/openssl tool found; skipping checksum check."
    fi
  fi

  chmod 0755 "$STAGED"

  expected_ver="${TAG_NAME#v}"
  got_ver="$("$STAGED" --version 2>/dev/null || true)"
  if [ "$got_ver" != "$expected_ver" ]; then
    die "Downloaded binary reported version '$got_ver', expected '$expected_ver'"
  fi
  "$STAGED" self-check >/dev/null || die "Downloaded binary failed self-check"

  mkdir -p "$INSTALL_DIR"
  TARGET="$INSTALL_DIR/ariadne"
  TMP_TARGET="$INSTALL_DIR/.ariadne-install.$$"
  cp "$STAGED" "$TMP_TARGET"
  chmod 0755 "$TMP_TARGET"
  mv -f "$TMP_TARGET" "$TARGET"
  say "Installed binary: $TARGET"

  if [ "$NO_ALIAS" != "1" ]; then
    ln -sf "$TARGET" "$INSTALL_DIR/ard"
    say "Created short alias: $INSTALL_DIR/ard -> $TARGET"
  fi

  ensure_path

  if [ "$NO_SERVICE" = "1" ]; then
    say ""
    say "Ariadne $TAG_NAME installed. Start it anytime with:"
    say "  $TARGET service install --start"
    return 0
  fi

  say ""
  if "$TARGET" service status --json 2>/dev/null | grep -Eq '"running"[[:space:]]*:[[:space:]]*true'; then
    say "Detected a running Ariadne service; performing a safe restart..."
    if ! "$TARGET" service restart; then
      warn "Ariadne binary was installed at '$TARGET', but restarting the background service failed."
      say "You can retry or inspect the failure with:"
      say "  $TARGET service restart"
      say "  $TARGET service logs"
      exit 1
    fi
  else
    say "Registering and starting the Ariadne background service..."
    if ! "$TARGET" service install --start; then
      warn "Ariadne binary was installed at '$TARGET', but background service setup failed."
      say "You can retry or inspect the failure with:"
      say "  $TARGET service install --start"
      say "  $TARGET service install --no-autostart --start"
      say "  $TARGET service logs"
      exit 1
    fi
  fi
}

main
