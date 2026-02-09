# asktg

Windows-first Telegram sidecar search desktop app built with Go + Wails.

## Current State
- Wails desktop shell + Svelte UI.
- SQLite schema + FTS5-based message search.
- Hybrid retrieval: FTS + HNSW vector search (RRF fusion).
- Embeddings ingestion is tail-only per chat (no automatic history backfill when toggled on).
- Per-chat policy management (enabled/history/embeddings/url mode).
- Read-only MCP server (streamable HTTP on loopback).
- MCP runtime controls in UI (enable/disable, port override, endpoint copy).
- MCP tools return structured metadata, snippets, and deep links for search/message retrieval.
- Backup/restore and purge flows in UI.
- Vector graph persistence to `vectors.graph` with rebuild workflow.
- Secret settings are protected with Windows DPAPI (`telegram_api_hash`, `embeddings_api_key`).
- Mandatory onboarding flow: Telegram setup + chat discovery + require at least one enabled chat before search/sync.
- Windows autostart toggle in Maintenance (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`).
- Pause/Resume background workers from UI while keeping local search available.
- Explicit `Exit app` action for close-to-tray flow.
- Native Windows tray icon/menu (`Show Window` / `Hide Window` / `Exit`) with single-instance restore.
- Tray interaction policy: left click restores hidden window, right click opens tray context menu.
- `Open in Telegram` now falls back to chat-level deep-link when message-level deep-link is unavailable.
- Search result cards support `Copy link/reference` only when deep-link is available.
- URL-matched results display linked page metadata (title/url) plus the source Telegram message text.
- Security baseline utilities:
  - URL fetch guard against localhost/private/link-local targets.
  - MCP origin validation for local origins.

## Run (dev)
```powershell
wails dev
```

## Test
```powershell
go test ./...
```

## Build
```powershell
wails build
```

## Data Directory
Default: `%LOCALAPPDATA%\asktg`
- You can pick and persist a custom data directory from the onboarding wizard (`Browse data dir` / `Apply data dir`), then restart app.
- `ASKTG_DATA_DIR` environment variable still overrides persisted/app defaults for the current process.
Manual override with environment variable:
```powershell
$env:ASKTG_DATA_DIR = "C:\path\to\data"
```

## MCP Endpoint
The app starts MCP with desktop runtime and exposes endpoint in UI status.  
Transport: streamable HTTP on `127.0.0.1`.
