# Runnix sandbox runner (python).

# Untrusted code runs inside this image under runsc (or the Docker default
# runtime in tests). No source is baked in; the dispatcher streams main.py +
# stdin.txt as a tar over the container's stdin into the writable tmpfs
# /work, then runs `python /work/main.py`.
#
# The dispatcher starts the container as uid/gid 65534 (nobody) via
# HostConfig/Config, so no passwd entry is required here.
FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    HOME=/tmp

WORKDIR /work

USER 65534:65534
ENTRYPOINT ["python", "/work/main.py"]