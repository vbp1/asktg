# Telegram Sidecar Search - Development Plan

## Goal
Build a Windows-first desktop app (Go + Wails) for local Telegram search with:
- local SQLite FTS5 indexing,
- optional hybrid mode,
- read-only MCP server running with desktop app,
- per-chat policies and local-first defaults.

## Architecture
- Desktop shell: Wails v2 + Svelte frontend.
- Backend core:
  - `internal/store/sqlite`: schema, migrations, policy storage, message/chunk indexing, FTS search.
  - `internal/search`: safe Simple/Advanced query parsing for FTS5.
  - `internal/security`: URL-fetch SSRF baseline guard + Windows DPAPI secret protection.
  - `internal/mcpserver`: read-only MCP tools over streamable HTTP (`127.0.0.1`).
  - `internal/telegram`: gotd/td session auth (code + optional 2FA) and dialog discovery.
  - `internal/embeddings`: OpenAI-compatible embeddings client + worker integration.
  - `internal/vector`: HNSW index wrapper with tombstones and persisted graph snapshots.
  - `internal/tasks`: SQLite-backed durable queues for URL + embeddings workers.
- Runtime defaults:
  - data dir: `%LOCALAPPDATA%\\asktg` (override via `ASKTG_DATA_DIR`),
  - MCP auto-start enabled, loopback endpoint, origin validation.

## Public Interfaces
- Wails-bound methods:
  - `DataDir`
  - `ListChats`
  - `SetChatPolicy`
  - `Search`
  - `GetMessage`
  - `Status`
  - `MCPEndpoint`
  - `OpenInTelegram`
  - `ToggleMCP`
  - `RebuildSemanticIndex`
  - `TelegramSetCredentials`
  - `TelegramAuthStatus`
  - `TelegramRequestCode`
  - `TelegramSignIn`
  - `TelegramLoadChats`
- MCP read-only tools:
  - `list_chats`
  - `search_messages`
  - `get_message`
  - `index_status`

## Implementation Plan
1. Scaffold Wails + Svelte project and baseline runtime options.
2. Add SQLite schema/migrations and seed data for first-run validation.
3. Implement FTS query parser and search service.
4. Implement read-only MCP server with streamable HTTP and origin guard.
5. Build initial Search + Chats UI and wire Wails bindings.
6. Add tests for query parser, origin validation, and SSRF guard.
7. Integrate Telegram auth/discovery first, then collector and embeddings/HNSW modules incrementally.

## Risks and Decisions
- Telegram auth and chat discovery are implemented.
- Sync engine runs periodic polling with per-chat history cursors (newest->oldest tail + incremental backfill).
- Realtime updates manager is integrated for new/edit events and channel deletes, with periodic sync retained as recovery path.
- URL lazy pipeline is integrated: secure fetch queue (SQLite tasks), SSRF baseline enforcement, HTML text extraction, and URL text participation in search results.
- Hybrid mode merges FTS + HNSW vector candidates with RRF fusion.
- Embeddings policy is tail-only via per-chat enable timestamp (`embeddings_since_unix`), preventing automatic historical backfill.
- Onboarding wizard is enforced in UI and cannot be completed without selecting at least one chat.
- Windows autostart is implemented via HKCU `Run` key toggle in Maintenance.
- Pause/Resume background workers is implemented and persisted (`sync_paused` setting).
- No MCP auth token by decision; security relies on loopback binding + origin validation.

## Acceptance Checklist
- [x] Wails app scaffolded and starts with backend bindings.
- [x] SQLite schema and FTS table created via migrations.
- [x] Search works through backend API with safe query parsing.
- [x] Read-only MCP server starts and exposes required tools.
- [x] MCP UI controls added (enable/disable, configurable port, copy endpoint, runtime status).
- [x] UI provides Search, Status, and per-chat policy controls.
- [x] Results actions follow contract: `Open in Telegram` and conditional `Copy link/reference` when deep-link exists.
- [x] Data directory wizard controls added (browse/apply persisted path, restart-to-apply).
- [x] `Open in Telegram` includes fallback to chat-level deep-link when message-level link is unavailable.
- [x] URL search results surface linked-page metadata together with source message context.
- [x] Tray flow implemented: close-to-tray, tray menu show/hide, explicit exit, second-instance restore.
- [x] Tests added for parser and URL/origin safety.
- [x] Telegram auth and chat discovery (gotd/td).
- [x] Telegram periodic sync/backfill with per-chat cursors.
- [x] Telegram realtime updates manager (new/edit + channel delete, best-effort private delete mapping).
- [x] URL lazy fetch pipeline (queue + fetch + index + search integration).
- [x] Durable task workers for URL/embeddings pipelines.
- [x] HNSW-backed hybrid retrieval and graph persistence (`vectors.graph`, tombstones + rebuild flow).
- [x] Backup/restore and purge flows.
