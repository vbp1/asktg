package domain

import "time"

type SearchMode string

const (
	SearchModeFTS    SearchMode = "fts"
	SearchModeHybrid SearchMode = "hybrid"
)

type ChatPolicy struct {
	ChatID          int64  `json:"chat_id"`
	Title           string `json:"title"`
	Type            string `json:"type"`
	Enabled         bool   `json:"enabled"`
	HistoryMode     string `json:"history_mode"`
	AllowEmbeddings bool   `json:"allow_embeddings"`
	URLsMode        string `json:"urls_mode"`
	SyncCursor      string `json:"sync_cursor"`
	LastMessageUnix int64  `json:"last_message_unix"`
	LastSyncedUnix  int64  `json:"last_synced_unix"`
}

type SearchFilters struct {
	ChatIDs  []int64 `json:"chat_ids"`
	FromUnix int64   `json:"from_unix"`
	ToUnix   int64   `json:"to_unix"`
	HasURL   *bool   `json:"has_url"`
	Limit    int     `json:"limit"`
	Advanced bool    `json:"advanced"`
}

type SearchRequest struct {
	Query   string        `json:"query"`
	Mode    SearchMode    `json:"mode"`
	Filters SearchFilters `json:"filters"`
}

type SearchUISettings struct {
	Mode        string `json:"mode"`
	ResultLimit string `json:"result_limit"`
	Advanced    bool   `json:"advanced"`
}

type SearchResult struct {
	ChatID             int64   `json:"chat_id"`
	MsgID              int64   `json:"msg_id"`
	Timestamp          int64   `json:"timestamp"`
	ChatTitle          string  `json:"chat_title"`
	Sender             string  `json:"sender"`
	Snippet            string  `json:"snippet"`
	MessageText        string  `json:"message_text"`
	ExtractedSnippet   string  `json:"extracted_snippet,omitempty"`
	SourceType         string  `json:"source_type,omitempty"`
	URL                string  `json:"url,omitempty"`
	URLFinal           string  `json:"url_final,omitempty"`
	URLTitle           string  `json:"url_title,omitempty"`
	URLMime            string  `json:"url_mime,omitempty"`
	FileName           string  `json:"file_name,omitempty"`
	FileMime           string  `json:"file_mime,omitempty"`
	FileSize           int64   `json:"file_size,omitempty"`
	FileDocID          int64   `json:"file_doc_id,omitempty"`
	Score              float64 `json:"score"`
	SemanticSimilarity float64 `json:"semantic_similarity,omitempty"`
	DeepLink           string  `json:"deep_link,omitempty"`
	MatchFTS           bool    `json:"match_fts,omitempty"`
	MatchSemantic      bool    `json:"match_semantic,omitempty"`
}

type Message struct {
	ChatID        int64  `json:"chat_id"`
	MsgID         int64  `json:"msg_id"`
	Timestamp     int64  `json:"timestamp"`
	EditTS        int64  `json:"edit_ts"`
	SenderID      int64  `json:"sender_id"`
	SenderDisplay string `json:"sender_display"`
	Text          string `json:"text"`
	Deleted       bool   `json:"deleted"`
	HasURL        bool   `json:"has_url"`
}

type IndexStatus struct {
	SyncState         string `json:"sync_state"`
	BackfillProgress  int    `json:"backfill_progress"`
	QueueDepth        int    `json:"queue_depth"`
	LastSyncUnix      int64  `json:"last_sync_unix"`
	MCPEndpoint       string `json:"mcp_endpoint"`
	MCPEnabled        bool   `json:"mcp_enabled"`
	MCPStatus         string `json:"mcp_status"`
	MCPPort           int    `json:"mcp_port"`
	MessageCount      int    `json:"message_count"`
	IndexedChunkCount int    `json:"indexed_chunk_count"`
	UpdatedAtUnix     int64  `json:"updated_at_unix"`
}

type TelegramAuthStatus struct {
	Configured   bool   `json:"configured"`
	Authorized   bool   `json:"authorized"`
	AwaitingCode bool   `json:"awaiting_code"`
	Phone        string `json:"phone"`
	UserDisplay  string `json:"user_display"`
}

type EmbeddingsConfig struct {
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Configured bool   `json:"configured"`
}

type EmbeddingsTestResult struct {
	OK         bool   `json:"ok"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	VectorLen  int    `json:"vector_len"`
	TookMs     int64  `json:"took_ms"`
	Error      string `json:"error,omitempty"`
}

type OnboardingStatus struct {
	Completed          bool `json:"completed"`
	TelegramConfigured bool `json:"telegram_configured"`
	TelegramAuthorized bool `json:"telegram_authorized"`
	ChatsDiscovered    int  `json:"chats_discovered"`
	EnabledChats       int  `json:"enabled_chats"`
}

type ChatFolder struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	Emoticon      string  `json:"emoticon,omitempty"`
	Color         int     `json:"color,omitempty"`
	ChatIDs       []int64 `json:"chat_ids"`
	PinnedChatIDs []int64 `json:"pinned_chat_ids,omitempty"`
}

type EmbeddingsProgress struct {
	TotalEligible int `json:"total_eligible"`
	Embedded      int `json:"embedded"`
	QueuePending  int `json:"queue_pending"`
	QueueRunning  int `json:"queue_running"`
	QueueFailed   int `json:"queue_failed"`
}

func (s *IndexStatus) Touch() {
	s.UpdatedAtUnix = time.Now().Unix()
}
