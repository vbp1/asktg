# Архитектура / Техстек — Sidecar для локального поиска по Telegram (один бинарник, Go)

## 1. Ключевые принципы
- **Один бинарник**: никакого отдельного векторного сервиса.
- **Embeddings только через API** (OpenAI-compatible), локальных моделей нет.
- **Локальное хранение**: SQLite как источник истины + файлы индекса HNSW.
- **Per-chat политики** для истории, вложений и URL: `off|lazy|full`.
- Минимальный UI + MCP, всё в одном процессе.

---

## 2. Техстек (выбранные компоненты)

### 2.1 Telegram клиент (MTProto, pure Go)
- `gotd/td` — чисто Go клиент Telegram (MTProto), с примерами user-auth, обработкой FLOOD_WAIT и т.п.  
  Repo: https://github.com/gotd/td

Почему так:
- TDLib обычно требует нативные библиотеки → сложнее “один бинарник”.
- gotd/td даёт “однобинарный” путь без C/C++ зависимостей.

### 2.2 База и полнотекст
- SQLite + FTS5
  - ранжирование `ORDER BY bm25(fts_table)`
  - подсветка сниппетов (через `snippet()`/`highlight()` — по необходимости)
Документация: https://www.sqlite.org/fts5.html

### 2.3 Векторный индекс (встроенный)
- `coder/hnsw` — HNSW в Go, in-memory + есть сохранение/загрузка (Export/Import, SavedGraph).  
  Repo: https://github.com/coder/hnsw

Почему:
- чисто Go, подходит под “один бинарник”;
- есть встроенная персистентность (файл .graph);
- фокус на embedded-векторный поиск без инфраструктуры.

### 2.4 Embeddings API
- OpenAI embeddings guide (модель, размерности, параметр `dimensions`, max input 8192)  
  Документация: https://platform.openai.com/docs/guides/embeddings

Реализация:
- клиент “OpenAI-compatible”: base_url + api_key + model + dimensions
- батчинг input’ов и retry/backoff.

### 2.5 MCP
- Официальный Go SDK MCP: https://github.com/modelcontextprotocol/go-sdk

---

## 3. Компонентная архитектура

### 3.1 Процессы
Один процесс (один бинарник) с модулями:

1) **Collector (Telegram)**
   - user-auth, session storage
   - чтение истории (full) и подписка на апдейты (tail)
   - нормализация “message → Document”

2) **Policy Engine**
   - хранит per-chat настройки:
     - enabled
     - history_mode: lazy/full
     - attachments_mode: off/lazy/full
     - urls_mode: off/lazy/full
   - принимает решения: ставить ли задачи на вложения/URL/embeddings

3) **Task Queue (внутри приложения)**
   - очередь задач в SQLite (durable)
   - воркеры:
     - fetch URL
     - download attachment
     - extract text
     - chunk
     - embed
     - index

4) **Extractor**
   - Message text normalizer
   - URL extractor + fetch + HTML→text
   - Attachment extractor (best-effort по форматам)

5) **Indexer**
   - SQLite tables + FTS5 upsert
   - HNSW upsert (вставка/удаление/обновление)
   - периодическое сохранение графа на диск

6) **Query Engine**
   - FTS retrieval (topK_fts)
   - HNSW retrieval (topK_vec)
   - Fusion (RRF или иное)
   - фильтры (чат/даты/флаги)

7) **UI (localhost web)**
   - страницы: Search, Chats/Policies, Status
   - API: /search, /chats, /policy, /status, /reindex

8) **MCP Server**
   - tools/resources поверх Query Engine + Policy Engine

---

## 4. Хранение данных

### 4.1 Файлы на диске
- `app.db` (SQLite)
- `vectors.graph` (HNSW SavedGraph)
- `cache/attachments/...` (опционально, если включено)
- `cache/urls/...` (опционально)

### 4.2 SQLite schema (набросок)

#### chats
- `chat_id INTEGER PRIMARY KEY`
- `title TEXT`
- `type TEXT` (private/group/channel/saved)
- `enabled BOOL`
- `history_mode TEXT` (lazy/full)
- `attachments_mode TEXT` (off/lazy/full)
- `urls_mode TEXT` (off/lazy/full)
- `sync_cursor ...` (последняя точка синка)

#### messages
- `(chat_id, msg_id) PRIMARY KEY`
- `ts INTEGER`
- `from_id INTEGER`
- `text TEXT`
- `edit_ts INTEGER NULL`
- `deleted BOOL`
- `has_urls BOOL`
- `has_attachments BOOL`

#### chunks
Единица индексации для FTS и семантики.
- `chunk_id INTEGER PRIMARY KEY`
- `chat_id INTEGER`
- `msg_id INTEGER`
- `ordinal INTEGER` (номер чанка в сообщении/документе)
- `source TEXT` (message/url/attachment)
- `text TEXT`
- `token_est INTEGER`
- `embedding_status TEXT` (pending/done/error)
- `embedding_dim INTEGER`
- `hnsw_id INTEGER UNIQUE` (ID узла в графе)

#### urls
- `url_id INTEGER PRIMARY KEY`
- `chat_id INTEGER`
- `msg_id INTEGER`
- `url TEXT`
- `final_url TEXT`
- `fetch_status TEXT` (pending/done/error)
- `http_status INTEGER`
- `content_hash TEXT`
- `extracted_text TEXT` (или ссылкой на blob/file)

#### attachments
- `att_id INTEGER PRIMARY KEY`
- `chat_id INTEGER`
- `msg_id INTEGER`
- `file_name TEXT`
- `mime TEXT`
- `size INTEGER`
- `extract_status TEXT`
- `extracted_text TEXT` (или ссылкой)

#### tasks
- `task_id INTEGER PRIMARY KEY`
- `kind TEXT` (fetch_url, dl_attachment, extract, embed, upsert_index, ...)
- `payload_json TEXT`
- `status TEXT` (queued/running/done/error)
- `retries INTEGER`
- `run_after INTEGER`
- `last_error TEXT`

#### FTS5
- `fts_chunks` virtual table (FTS5):
  - `chunk_id UNINDEXED`
  - `text`
  - `chat_id UNINDEXED`
  - `msg_id UNINDEXED`
  - `ts UNINDEXED`

Поиск:
```sql
SELECT chunk_id, chat_id, msg_id
FROM fts_chunks
WHERE fts_chunks MATCH ?
ORDER BY bm25(fts_chunks)
LIMIT ?;
```

Источник: https://www.sqlite.org/fts5.html

---

## 5. Векторный индекс (HNSW)

### 5.1 Идентификаторы
- `hnsw_id` = `chunk_id` (самый простой стабильный ключ).
- В граф кладём узел: (id=chunk_id, vec=[]float32).

### 5.2 Обновления
- Новый chunk → `Add(node)`.
- Удаление/редактирование:
  - вариант A (простой): помечать старый chunk как “deleted” и добавлять новый (т.е. граф растёт). Периодическая “rebuild” раз в N дней.
  - вариант B (чистый): использовать `Delete` (поддерживается, но дороже), затем `Add`.

Для начала обычно хватает варианта A + периодический rebuild.

### 5.3 Персистентность
- Использовать `SavedGraph` и `Save()` периодически (например каждые 30–60 секунд, либо по N операций).
- На старте: `LoadSavedGraph(path)`.

Источник (Persistence): https://github.com/coder/hnsw

### 5.4 Параметры HNSW (рекомендации для старта)
- `M` (max neighbors): 16 (старт), 32 если нужен recall ценой памяти.
- `efSearch`: 64 (старт), повышать для качества.
- `efConstruction`: 200 (старт) для качества индекса при построении.
(Значения подбираются бенчмарком на ваших данных.)

---

## 6. Настройки embeddings (рекомендованные)

### 6.1 Модель и размерность
- `model = text-embedding-3-large`
- `dimensions`:
  - старт: **1024** (баланс скорость/память/качество для embedded-вектора)
  - если recall не устраивает: 1536 или 3072

Документация: https://platform.openai.com/docs/guides/embeddings  
Там же: по умолчанию для `text-embedding-3-large` — 3072, и можно уменьшать через `dimensions`.

### 6.2 Чанкинг (URL и большие документы)
- лимит на input: 8192 tokens
- стартовая политика:
  - chunk_size: 800–1200 токенов (оценка)
  - overlap: 80–120 токенов
- Сообщения обычно короткие → без чанкинга (1 chunk = 1 message).

Документация: https://platform.openai.com/docs/guides/embeddings

### 6.3 Батчинг и кеш
- В embeddings endpoint отправлять batch входов (массив строк).
- Кеш по `sha256(normalized_text) + model + dimensions`.
- Retry/backoff + rate limit.

---

## 7. URL-fetch безопасность (SSRF и лимиты)
Рекомендуемые защиты:
- max_size (например 5MB), timeout (10s), max_redirects (5)
- ограничение параллелизма и доменных квот
- запрет `localhost`, private IP ranges, link-local, .local
- DNS re-resolve и проверка IP перед запросом
- allowlist доменов (опционально)

---

## 8. Открытие результатов в Telegram
- Для каналов/групп использовать message links и `tg://` deep links (best-effort).  
  Документация: https://core.telegram.org/api/links
- Для Saved Messages: гарантированный deep-link на конкретный message_id может быть непредсказуемым по клиентам → fallback:
  - открыть чат,
  - показать timestamp + сниппет + “copy text”.

---

## 9. MCP API (минимальный контракт)
- `list_chats() -> [{chat_id, title, policy...}]`
- `set_chat_policy(chat_id, enabled, history_mode, attachments_mode, urls_mode)`
- `search_messages(query, filters, top_k, mode=fts|hybrid)`
- `get_message(chat_id, msg_id)`
- `get_context(chat_id, msg_id, window_before, window_after)`
- `index_status()`

SDK: https://github.com/modelcontextprotocol/go-sdk

---

## 10. Комплаенс-режимы (важно)
- Default: FTS-only.
- “Embeddings mode”:
  - предупреждение: текст отправляется во внешний API,
  - предупреждение: ограничения Telegram по AI scraping/indexing,
  - явное подтверждение.

Ссылки:
- https://telegram.org/tos/content-licensing
- https://core.telegram.org/api/terms
