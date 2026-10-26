# Runner image

One polyglot sandbox image for all supported languages: python, javascript,
go, java, ruby, rust, cpp (single fat image for now; split per language later
if size matters). Untrusted code runs via the dispatcher's Docker sandbox
runner: `runsc` runtime (gVisor; falls back to the default runtime in
tests/CI), `cap-drop=ALL`, read-only rootfs + tmpfs `/tmp`, no network, `192m`
memory (+`192m` swap), `0.5` CPU, `32` pids, non-root `nobody`.

## Build

```bash
docker build -t runnix-runner:local runner/docker
```

The compose stack tags it via the `runner` service (`docker compose build`),
and the dispatcher pre-pulls it at startup.

## Contract

- one source file under `/work` — `main.py`, `main.js`, `main.go`,
  `Main.java`, `main.rb`, `main.rs` or `main.cpp` (shipped as a tar over the
  container's stdin into the tmpfs `/work`)
- `/work/stdin.txt` — stdin (same tar)
- stdout/stderr → container logs, captured by the dispatcher
- exit code 0 → succeeded; nonzero → failed; killed on `timeout_s` → timeout

The dispatcher sets the container entrypoint per run (compile + exec the
matching toolchain); the image `ENTRYPOINT` is only a fallback.
