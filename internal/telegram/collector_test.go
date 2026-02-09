package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestParseCursor(t *testing.T) {
	value, ok := parseCursor("123")
	if !ok || value != 123 {
		t.Fatalf("expected cursor=123, ok=true, got %d, %v", value, ok)
	}

	_, ok = parseCursor("0")
	if ok {
		t.Fatal("expected zero cursor to be invalid")
	}

	_, ok = parseCursor("abc")
	if ok {
		t.Fatal("expected non-numeric cursor to be invalid")
	}
}

func TestContainsURL(t *testing.T) {
	if !containsURL("visit https://example.com") {
		t.Fatal("expected URL to be detected")
	}
	if containsURL("plain text message") {
		t.Fatal("expected plain text to not match URL")
	}
}

func TestResolveSenderUser(t *testing.T) {
	msg := &tg.Message{}
	msg.SetFromID(&tg.PeerUser{UserID: 42})

	lookup := entityLookup{
		users: map[int64]*tg.User{
			42: {
				ID:        42,
				FirstName: "Alice",
				LastName:  "Smith",
			},
		},
		chats:    map[int64]*tg.Chat{},
		channels: map[int64]*tg.Channel{},
	}

	senderID, sender := resolveSender(msg, lookup)
	if senderID != 42 {
		t.Fatalf("expected senderID=42, got %d", senderID)
	}
	if sender != "Alice Smith" {
		t.Fatalf("expected sender name to be resolved, got %q", sender)
	}
}

func TestResolveSenderFallback(t *testing.T) {
	msg := &tg.Message{}
	msg.Out = true
	senderID, sender := resolveSender(msg, entityLookup{
		users:    map[int64]*tg.User{},
		chats:    map[int64]*tg.Chat{},
		channels: map[int64]*tg.Channel{},
	})
	if senderID != 0 || sender != "You" {
		t.Fatalf("expected outgoing fallback sender, got %d %q", senderID, sender)
	}
}
