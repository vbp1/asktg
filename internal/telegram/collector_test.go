package telegram

import (
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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

func TestShouldMarkDialogRead(t *testing.T) {
	if shouldMarkDialogRead(Dialog{Type: "channel"}) {
		t.Fatal("expected broadcast channels to be skipped")
	}
	if !shouldMarkDialogRead(Dialog{Type: "group"}) {
		t.Fatal("expected groups to be markable")
	}
	if !shouldMarkDialogRead(Dialog{Type: "private"}) {
		t.Fatal("expected private chats to be markable")
	}
}

func TestAdaptiveBatchFloorAndRecovery(t *testing.T) {
	svc := NewService("test-session")
	if got := svc.currentAdaptiveBatchSize(); got != historyBatchSize {
		t.Fatalf("expected default adaptive batch %d, got %d", historyBatchSize, got)
	}

	svc.noteAdaptiveBatchFlood(101, 4*time.Second)
	lowered := svc.currentAdaptiveBatchSize()
	if lowered >= historyBatchSize {
		t.Fatalf("expected adaptive batch to shrink, got %d", lowered)
	}
	if lowered < minHistoryBatchSize {
		t.Fatalf("expected adaptive batch >= %d, got %d", minHistoryBatchSize, lowered)
	}

	for i := 0; i < historySuccessBumpThreshold; i++ {
		svc.noteAdaptiveBatchSuccess()
	}
	if grown := svc.currentAdaptiveBatchSize(); grown <= lowered {
		t.Fatalf("expected adaptive batch to recover from %d, got %d", lowered, grown)
	}
}

func TestBackfillFloodCacheExpires(t *testing.T) {
	svc := NewService("test-session")
	svc.noteAdaptiveBatchFlood(202, 3*time.Second)
	if !svc.backfillFloodBlocked(202) {
		t.Fatal("expected chat to be blocked by flood cache")
	}

	svc.throttleMu.Lock()
	svc.floodUntilByChat[202] = time.Now().Add(-1 * time.Second)
	svc.throttleMu.Unlock()

	if svc.backfillFloodBlocked(202) {
		t.Fatal("expected expired flood cache entry to be cleared")
	}
}

func TestIsReactionUnavailable(t *testing.T) {
	if !isReactionUnavailable(tgerr.New(400, "CHAT_REACTIONS_UNAVAILABLE")) {
		t.Fatal("expected unavailable reaction error to be skipped")
	}
	if !isReactionUnavailable(tgerr.New(400, "REACTION_INVALID")) {
		t.Fatal("expected invalid reaction error to be skipped")
	}
	if isReactionUnavailable(errors.New("plain error")) {
		t.Fatal("expected plain error to not be treated as reaction-unavailable")
	}
}

func TestIsRecoverableDialogLookupError(t *testing.T) {
	if !isRecoverableDialogLookupError(errors.New("callback: get offset peer: chat 123 not found")) {
		t.Fatal("expected offset-peer lookup error to be recoverable")
	}
	if isRecoverableDialogLookupError(errors.New("random failure")) {
		t.Fatal("expected random error to be non-recoverable")
	}
}
