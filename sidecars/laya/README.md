# laya-mlx sidecar

A free, local decision backend that runs **beside** jev, not instead of it.

[laya-mlx](https://github.com/mizorewww/laya-mlx) is an Apache-2.0 MLX port of
the Laya typed-decision models. It answers the same three question shapes
captain's decision leg asks - `choice`, `score`, `noul` - and returns them in
the same `{model, answers, usage}` envelope, because both descend from the
same format. `serve.py` puts an HTTP socket in front of it and adds the two
guards captain needs; it is about 200 lines and translates nothing.

Measured by its authors on an M3 Max: **13.4ms** median for a short English
decision, 7.4ms on the multilingual checkpoint, 0 output tokens. Weights are
downloaded once and inference is local, so every call after that is free.

## Requirements

Apple silicon, macOS 14+, Python 3.11+. laya-mlx is **not** a dependency of
Captain Code and nothing here is vendored: without it this directory does
nothing and the rest of captain is unchanged.

```bash
pip install laya-mlx
python3 sidecars/laya/serve.py --port 8181     # first run downloads the checkpoint
```

## Wiring it up

```bash
# ~/.config/captain/env
CAPTAIN_SYSTEMONE_OPEN_URL=http://127.0.0.1:8181
# CAPTAIN_SYSTEMONE_OPEN_CONTEXT=1024   # the multilingual / typed-decisions checkpoints
```

`CAPTAIN_SYSTEMONE_OPEN_URL` is **not** `CAPTAIN_SYSTEMONE_URL`. The second
one *replaces* the decision leg; this one adds a backend beside it. jev stays
the default, keeps the action gate and the supervisor, and keeps every call
whose state is larger than the sidecar can read.

## Before it decides anything

```bash
captain jev conform --open      # does it answer captain's questions at all?
captain jev shadow --backend 127.0.0.1:8181
```

`conform` is a smoke test: a fixed suite of questions whose answers are not in
doubt. Passing means the backend is not broken, never that captain should
route on it. With `--open` the exit code speaks for the capabilities a sidecar
can actually be promoted to (`triage`, `route`); the rest are still run and
still reported.

The number that matters comes from the shadow. Configure the sidecar and use
captain normally: it answers the triage and routing questions beside every
turn captain was already unsure about, on rows of its own stamped with its
host, and decides nothing. When `captain jev shadow --backend 127.0.0.1:8181`
prints a bar for a point, that bar is this backend's:

```bash
CAPTAIN_SYSTEMONE_OPEN_FOR=triage=0.85
```

That line is the promotion, and the number in it is the only thing that makes
one. There is no flag that promotes a backend without stating the bar you
read off its own calibration, and jev's bar is never available to it.

## What it is not allowed to do

`gate`, `supervise` and `keep` stay on the primary backend whatever
`CAPTAIN_SYSTEMONE_OPEN_FOR` says. The first two send the largest states
captain produces - a command with its working directory and the worker's
assignment, a worker's whole recent action list - which is both where a
512-token context truncates first and where being wrong means a destructive
command screened on two thirds of its text. The third decides what compaction
drops, and what is dropped does not come back.

That is a policy, but the mechanism underneath it is not: laya's sequence
builder **cuts** an oversized state and answers anyway. So the sidecar counts
the state's tokens and refuses with `400 max_tokens_exceeded` - the same
status the vendor uses - rather than answering about part of it, and captain
checks the size before dispatching and sends oversized calls to the backend
that can hold them.

## Checkpoints

| Model | Params | Context | Notes |
|---|---:|---:|---|
| `aac6fef/laya-mlx` | 421M | 512 | default here; English |
| `aac6fef/laya-multilingual-mlx` | 322M | 1,024 | set `CAPTAIN_SYSTEMONE_OPEN_CONTEXT=1024` |
| `aac6fef/laya-typed-decisions-mlx` | 421M | 1,024 | same |

```bash
python3 sidecars/laya/serve.py --model aac6fef/laya-multilingual-mlx --port 8181
```

The server binds loopback and has no authentication, which is why it refuses
any other interface unless you pass `--allow-remote` and mean it.
