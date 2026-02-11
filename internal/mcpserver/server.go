package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"asktg/internal/domain"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type QueryService interface {
	ListChats(ctx context.Context) ([]domain.ChatPolicy, error)
	Search(ctx context.Context, req domain.SearchRequest) ([]domain.SearchResult, error)
	GetMessage(ctx context.Context, chatID int64, msgID int64) (domain.Message, error)
	GetStatus(ctx context.Context) (domain.IndexStatus, error)
}

type Server struct {
	mu        sync.RWMutex
	query     QueryService
	httpSrv   *http.Server
	endpoint  string
	startedAt time.Time
}

func New(query QueryService) *Server {
	return &Server{query: query}
}

func (s *Server) Endpoint() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.endpoint
}

func (s *Server) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpSrv != nil {
		return nil
	}

	host := "127.0.0.1"
	addr := fmt.Sprintf("%s:%d", host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	impl := &mcp.Implementation{Name: "asktg-mcp", Version: "0.1.0"}
	server := mcp.NewServer(impl, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_chats",
		Description: "List chats and their index policies",
	}, s.listChatsTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_messages",
		Description: "Search indexed Telegram messages",
	}, s.searchMessagesTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_message",
		Description: "Get one message by chat_id and msg_id",
	}, s.getMessageTool)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "index_status",
		Description: "Get indexer and MCP status",
	}, s.indexStatusTool)

	streamHandler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return server
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", withOriginValidation(streamHandler))
	httpSrv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		_ = httpSrv.Serve(listener)
	}()

	s.httpSrv = httpSrv
	s.endpoint = "http://" + listener.Addr().String() + "/mcp"
	s.startedAt = time.Now()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpSrv == nil {
		return nil
	}
	err := s.httpSrv.Shutdown(ctx)
	s.httpSrv = nil
	s.endpoint = ""
	return err
}

type listChatsOutput struct {
	Chats []domain.ChatPolicy `json:"chats"`
}

func (s *Server) listChatsTool(ctx context.Context, _ *mcp.CallToolRequest, _ *struct{}) (*mcp.CallToolResult, any, error) {
	chats, err := s.query.ListChats(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Returned %d chats", len(chats))}},
	}, listChatsOutput{Chats: chats}, nil
}

type searchInput struct {
	Query    string  `json:"query" jsonschema:"Search query string"`
	Mode     string  `json:"mode" jsonschema:"Search mode: fts or hybrid"`
	TopK     int     `json:"top_k" jsonschema:"Maximum number of results"`
	FromUnix int64   `json:"from_unix,omitempty" jsonschema:"Optional lower timestamp bound"`
	ToUnix   int64   `json:"to_unix,omitempty" jsonschema:"Optional upper timestamp bound"`
	ChatIDs  []int64 `json:"chat_ids,omitempty" jsonschema:"Optional chat filter list"`
	Advanced bool    `json:"advanced,omitempty" jsonschema:"Treat query as advanced syntax"`
}

type searchOutput struct {
	Results []searchResult `json:"results"`
}

type searchResult struct {
	ChatID    int64   `json:"chat_id"`
	MsgID     int64   `json:"msg_id"`
	TS        int64   `json:"ts"`
	ChatTitle string  `json:"chat_title"`
	Sender    string  `json:"sender"`
	Snippet   string  `json:"snippet"`
	Score     float64 `json:"score"`
	DeepLink  string  `json:"deep_link,omitempty"`
}

func (s *Server) searchMessagesTool(ctx context.Context, _ *mcp.CallToolRequest, in *searchInput) (*mcp.CallToolResult, any, error) {
	if in == nil || strings.TrimSpace(in.Query) == "" {
		return nil, nil, errors.New("query is required")
	}
	mode := domain.SearchModeFTS
	if strings.EqualFold(in.Mode, string(domain.SearchModeHybrid)) {
		mode = domain.SearchModeHybrid
	}
	req := domain.SearchRequest{
		Query: in.Query,
		Mode:  mode,
		Filters: domain.SearchFilters{
			ChatIDs:  in.ChatIDs,
			FromUnix: in.FromUnix,
			ToUnix:   in.ToUnix,
			Limit:    in.TopK,
			Advanced: in.Advanced,
		},
	}
	results, err := s.query.Search(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	payload := make([]searchResult, 0, len(results))
	for _, item := range results {
		payload = append(payload, searchResult{
			ChatID:    item.ChatID,
			MsgID:     item.MsgID,
			TS:        item.Timestamp,
			ChatTitle: item.ChatTitle,
			Sender:    item.Sender,
			Snippet:   item.Snippet,
			Score:     item.Score,
			DeepLink:  item.DeepLink,
		})
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Returned %d results", len(results))}},
	}, searchOutput{Results: payload}, nil
}

type getMessageInput struct {
	ChatID int64 `json:"chat_id" jsonschema:"Chat ID"`
	MsgID  int64 `json:"msg_id" jsonschema:"Message ID"`
}

type getMessageOutput struct {
	Message getMessageResult `json:"message"`
}

type getMessageResult struct {
	ChatID    int64  `json:"chat_id"`
	MsgID     int64  `json:"msg_id"`
	TS        int64  `json:"ts"`
	ChatTitle string `json:"chat_title"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	DeepLink  string `json:"deep_link,omitempty"`
}

func (s *Server) getMessageTool(ctx context.Context, _ *mcp.CallToolRequest, in *getMessageInput) (*mcp.CallToolResult, any, error) {
	if in == nil || in.ChatID == 0 || in.MsgID == 0 {
		return nil, nil, errors.New("chat_id and msg_id are required")
	}
	message, err := s.query.GetMessage(ctx, in.ChatID, in.MsgID)
	if err != nil {
		return nil, nil, err
	}
	chats, err := s.query.ListChats(ctx)
	if err != nil {
		return nil, nil, err
	}
	chatTitle := ""
	for _, chat := range chats {
		if chat.ChatID == in.ChatID {
			chatTitle = chat.Title
			break
		}
	}
	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Message returned"}},
		}, getMessageOutput{
			Message: getMessageResult{
				ChatID:    message.ChatID,
				MsgID:     message.MsgID,
				TS:        message.Timestamp,
				ChatTitle: chatTitle,
				Sender:    message.SenderDisplay,
				Text:      message.Text,
				DeepLink:  buildDeepLink(message.ChatID, message.MsgID),
			},
		}, nil
}

type indexStatusOutput struct {
	Status domain.IndexStatus `json:"status"`
}

func (s *Server) indexStatusTool(ctx context.Context, _ *mcp.CallToolRequest, _ *struct{}) (*mcp.CallToolResult, any, error) {
	status, err := s.query.GetStatus(ctx)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "Status returned"}},
	}, indexStatusOutput{Status: status}, nil
}

func withOriginValidation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !isLocalOrigin(origin) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func buildDeepLink(chatID int64, msgID int64) string {
	if chatID == 0 || msgID == 0 {
		return ""
	}
	if channelID, ok := toTmeChannelID(chatID); ok {
		return fmt.Sprintf("https://t.me/c/%d/%d", channelID, msgID)
	}
	return fmt.Sprintf("tg://openmessage?chat_id=%d&message_id=%d", chatID, msgID)
}

func toTmeChannelID(chatID int64) (int64, bool) {
	if chatID > -1000000000000 {
		return 0, false
	}
	channelID := (-chatID) - 1000000000000
	if channelID <= 0 {
		return 0, false
	}
	return channelID, true
}
