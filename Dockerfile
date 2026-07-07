# syntax=docker/dockerfile:1
#
# Mnemosyne container image — the NAS-side "brain".
#
# The container catalogs, plans, builds packages, and MIRRORS to spinning drives.
# Hardware-in-the-loop workflows (tape/optical burning, the docking flow, SMART)
# belong on a NATIVE binary running next to the hardware — see the README.
#
# Multi-stage: a static CGO-free binary, then a tiny Alpine runtime that carries
# the three external tools the restore story depends on (tar, gpg, par2).

# ---- build ----------------------------------------------------------------
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.appVersion=${VERSION}" -o /out/mnemosyne .

# ---- runtime --------------------------------------------------------------
FROM alpine:3.20
# The whole restore story in three ubiquitous tools. `tar` on Alpine is GNU tar
# (supports --format=posix and -T); par2cmdline provides `par2`; gnupg `gpg`.
RUN apk add --no-cache tar gnupg par2cmdline ca-certificates wget

COPY --from=build /out/mnemosyne /usr/local/bin/mnemosyne

# /data  — catalog.json, config.json, daily backups (mount a persistent volume)
# /staging — scratch space for package builds (big + fast; can be ephemeral)
VOLUME ["/data", "/staging"]

EXPOSE 7821

# Binding 0.0.0.0 makes the UI reachable off-box, which the binary REFUSES unless
# an auth token is set (env MNEMO_AUTH_TOKEN or config.json auth_token). Set one.
#   docker run -e MNEMO_AUTH_TOKEN=... -p 7821:7821 -v mnemo-data:/data ...
ENTRYPOINT ["mnemosyne"]
CMD ["-listen", "0.0.0.0:7821", "-data", "/data"]

# A healthcheck that needs no token: the static UI root is public (only /api is
# gated), so a 200 there means the server is up.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:7821/ >/dev/null 2>&1 || exit 1
