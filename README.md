# asktg

Local desktop search for your Telegram chats. Index selected conversations, search with full-text and semantic retrieval through a built-in GUI, and optionally expose results to LLM agents via an MCP server.

Built with Go, Svelte, and [Wails](https://wails.io). Windows-first.

## Features

- **Full-text search** — SQLite FTS5 with BM25 ranking and advanced query syntax (AND, OR, NOT, phrase, prefix)
- **Hybrid search** — combines FTS with HNSW vector search using reciprocal rank fusion (RRF); powered by any OpenAI-compatible embeddings API
- **Per-chat policies** — fine-grained control over which chats to index, history depth, URL/PDF extraction, and embeddings
- **URL & PDF indexing** — extracts and indexes text from links and PDF attachments found in messages
- **Realtime sync** — periodic background sync plus instant updates for new, edited, and deleted messages
- **MCP server** — read-only HTTP server on localhost with tools for `search_messages`, `get_message`, `list_chats`, and `index_status`; connect it to Claude or any MCP-compatible LLM agent
- **Windows integration** — system tray icon, close-to-tray, autostart, single-instance restore
- **Backup & restore** — full data backup/restore and purge from the UI
- **Security** — secrets protected with Windows DPAPI; URL fetch guard against SSRF; MCP origin validation (localhost only)

## Prerequisites

- Windows 10/11
- [Telegram API credentials](https://my.telegram.org/apps) (API ID and API Hash)
- *(Optional)* An OpenAI-compatible embeddings API key for semantic search

## Installation

Download the latest version from the [Releases](https://github.com/vbp1/asktg/releases) page. `asktg.exe` - standalone portable binary — no installation required, just run it

Alternatively, build from source (see [Development](#development)).

## Getting Started

1. **Launch** `asktg.exe`. The onboarding wizard will guide you through:
   - Entering your Telegram API ID and API Hash
   - Logging in with your phone number and verification code (2FA supported)
   - Discovering and enabling at least one chat for indexing

3. **Search** — once indexing completes, use the Search tab to query your messages. Toggle between simple and advanced (FTS5 syntax) modes, filter by chat or date range.

4. **Configure chats** — in the Chats tab, set per-chat policies:
   - **History** — `off` / `lazy` (new messages only) / `full` (backfill entire history)
   - **URLs** — `off` / `lazy` / `full` — controls URL text extraction
   - **Embeddings** — enable semantic search per chat

5. **Enable semantic search** *(optional)* — go to Settings, enter your embeddings API endpoint, model, and API key. Enable embeddings on desired chats.

6. **Connect an LLM agent** *(optional)* — enable the MCP server in Settings, copy the endpoint URL, and add it to your agent's MCP config. The server exposes read-only search tools on `127.0.0.1`.

## Data Directory

Default: `%LOCALAPPDATA%\asktg`

You can pick a custom data directory during onboarding or later in Settings. Override for the current process:

```powershell
$env:ASKTG_DATA_DIR = "D:\my-asktg-data"
```

Contents:
- `app.db` — SQLite database (messages, chat policies, task queue)
- `vectors.graph` — HNSW vector index
- `telegram/session.json` — Telegram session state

## Development

Requires **Go 1.24+**, **Node.js 18+**, and the [Wails CLI](https://wails.io/docs/gettingstarted/installation).

```bash
# Run in dev mode (hot-reload UI)
wails dev

# Run tests
go test ./...

# Production build
wails build
```

Output binary: `build/bin/asktg.exe`

### Telegram API Credentials for Development

Create `tg_secrets.local.json` in the project root (gitignored):

```json
{
  "id": "YOUR_APP_ID",
  "hash": "YOUR_APP_HASH"
}
```

Then generate the encrypted secrets file:

```bash
go generate ./...
```

## License

[MIT](LICENSE)
