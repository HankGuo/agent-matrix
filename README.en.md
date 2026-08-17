# Agent Matrix

[中文文档](README.md)

A lightweight **agent registry & online-status monitor**. The core idea: no daemon to install on every machine — generate a **one-shot onboarding prompt** in the WebUI and hand it to any agent with shell access (Claude Code, Kimi CLI, Codex, OpenClaw, Hermes…). The agent registers itself, persists its config, and installs a scheduled heartbeat job. You watch every agent's live status in the WebUI.

Single static binary + embedded SQLite + embedded WebUI — **zero runtime dependencies**.

## How it works

```
Browser ──► Agent Matrix Server (any one server)
              │  ① issue one-time enrollment token + onboarding prompt
              ▼
        You paste the prompt to the target agent
              ▼
  The agent itself: ② POST /api/register (consumes token, gets heartbeat token)
                    ③ writes ~/.agent-matrix/{config,heartbeat.sh}
                    ④ installs cron / launchd / systemd / schtasks job
                    ⑤ POST /api/heartbeat every minute
              ▼
   WebUI marks online/offline by last-seen (default timeout 3m)
```

## Features

- **Prompt-as-installer**: the onboarding prompt is idempotent and self-verifying; the agent reports back its result
- **One-time enrollment tokens**: valid 24h, single-use; a separate heartbeat token is issued on registration; only hashes are stored
- **Pure WebUI**: the control machine needs nothing but a browser; status dots auto-refresh every 15s
- **Cross-platform heartbeat**: the prompt covers Linux (cron / systemd user timer), macOS (launchd), and Windows (schtasks)
- **Reliable deployment**: one static binary + one SQLite file under systemd; graceful shutdown, rate limiting, and security headers built in

## Quick start

### Build

Requires Go ≥ 1.22 (developed on 1.26):

```bash
git clone https://github.com/HankGuo/agent-matrix.git
cd agent-matrix
go build -o agent-matrix .
```

Or `go install github.com/HankGuo/agent-matrix@latest`.

### Run

```bash
export AGENT_MATRIX_ADMIN_TOKEN='your-admin-token'              # required, WebUI login
export AGENT_MATRIX_BASE_URL='https://matrix.example.com'       # public URL, baked into prompts
./agent-matrix
```

Open `http://localhost:8080` (or your domain) and enter the admin token.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `AGENT_MATRIX_ADMIN_TOKEN` | — (required) | WebUI admin token |
| `AGENT_MATRIX_ADDR` | `:8080` | HTTP listen address |
| `AGENT_MATRIX_DB` | `./agent-matrix.db` | SQLite database path |
| `AGENT_MATRIX_BASE_URL` | `http://localhost:8080` | Public URL used in onboarding prompts |
| `AGENT_MATRIX_ONLINE_TIMEOUT` | `3m` | Mark offline after this long without heartbeat |

### Production tips

- **systemd** with `Restart=always`:

```ini
[Unit]
Description=Agent Matrix
After=network.target

[Service]
Environment=AGENT_MATRIX_ADMIN_TOKEN=use-a-strong-token
Environment=AGENT_MATRIX_BASE_URL=https://matrix.example.com
Environment=AGENT_MATRIX_DB=/var/lib/agent-matrix/agent-matrix.db
ExecStart=/usr/local/bin/agent-matrix
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

- **HTTPS reverse proxy (Caddy)**: `matrix.example.com { reverse_proxy 127.0.0.1:8080 }`; firewall everything except 443
- **Backup**: back up the single file at `AGENT_MATRIX_DB`

## Usage

1. Click "接入新 Agent" (top right) → optional name → generate prompt → copy
2. **Paste the prompt verbatim to the target agent** (into its chat/CLI)
3. The agent reports back: registration result, scheduler type, verification
4. Back in the WebUI, a green dot means it's online

> Note: the target agent must be able to **run shell commands and create scheduled jobs**. A chat-only agent (e.g. some vendor-hosted IM bots) cannot onboard itself — that's a mechanical constraint, not a configuration issue.

## API summary

| Method | Path | Auth | Description |
|---|---|---|---|
| `POST` | `/api/register` | one-time enrollment token | Register agent, issue heartbeat token |
| `POST` | `/api/heartbeat` | `Bearer amh_…` | Heartbeat (optional `meta` JSON) |
| `POST` | `/api/login` | admin token | WebUI login, sets session cookie |
| `GET` | `/api/agents` | session cookie | Agent list with online status |
| `POST` | `/api/enrollments` | session cookie | Issue one-time token + onboarding prompt |
| `DELETE` | `/api/agents/{id}` | session cookie | Delete agent |
| `GET` | `/healthz` | none | Health check |

## Scope

Agent Matrix is a **registry + presence monitor only** — no task dispatch. It complements task-orchestration platforms (e.g. Multica): Matrix answers "who's alive", an orchestrator answers "who's doing what".

## License

[MIT](LICENSE)
