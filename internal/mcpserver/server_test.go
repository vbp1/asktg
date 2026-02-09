package mcpserver

import (
	"context"
	"testing"

	"asktg/internal/domain"
)

func TestIsLocalOrigin(t *testing.T) {
	allowed := []string{
		"http://localhost:5173",
		"http://127.0.0.1:3000",
		"http://[::1]:3000",
	}
	for _, candidate := range allowed {
		if !isLocalOrigin(candidate) {
			t.Fatalf("expected local origin: %s", candidate)
		}
	}

	blocked := []string{
		"https://example.com",
		"http://10.0.0.2:8080",
		"not-a-url",
	}
	for _, candidate := range blocked {
		if isLocalOrigin(candidate) {
			t.Fatalf("expected blocked origin: %s", candidate)
		}
	}
}

type stubQueryService struct{}

func (stubQueryService) ListChats(context.Context) ([]domain.ChatPolicy, error) {
	return []domain.ChatPolicy{
		{
			ChatID: 7,
			Title:  "Engineering",
		},
	}, nil
}

func (stubQueryService) Search(context.Context, domain.SearchRequest) ([]domain.SearchResult, error) {
	return []domain.SearchResult{
		{
			ChatID:    7,
			MsgID:     11,
			Timestamp: 1730000010,
			ChatTitle: "Engineering",
			Sender:    "Alex",
			Snippet:   "hello",
			Score:     0.3,
			DeepLink:  "tg://openmessage?chat_id=7&message_id=11",
		},
	}, nil
}

func (stubQueryService) GetMessage(context.Context, int64, int64) (domain.Message, error) {
	return domain.Message{
		ChatID:        7,
		MsgID:         11,
		Timestamp:     1730000010,
		SenderDisplay: "Alex",
		Text:          "hello world",
	}, nil
}

func (stubQueryService) GetStatus(context.Context) (domain.IndexStatus, error) {
	return domain.IndexStatus{}, nil
}

func TestSearchMessagesToolStructuredOutputShape(t *testing.T) {
	server := New(stubQueryService{})
	result, payload, err := server.searchMessagesTool(context.Background(), nil, &searchInput{
		Query: "hello",
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("searchMessagesTool failed: %v", err)
	}
	if result == nil {
		t.Fatalf("expected non-nil CallToolResult")
	}
	output, ok := payload.(searchOutput)
	if !ok {
		t.Fatalf("expected searchOutput payload, got %T", payload)
	}
	if len(output.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(output.Results))
	}
	first := output.Results[0]
	if first.ChatID != 7 || first.MsgID != 11 {
		t.Fatalf("unexpected id pair chat=%d msg=%d", first.ChatID, first.MsgID)
	}
	if first.TS == 0 || first.DeepLink == "" {
		t.Fatalf("expected ts/deep_link to be populated")
	}
}

func TestGetMessageToolStructuredOutputShape(t *testing.T) {
	server := New(stubQueryService{})
	result, payload, err := server.getMessageTool(context.Background(), nil, &getMessageInput{
		ChatID: 7,
		MsgID:  11,
	})
	if err != nil {
		t.Fatalf("getMessageTool failed: %v", err)
	}
	if result == nil {
		t.Fatalf("expected non-nil CallToolResult")
	}
	output, ok := payload.(getMessageOutput)
	if !ok {
		t.Fatalf("expected getMessageOutput payload, got %T", payload)
	}
	if output.Message.ChatTitle != "Engineering" {
		t.Fatalf("unexpected chat title: %s", output.Message.ChatTitle)
	}
	if output.Message.DeepLink == "" {
		t.Fatalf("expected deep_link in output")
	}
	if output.Message.TS == 0 {
		t.Fatalf("expected ts in output")
	}
}
