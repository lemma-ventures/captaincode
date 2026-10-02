FROM python:3.12-slim-bookworm@sha256:392307d22300de8b5986851a12d9176dfc0fc073e65bf6523ebd7dcbeb23564e
RUN apt-get update && apt-get install -y --no-install-recommends git curl ca-certificates ripgrep=13.0.0-4+b2 && rm -rf /var/lib/apt/lists/*
RUN printf '#!/bin/sh\nexec /usr/bin/rg --sort=path "$@"\n' > /usr/local/bin/rg && chmod 755 /usr/local/bin/rg
COPY opencode /usr/local/bin/opencode
RUN useradd --uid 1000 --create-home --shell /bin/bash sandbox && mkdir -p /sandbox /outside && chown sandbox:sandbox /sandbox && printf 'captain-outside-canary\n' > /outside/canary && chmod 777 /outside && chmod 644 /outside/canary
USER sandbox
WORKDIR /sandbox
ENV HOME=/sandbox/home
ENV PYTHONDONTWRITEBYTECODE=1
ENV OPENCODE_DISABLE_AUTOUPDATE=true
ENV OPENCODE_DISABLE_MODELS_FETCH=true
ENV OPENCODE_DISABLE_DEFAULT_PLUGINS=true
ENV OPENCODE_CONFIG=/sandbox/opencode.json
CMD ["sleep", "infinity"]
