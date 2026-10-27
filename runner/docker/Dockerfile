# Runnix polyglot sandbox runner (python, javascript, go, java, ruby, rust, cpp).

# Untrusted code runs inside this image under runsc (or the Docker default
# runtime in tests). No source is baked in; the dispatcher streams the source
# file + stdin.txt as a tar over the container's stdin into the writable tmpfs
# /work, then execs the matching toolchain itself. The image ENTRYPOINT is a
# fallback only: the dispatcher always sets Entrypoint per run.
#
# Source contract, all under /work:
#   main.py | main.js | main.go | Main.java | main.rb | main.rs | main.cpp
#   stdin.txt
#
# The dispatcher starts the container as uid/gid 65534 (nobody) via
# HostConfig/Config, so no passwd entry is required here.
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HOME=/tmp \
    GOCACHE=/tmp \
    GOPATH=/tmp

# Single apt transaction = one layer: python ships in the base, this adds the
# javascript (nodejs), go, java, ruby, rust and cpp (g++) toolchains.
# libc6-dev is explicit: --no-install-recommends drops it, and g++/rustc need
# it to link. Package versions are deliberately unpinned (DL3008): the base
# image tag pins the distro snapshot; per-package pins break on mirror updates.
# hadolint ignore=DL3008
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        nodejs \
        golang-go \
        openjdk-21-jdk-headless \
        ruby \
        rustc \
        g++ \
        libc6-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /work

USER 65534:65534
ENTRYPOINT ["/bin/sh"]
