# Runner images

Shared sandbox base + per-language images (python only for now). Untrusted
code runs via the dispatcher's Docker sandbox runner: `runsc` runtime (gVisor;
falls back to the default runtime in tests/CI), `cap-drop=ALL`, read-only
rootfs + tmpfs `/tmp`, no network, `128m` memory (+`128m` swap), `0.5` CPU,
`32` pids, non-root `nobody`.

K8s parity (deferred: k8s-jobs): the same image becomes the `Job` container
with `RuntimeClass: kata-gvisor` + matching `securityContext`.

## Build

```bash
docker build -t runnix-runner-python:local runner/docker
```

The compose stack tags it via the `runner-python` service
(`docker compose build`), and the dispatcher pre-pulls it at startup.

## Contract

- `/work/main.py` — source (shipped as a tar over the container's stdin into the tmpfs `/work`)
- `/work/stdin.txt` — stdin (same tar)
- stdout/stderr → container logs, captured by the dispatcher
- exit code 0 → succeeded; nonzero → failed; killed on `timeout_s` → timeout