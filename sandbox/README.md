# Sandbox

A container in which a Claude instance develops `dm` with permission checks bypassed
("auto acknowledge"), without being handed the host.

```
bin/sandbox_claude            # start claude in the sandbox
bin/sandbox_claude --shell    # drop into bash in the sandbox instead
bin/sandbox_claude --rebuild  # rebuild the image (also: pick up a newer Claude CLI)
```

The image builds automatically on every launch; docker's layer cache makes a warm start
a sub-second no-op.

## Authentication

Mint a long-lived (1 year) token once, on the host:

```
claude setup-token
```

Store it outside the repo, so it can never be committed:

```
install -Dm600 /dev/null ~/.config/dark-matter/sandbox.env
printf 'CLAUDE_CODE_OAUTH_TOKEN=%s\n' '<token>' >> ~/.config/dark-matter/sandbox.env
```

The container receives the *value* as an environment variable; it never sees the file.
If no token is found, `bin/sandbox_claude --login` falls back to an interactive `/login`,
whose credential is then persisted in `.state/claude/`.

On this host the CLI is not on `PATH` — it ships inside the VS Code extension, at
`~/.vscode/extensions/anthropic.claude-code-*/resources/native-binary/claude`.

## What the sandbox can reach

| | |
|---|---|
| **mounted** | this repo at `/workspace` (rw); `sandbox/.state/` for the container's own claude config and Go caches (untracked — `rm -rf` it to reset) |
| **passed in** | `CLAUDE_CODE_OAUTH_TOKEN`, your git `user.name`/`user.email`, `TERM` |
| **not passed** | `~/.ssh`, `~/.gitconfig`, `~/.aws`, `~/.config/gh`, `/var/run/docker.sock`, `--network host`, and any other host environment — docker forwards none of it |

So the agent can edit, build, test and commit this repo, and can create the throwaway git
repos and worktrees the e2e harness needs (design.md §9). It holds no key for `origin`
(an SSH remote), so it **cannot push** — publishing stays a human act on the host.

It runs as a non-root user (`dev`, mapped to your uid, so files it writes stay yours),
with `--cap-drop=ALL` and `--security-opt no-new-privileges`.

## Two accepted risks

- **The live working copy is writable.** Bypass mode plus a read-write bind mount means the
  agent could in principle `git reset --hard` or discard uncommitted work *in this repo*. It
  cannot reach `origin`, so anything pushed is recoverable.
- **Network egress is unrestricted**, by choice. Anthropic's reference devcontainer narrows
  this with an iptables allowlist, but that needs `NET_ADMIN`/`NET_RAW` — the opposite of
  `--cap-drop=ALL`.
