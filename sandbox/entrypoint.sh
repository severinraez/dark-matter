#!/usr/bin/env bash
# Seeds a fresh claude config volume, then hands off. Every step here is best-effort:
# a failure to pre-seed must never stop the container from starting.
set -uo pipefail

config_dir="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
mkdir -p "$config_dir" 2>/dev/null

# Skip the first-run wizard and default to bypassed permissions, so an unattended
# agent isn't parked on an interactive prompt.
if [[ ! -f "$config_dir/settings.json" ]]; then
    cat >"$config_dir/settings.json" <<'JSON' 2>/dev/null
{
  "permissions": {
    "defaultMode": "bypassPermissions"
  }
}
JSON
fi

if [[ ! -f "$config_dir/.claude.json" ]]; then
    echo '{"hasCompletedOnboarding": true}' >"$config_dir/.claude.json" 2>/dev/null
fi

if [[ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" && ! -f "$config_dir/.credentials.json" ]]; then
    echo "sandbox: no CLAUDE_CODE_OAUTH_TOKEN and no stored credentials." >&2
    echo "sandbox: run '/login' here, or exit and see 'bin/sandbox_claude --help'." >&2
fi

exec "$@"
