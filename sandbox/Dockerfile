# Sandbox image: a Claude instance developing `dm` (see design.md) with permission
# checks bypassed. Nothing from the host is baked in — credentials arrive only as
# CLAUDE_CODE_OAUTH_TOKEN at run time (see bin/sandbox_claude).
FROM golang:1.26-bookworm

# Match the host user so files written into the bind-mounted repo keep their owner.
# Non-root is also mandatory: claude refuses --dangerously-skip-permissions as root.
ARG HOST_UID=1000
ARG HOST_GID=1000
ARG CLAUDE_VERSION=latest

# No sudo on purpose — there is nothing for --security-opt no-new-privileges to defeat.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        less \
        make \
        procps \
        ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd -g "${HOST_GID}" dev 2>/dev/null || true \
    && useradd -m -u "${HOST_UID}" -g "${HOST_GID}" -s /bin/bash dev

ENV HOME=/home/dev \
    CLAUDE_CONFIG_DIR=/home/dev/.claude \
    GOPATH=/home/dev/go \
    GOMODCACHE=/home/dev/go/pkg/mod \
    GOCACHE=/home/dev/.cache/go-build \
    GOFLAGS=-buildvcs=false \
    DISABLE_AUTOUPDATER=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    PATH=/home/dev/.local/bin:/home/dev/go/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

# Mountpoints for the persisted sandbox state (bind-mounted from sandbox/.state/ at
# run time). The chown covers intermediate dirs too — `install -d` only applies -o/-g
# to the final component, which would leave e.g. /home/dev/.local owned by root and
# break the CLI installer below.
RUN mkdir -p \
        /home/dev/.claude \
        /home/dev/.local/bin \
        /home/dev/go/pkg/mod \
        /home/dev/.cache/go-build \
        /workspace \
    && chown -R dev:dev /home/dev /workspace

USER dev
WORKDIR /workspace

# Native binary install — keeps a Node runtime out of a Go image.
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- "${CLAUDE_VERSION}" \
    && claude --version

# The e2e harness (design.md §9.1) builds real git repos and commits into them, which
# git refuses to do without an identity. bin/sandbox_claude overrides author/committer
# from the host's name+email; this is the fallback. The host ~/.gitconfig is never
# mounted — it can carry credential helpers and signing keys.
RUN git config --global user.name "dm sandbox" \
    && git config --global user.email "sandbox@dark-matter.invalid" \
    && git config --global init.defaultBranch main \
    && git config --global commit.gpgsign false \
    && git config --global safe.directory /workspace

COPY --chown=dev:dev entrypoint.sh /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["claude", "--dangerously-skip-permissions"]
