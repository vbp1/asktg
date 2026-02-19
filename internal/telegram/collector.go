package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"asktg/internal/domain"

	tdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
)

const channelChatIDOffset int64 = 1_000_000_000_000
const historyBatchSize = 100
const minHistoryBatchSize = 20
const historyBatchStep = 10
const historySuccessBumpThreshold = 6
const backfillGlobalMinInterval = 333 * time.Millisecond
const backfillPerChatMinInterval = 1400 * time.Millisecond
const floodCacheGrace = 1 * time.Second
const maxFloodCacheWait = 15 * time.Minute

var (
	ErrNotConfigured  = errors.New("telegram api credentials are not configured")
	ErrCodeNotPending = errors.New("telegram login code was not requested")
	ErrPasswordNeeded = errors.New("telegram password is required")
	ErrUnauthorized   = errors.New("telegram session is not authorized")
	urlPattern        = regexp.MustCompile(`https?://`)
)

type AuthStatus struct {
	Configured   bool
	Authorized   bool
	AwaitingCode bool
	Phone        string
	UserDisplay  string
}

type Dialog struct {
	ChatID int64
	Title  string
	Type   string
}

type SyncChatState struct {
	ChatID          int64
	SyncCursor      string
	LastMessageUnix int64
}

type SyncedMessage struct {
	ChatID        int64
	MsgID         int64
	Timestamp     int64
	EditTS        int64
	SenderID      int64
	SenderDisplay string
	Text          string
	HasURL        bool
	IsOutgoing    bool
}

type MessageReaction struct {
	ChatID int64
	MsgID  int64
	Emoji  string
}

type SyncedFile struct {
	ChatID        int64
	MsgID         int64
	Timestamp     int64
	DocumentID    int64
	AccessHash    int64
	FileReference []byte
	DCID          int
	FileName      string
	MimeType      string
	Size          int64
}

type ChatSyncResult struct {
	ChatID          int64
	NextCursor      string
	LastMessageUnix int64
	LastSyncedUnix  int64
	Upserted        int
	BackfillDone    bool
}

type SyncReport struct {
	SyncedAtUnix int64
	Chats        []ChatSyncResult
	Messages     []SyncedMessage
	Files        []SyncedFile
	Metrics      SyncMetrics
}

type SyncMetrics struct {
	HistoryRequests   int   `json:"history_requests"`
	FloodWaitEvents   int   `json:"flood_wait_events"`
	FloodWaitSeconds  int64 `json:"flood_wait_seconds"`
	BackfillSleeps    int   `json:"backfill_sleeps"`
	BackfillSleepMS   int64 `json:"backfill_sleep_ms"`
	FloodSkippedChats int   `json:"flood_skipped_chats"`
	BatchMin          int   `json:"batch_min"`
	BatchMax          int   `json:"batch_max"`
	BatchCurrent      int   `json:"batch_current"`
	StartedAtUnix     int64 `json:"started_at_unix"`
	CompletedAtUnix   int64 `json:"completed_at_unix"`
}

type LiveEventKind string

const (
	LiveEventUpsert LiveEventKind = "upsert"
	LiveEventDelete LiveEventKind = "delete"
)

type LiveEvent struct {
	Kind    LiveEventKind
	Message SyncedMessage
	Files   []SyncedFile
	ChatID  int64
	MsgID   int64
}

type Service struct {
	sessionPath string

	mu           sync.RWMutex
	runMu        sync.Mutex
	throttleMu   sync.Mutex
	qrMu         sync.Mutex
	apiID        int
	apiHash      string
	pendingPhone string
	pendingHash  string
	qrCancel     context.CancelFunc
	qrPasswordCh chan string

	backfillLastGlobalReqAt time.Time
	backfillLastReqByChat   map[int64]time.Time
	floodUntilByChat        map[int64]time.Time
	adaptiveBatchSize       int
	adaptiveSuccessStreak   int
}

func NewService(sessionPath string) *Service {
	return &Service{
		sessionPath:           sessionPath,
		backfillLastReqByChat: map[int64]time.Time{},
		floodUntilByChat:      map[int64]time.Time{},
		adaptiveBatchSize:     historyBatchSize,
	}
}

func (s *Service) Configure(apiID int, apiHash string) error {
	apiHash = strings.TrimSpace(apiHash)
	if apiID <= 0 || apiHash == "" {
		return ErrNotConfigured
	}

	s.mu.Lock()
	s.apiID = apiID
	s.apiHash = apiHash
	s.mu.Unlock()
	return nil
}

func (s *Service) AuthStatus(ctx context.Context) (AuthStatus, error) {
	status := AuthStatus{}
	apiID, apiHash, err := s.credentials()
	if err != nil {
		status.AwaitingCode, status.Phone = s.pending()
		return status, nil
	}

	status.Configured = true
	status.AwaitingCode, status.Phone = s.pending()
	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		status.Authorized = authStatus.Authorized
		if authStatus.User != nil {
			status.UserDisplay = formatUserDisplay(authStatus.User)
		}
		return nil
	})
	if err != nil {
		return status, err
	}
	return status, nil
}

func (s *Service) RequestCode(ctx context.Context, phone string) (AuthStatus, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return AuthStatus{}, errors.New("telegram phone is required")
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return AuthStatus{}, err
	}

	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		current, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if current.Authorized {
			s.clearPending()
			return nil
		}

		sentCode, sendErr := client.Auth().SendCode(runCtx, phone, auth.SendCodeOptions{})
		if sendErr != nil {
			return sendErr
		}

		switch sent := sentCode.(type) {
		case *tg.AuthSentCode:
			s.setPending(phone, sent.PhoneCodeHash)
		case *tg.AuthSentCodeSuccess:
			s.clearPending()
		default:
			return fmt.Errorf("unexpected send code result type: %T", sentCode)
		}
		return nil
	})
	if err != nil {
		return AuthStatus{}, err
	}

	return s.AuthStatus(ctx)
}

func (s *Service) SignIn(ctx context.Context, code, password string) (AuthStatus, error) {
	code = strings.TrimSpace(code)
	password = strings.TrimSpace(password)
	if code == "" {
		return AuthStatus{}, errors.New("telegram login code is required")
	}

	phone, hash, ok := s.pendingCode()
	if !ok {
		return AuthStatus{}, ErrCodeNotPending
	}
	apiID, apiHash, err := s.credentials()
	if err != nil {
		return AuthStatus{}, err
	}

	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		_, signInErr := client.Auth().SignIn(runCtx, phone, code, hash)
		if errors.Is(signInErr, auth.ErrPasswordAuthNeeded) {
			if password == "" {
				return ErrPasswordNeeded
			}
			_, pwdErr := client.Auth().Password(runCtx, password)
			if pwdErr != nil {
				return pwdErr
			}
			return nil
		}
		return signInErr
	})
	if err != nil {
		return AuthStatus{}, err
	}

	s.clearPending()
	return s.AuthStatus(ctx)
}

func (s *Service) QRLogin(ctx context.Context, showQR func(token domain.TelegramQRToken) error) (AuthStatus, error) {
	apiID, apiHash, err := s.credentials()
	if err != nil {
		return AuthStatus{}, err
	}

	s.runMu.Lock()
	defer s.runMu.Unlock()

	qrCtx, qrCancel := context.WithCancel(ctx)
	defer qrCancel()

	s.qrMu.Lock()
	s.qrCancel = qrCancel
	s.qrMu.Unlock()
	defer func() {
		s.qrMu.Lock()
		s.qrCancel = nil
		s.qrMu.Unlock()
	}()

	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)

	var authResult AuthStatus
	err = s.withClientUsingOptions(qrCtx, apiID, apiHash, tdtelegram.Options{
		SessionStorage: &SafeFileSessionStorage{
			Path: s.sessionPath,
		},
		UpdateHandler: dispatcher,
	}, func(runCtx context.Context, client *tdtelegram.Client) error {
		status, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if status.Authorized {
			authResult.Configured = true
			authResult.Authorized = true
			if status.User != nil {
				authResult.UserDisplay = formatUserDisplay(status.User)
			}
			return nil
		}

		_, authErr := client.QR().Auth(runCtx, loggedIn, func(_ context.Context, token qrlogin.Token) error {
			code, codeErr := qr.Encode(token.URL(), qr.M)
			if codeErr != nil {
				return codeErr
			}
			dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
			return showQR(domain.TelegramQRToken{
				DataURI:   dataURI,
				ExpiresAt: token.Expires().Unix(),
			})
		})
		if authErr != nil {
			if !isPasswordNeeded(authErr) {
				return authErr
			}
			if notifyErr := showQR(domain.TelegramQRToken{PasswordNeeded: true}); notifyErr != nil {
				return notifyErr
			}
			passwordCh := s.getPasswordCh()
			var password string
			select {
			case password = <-passwordCh:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			if _, pwdErr := client.Auth().Password(runCtx, password); pwdErr != nil {
				return pwdErr
			}
		}

		newStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		authResult.Configured = true
		authResult.Authorized = newStatus.Authorized
		if newStatus.User != nil {
			authResult.UserDisplay = formatUserDisplay(newStatus.User)
		}
		return nil
	})
	if err != nil {
		return AuthStatus{}, err
	}
	return authResult, nil
}

func (s *Service) CancelQRLogin() {
	s.qrMu.Lock()
	defer s.qrMu.Unlock()
	if s.qrCancel != nil {
		s.qrCancel()
	}
}

func (s *Service) SubmitQRPassword(password string) {
	s.qrMu.Lock()
	ch := s.qrPasswordCh
	s.qrMu.Unlock()
	if ch != nil {
		select {
		case ch <- password:
		default:
		}
	}
}

func (s *Service) getPasswordCh() chan string {
	s.qrMu.Lock()
	defer s.qrMu.Unlock()
	if s.qrPasswordCh == nil {
		s.qrPasswordCh = make(chan string, 1)
	} else {
		// drain stale value
		select {
		case <-s.qrPasswordCh:
		default:
		}
	}
	return s.qrPasswordCh
}

func isPasswordNeeded(err error) bool {
	if errors.Is(err, auth.ErrPasswordAuthNeeded) {
		return true
	}
	if rpcErr, ok := tgerr.As(err); ok {
		return rpcErr.IsOneOf("SESSION_PASSWORD_NEEDED")
	}
	return false
}

func (s *Service) ListDialogs(ctx context.Context) ([]Dialog, error) {
	apiID, apiHash, err := s.credentials()
	if err != nil {
		return nil, err
	}

	dialogMap := map[int64]Dialog{}
	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		resolved, collectErr := collectDialogLookup(runCtx, client)
		if collectErr != nil {
			return collectErr
		}
		for chatID, entry := range resolved {
			dialogMap[chatID] = entry.dialog
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make([]Dialog, 0, len(dialogMap))
	for _, item := range dialogMap {
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) SyncChats(ctx context.Context, chats []SyncChatState, maxPerChat int) (SyncReport, error) {
	startedAt := time.Now().Unix()
	report := SyncReport{
		SyncedAtUnix: startedAt,
		Chats:        make([]ChatSyncResult, 0, len(chats)),
		Messages:     make([]SyncedMessage, 0, len(chats)*historyBatchSize),
		Files:        make([]SyncedFile, 0, len(chats)),
		Metrics: SyncMetrics{
			StartedAtUnix: startedAt,
			BatchCurrent:  s.currentAdaptiveBatchSize(),
		},
	}
	if len(chats) == 0 {
		report.Metrics.CompletedAtUnix = time.Now().Unix()
		return report, nil
	}

	if maxPerChat <= 0 {
		maxPerChat = 600
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return report, err
	}
	runMetrics := &syncRunMetrics{}

	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		dialogLookup, collectErr := collectDialogLookup(runCtx, client)
		if collectErr != nil {
			return collectErr
		}

		for _, state := range chats {
			resolved, ok := dialogLookup[state.ChatID]
			if !ok {
				report.Chats = append(report.Chats, ChatSyncResult{
					ChatID:          state.ChatID,
					NextCursor:      state.SyncCursor,
					LastMessageUnix: state.LastMessageUnix,
					LastSyncedUnix:  time.Now().Unix(),
					BackfillDone:    strings.TrimSpace(state.SyncCursor) == "",
				})
				continue
			}

			result, syncedMessages, syncedFiles, syncErr := s.syncSingleChat(runCtx, client.API(), resolved, state, maxPerChat, runMetrics)
			if syncErr != nil {
				return syncErr
			}
			report.Chats = append(report.Chats, result)
			report.Messages = append(report.Messages, syncedMessages...)
			report.Files = append(report.Files, syncedFiles...)
		}
		return nil
	})

	if err != nil {
		return report, err
	}
	report.SyncedAtUnix = time.Now().Unix()
	report.Metrics = runMetrics.toPublic(s.currentAdaptiveBatchSize(), startedAt, report.SyncedAtUnix)
	return report, nil
}

// MarkChatsRead marks indexed messages as read in Telegram.
// Broadcast channels are intentionally skipped.
func (s *Service) MarkChatsRead(ctx context.Context, chatMaxMsgID map[int64]int64) error {
	if len(chatMaxMsgID) == 0 {
		return nil
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return err
	}

	return s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		dialogLookup, collectErr := collectDialogLookup(runCtx, client)
		if collectErr != nil {
			return collectErr
		}

		var joinedErr error
		for chatID, maxMsgID := range chatMaxMsgID {
			if maxMsgID <= 0 {
				continue
			}

			resolved, ok := dialogLookup[chatID]
			if !ok || !shouldMarkDialogRead(resolved.dialog) {
				continue
			}

			if err := markPeerRead(runCtx, client.API(), resolved.peer, int(maxMsgID)); err != nil {
				joinedErr = errors.Join(joinedErr, fmt.Errorf("chat %d: %w", chatID, err))
			}
		}
		return joinedErr
	})
}

func (s *Service) ReactToMessages(ctx context.Context, reactions []MessageReaction) error {
	if len(reactions) == 0 {
		return nil
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return err
	}

	uniq := make(map[string]MessageReaction, len(reactions))
	for _, item := range reactions {
		emoji := strings.TrimSpace(item.Emoji)
		if item.ChatID == 0 || item.MsgID <= 0 || emoji == "" {
			continue
		}
		key := strconv.FormatInt(item.ChatID, 10) + ":" + strconv.FormatInt(item.MsgID, 10) + ":" + emoji
		uniq[key] = item
	}
	if len(uniq) == 0 {
		return nil
	}

	return s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		dialogLookup, collectErr := collectDialogLookup(runCtx, client)
		if collectErr != nil {
			return collectErr
		}

		var joinedErr error
		for _, item := range uniq {
			resolved, ok := dialogLookup[item.ChatID]
			if !ok {
				continue
			}
			if err := sendEmojiReaction(runCtx, client.API(), resolved.peer, int(item.MsgID), item.Emoji); err != nil {
				if isReactionUnavailable(err) {
					continue
				}
				joinedErr = errors.Join(joinedErr, fmt.Errorf("chat %d msg %d: %w", item.ChatID, item.MsgID, err))
			}
		}
		return joinedErr
	})
}

func (s *Service) RunRealtime(ctx context.Context, chatIDs []int64, onEvent func(LiveEvent) error) error {
	if onEvent == nil {
		return errors.New("onEvent callback is required")
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return err
	}

	filter := make(map[int64]struct{}, len(chatIDs))
	for _, id := range chatIDs {
		filter[id] = struct{}{}
	}

	knownMsgChats := map[int64]map[int64]struct{}{}
	dispatcher := tg.NewUpdateDispatcher()

	handleMessage := func(msgClass tg.MessageClass, entities tg.Entities) error {
		msg, ok := msgClass.(*tg.Message)
		if !ok || msg == nil {
			return nil
		}
		chatID, ok := peerToChatID(msg.GetPeerID())
		if !ok {
			return nil
		}
		if len(filter) > 0 {
			if _, exists := filter[chatID]; !exists {
				return nil
			}
		}

		synced := toSyncedMessage(chatID, msg, buildEntityLookupFromUpdate(entities))
		files := extractSyncedPDFFiles(chatID, msg)
		registerKnownMessage(knownMsgChats, synced.MsgID, chatID)
		return onEvent(LiveEvent{
			Kind:    LiveEventUpsert,
			Message: synced,
			Files:   files,
			ChatID:  synced.ChatID,
			MsgID:   synced.MsgID,
		})
	}

	dispatcher.OnNewMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return handleMessage(u.Message, e)
	})
	dispatcher.OnNewChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateNewChannelMessage) error {
		return handleMessage(u.Message, e)
	})
	dispatcher.OnEditMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditMessage) error {
		return handleMessage(u.Message, e)
	})
	dispatcher.OnEditChannelMessage(func(_ context.Context, e tg.Entities, u *tg.UpdateEditChannelMessage) error {
		return handleMessage(u.Message, e)
	})

	dispatcher.OnDeleteChannelMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteChannelMessages) error {
		chatID := -(channelChatIDOffset + u.ChannelID)
		if len(filter) > 0 {
			if _, exists := filter[chatID]; !exists {
				return nil
			}
		}
		for _, msgID := range u.Messages {
			if err := onEvent(LiveEvent{
				Kind:   LiveEventDelete,
				ChatID: chatID,
				MsgID:  int64(msgID),
			}); err != nil {
				return err
			}
		}
		return nil
	})

	dispatcher.OnDeleteMessages(func(_ context.Context, _ tg.Entities, u *tg.UpdateDeleteMessages) error {
		for _, msgID := range u.Messages {
			chatSet, ok := knownMsgChats[int64(msgID)]
			if !ok {
				continue
			}
			for chatID := range chatSet {
				if len(filter) > 0 {
					if _, exists := filter[chatID]; !exists {
						continue
					}
				}
				if err := onEvent(LiveEvent{
					Kind:   LiveEventDelete,
					ChatID: chatID,
					MsgID:  int64(msgID),
				}); err != nil {
					return err
				}
			}
		}
		return nil
	})

	manager := updates.New(updates.Config{
		Handler: dispatcher,
	})

	return s.withClientUsingOptions(ctx, apiID, apiHash, tdtelegram.Options{
		SessionStorage: &SafeFileSessionStorage{
			Path: s.sessionPath,
		},
		UpdateHandler: manager,
	}, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}
		self, selfErr := client.Self(runCtx)
		if selfErr != nil {
			return selfErr
		}
		return manager.Run(runCtx, client.API(), self.ID, updates.AuthOptions{
			IsBot: self.Bot,
		})
	})
}

type resolvedDialog struct {
	dialog Dialog
	peer   tg.InputPeerClass
}

func collectDialogLookup(ctx context.Context, client *tdtelegram.Client) (map[int64]resolvedDialog, error) {
	lookup := make(map[int64]resolvedDialog, 256)
	queryBuilder := query.GetDialogs(client.API()).BatchSize(100)
	err := queryBuilder.ForEach(ctx, func(_ context.Context, elem dialogs.Elem) error {
		dialog, ok := dialogFromElem(elem)
		if !ok || strings.TrimSpace(dialog.Title) == "" {
			return nil
		}
		lookup[dialog.ChatID] = resolvedDialog{
			dialog: dialog,
			peer:   elem.Peer,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return lookup, nil
}

type syncRunMetrics struct {
	historyRequests   int
	floodWaitEvents   int
	floodWaitSeconds  int64
	backfillSleeps    int
	backfillSleepMS   int64
	floodSkippedChats int
	batchMin          int
	batchMax          int
}

func (m *syncRunMetrics) recordRequest(limit int) {
	if m == nil || limit <= 0 {
		return
	}
	m.historyRequests++
	if m.batchMin == 0 || limit < m.batchMin {
		m.batchMin = limit
	}
	if limit > m.batchMax {
		m.batchMax = limit
	}
}

func (m *syncRunMetrics) recordFloodWait(wait time.Duration) {
	if m == nil {
		return
	}
	m.floodWaitEvents++
	seconds := int64(wait / time.Second)
	if wait%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	m.floodWaitSeconds += seconds
}

func (m *syncRunMetrics) recordFloodSkip() {
	if m == nil {
		return
	}
	m.floodSkippedChats++
}

func (m *syncRunMetrics) recordBackfillSleep(d time.Duration) {
	if m == nil || d <= 0 {
		return
	}
	m.backfillSleeps++
	m.backfillSleepMS += d.Milliseconds()
}

func (m *syncRunMetrics) toPublic(currentBatch int, startedAtUnix int64, completedAtUnix int64) SyncMetrics {
	if currentBatch <= 0 {
		currentBatch = historyBatchSize
	}
	out := SyncMetrics{
		HistoryRequests:   m.historyRequests,
		FloodWaitEvents:   m.floodWaitEvents,
		FloodWaitSeconds:  m.floodWaitSeconds,
		BackfillSleeps:    m.backfillSleeps,
		BackfillSleepMS:   m.backfillSleepMS,
		FloodSkippedChats: m.floodSkippedChats,
		BatchCurrent:      currentBatch,
		StartedAtUnix:     startedAtUnix,
		CompletedAtUnix:   completedAtUnix,
	}
	if m.batchMin > 0 {
		out.BatchMin = m.batchMin
	} else {
		out.BatchMin = currentBatch
	}
	if m.batchMax > 0 {
		out.BatchMax = m.batchMax
	} else {
		out.BatchMax = currentBatch
	}
	return out
}

func (s *Service) currentAdaptiveBatchSize() int {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()
	return s.currentAdaptiveBatchSizeLocked()
}

func (s *Service) currentAdaptiveBatchSizeLocked() int {
	if s.adaptiveBatchSize <= 0 || s.adaptiveBatchSize > historyBatchSize {
		s.adaptiveBatchSize = historyBatchSize
	}
	if s.adaptiveBatchSize < minHistoryBatchSize {
		s.adaptiveBatchSize = minHistoryBatchSize
	}
	return s.adaptiveBatchSize
}

func (s *Service) noteAdaptiveBatchSuccess() {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()
	current := s.currentAdaptiveBatchSizeLocked()
	if current >= historyBatchSize {
		s.adaptiveBatchSize = historyBatchSize
		s.adaptiveSuccessStreak = 0
		return
	}
	s.adaptiveSuccessStreak++
	if s.adaptiveSuccessStreak < historySuccessBumpThreshold {
		return
	}
	next := current + historyBatchStep
	if next > historyBatchSize {
		next = historyBatchSize
	}
	s.adaptiveBatchSize = next
	s.adaptiveSuccessStreak = 0
}

func (s *Service) noteAdaptiveBatchFlood(chatID int64, wait time.Duration) time.Time {
	if wait <= 0 {
		wait = 3 * time.Second
	}
	if wait > maxFloodCacheWait {
		wait = maxFloodCacheWait
	}
	until := time.Now().Add(wait + floodCacheGrace)

	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()
	if s.floodUntilByChat == nil {
		s.floodUntilByChat = make(map[int64]time.Time, 64)
	}
	if existing, ok := s.floodUntilByChat[chatID]; ok && existing.After(until) {
		until = existing
	} else {
		s.floodUntilByChat[chatID] = until
	}

	current := s.currentAdaptiveBatchSizeLocked()
	next := current / 2
	if next < minHistoryBatchSize {
		next = minHistoryBatchSize
	}
	s.adaptiveBatchSize = next
	s.adaptiveSuccessStreak = 0
	return until
}

func (s *Service) backfillFloodBlocked(chatID int64) bool {
	s.throttleMu.Lock()
	defer s.throttleMu.Unlock()
	if s.floodUntilByChat == nil {
		return false
	}
	until, ok := s.floodUntilByChat[chatID]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(s.floodUntilByChat, chatID)
		return false
	}
	return true
}

func (s *Service) waitBackfillLimiter(ctx context.Context, chatID int64, metrics *syncRunMetrics) error {
	for {
		wait := time.Duration(0)
		now := time.Now()

		s.throttleMu.Lock()
		if s.backfillLastReqByChat == nil {
			s.backfillLastReqByChat = make(map[int64]time.Time, 64)
		}
		if since := now.Sub(s.backfillLastGlobalReqAt); since < backfillGlobalMinInterval {
			wait = backfillGlobalMinInterval - since
		}
		if last, ok := s.backfillLastReqByChat[chatID]; ok {
			if since := now.Sub(last); since < backfillPerChatMinInterval {
				perChatWait := backfillPerChatMinInterval - since
				if perChatWait > wait {
					wait = perChatWait
				}
			}
		}
		if wait <= 0 {
			s.backfillLastGlobalReqAt = now
			s.backfillLastReqByChat[chatID] = now
			s.throttleMu.Unlock()
			return nil
		}
		s.throttleMu.Unlock()

		metrics.recordBackfillSleep(wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Service) syncSingleChat(ctx context.Context, api *tg.Client, dialog resolvedDialog, state SyncChatState, maxPerChat int, metrics *syncRunMetrics) (ChatSyncResult, []SyncedMessage, []SyncedFile, error) {
	lastSyncedUnix := time.Now().Unix()
	result := ChatSyncResult{
		ChatID:          state.ChatID,
		NextCursor:      strings.TrimSpace(state.SyncCursor),
		LastMessageUnix: state.LastMessageUnix,
		LastSyncedUnix:  lastSyncedUnix,
		Upserted:        0,
		BackfillDone:    strings.TrimSpace(state.SyncCursor) == "",
	}

	remaining := maxPerChat
	messages := make([]SyncedMessage, 0, minInt(historyBatchSize, maxPerChat))
	files := make([]SyncedFile, 0, minInt(historyBatchSize, maxPerChat))
	lastKnown := state.LastMessageUnix
	tailMinID := 0
	hitKnown := false
	chatID := dialog.dialog.ChatID

	offsetID := 0
	for remaining > 0 {
		requestLimit := minInt(s.currentAdaptiveBatchSize(), remaining)
		if requestLimit <= 0 {
			break
		}
		metrics.recordRequest(requestLimit)
		page, pageErr := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:       dialog.peer,
			OffsetID:   offsetID,
			OffsetDate: 0,
			AddOffset:  0,
			Limit:      requestLimit,
			MaxID:      0,
			MinID:      0,
			Hash:       0,
		})
		if pageErr != nil {
			if wait, ok := tgerr.AsFloodWait(pageErr); ok {
				s.noteAdaptiveBatchFlood(chatID, wait)
				metrics.recordFloodWait(wait)
				return result, messages, files, nil
			}
			return result, nil, nil, pageErr
		}
		s.noteAdaptiveBatchSuccess()
		modified, ok := page.AsModified()
		if !ok {
			break
		}

		pageMessages := modified.GetMessages()
		if len(pageMessages) == 0 {
			break
		}
		entities := buildEntityLookup(modified.GetUsers(), modified.GetChats())

		pageMinID := 0
		for _, msgClass := range pageMessages {
			msg, ok := msgClass.(*tg.Message)
			if !ok {
				continue
			}
			if msg.ID > 0 && (pageMinID == 0 || msg.ID < pageMinID) {
				pageMinID = msg.ID
			}
			if lastKnown > 0 && int64(msg.Date) <= lastKnown {
				hitKnown = true
				continue
			}

			synced := toSyncedMessage(dialog.dialog.ChatID, msg, entities)
			messages = append(messages, synced)
			files = append(files, extractSyncedPDFFiles(dialog.dialog.ChatID, msg)...)
			result.Upserted++
			if synced.Timestamp > result.LastMessageUnix {
				result.LastMessageUnix = synced.Timestamp
			}

			remaining--
			if remaining <= 0 {
				break
			}
		}

		if pageMinID <= 0 {
			break
		}
		tailMinID = pageMinID
		if hitKnown || len(pageMessages) < requestLimit {
			break
		}
		if offsetID == pageMinID {
			break
		}
		offsetID = pageMinID
	}

	backfillOffset, hasCursor := parseCursor(state.SyncCursor)
	if !hasCursor && lastKnown == 0 && tailMinID > 0 && remaining <= 0 {
		backfillOffset = tailMinID
		hasCursor = true
	}
	if !hasCursor {
		result.NextCursor = ""
		result.BackfillDone = true
		return result, messages, files, nil
	}

	result.BackfillDone = false
	if s.backfillFloodBlocked(chatID) {
		result.NextCursor = strconv.Itoa(backfillOffset)
		metrics.recordFloodSkip()
		return result, messages, files, nil
	}
	for remaining > 0 {
		if s.backfillFloodBlocked(chatID) {
			result.NextCursor = strconv.Itoa(backfillOffset)
			metrics.recordFloodSkip()
			return result, messages, files, nil
		}
		if err := s.waitBackfillLimiter(ctx, chatID, metrics); err != nil {
			return result, nil, nil, err
		}
		requestLimit := minInt(s.currentAdaptiveBatchSize(), remaining)
		if requestLimit <= 0 {
			break
		}
		metrics.recordRequest(requestLimit)
		page, pageErr := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:       dialog.peer,
			OffsetID:   backfillOffset,
			OffsetDate: 0,
			AddOffset:  0,
			Limit:      requestLimit,
			MaxID:      0,
			MinID:      0,
			Hash:       0,
		})
		if pageErr != nil {
			if wait, ok := tgerr.AsFloodWait(pageErr); ok {
				s.noteAdaptiveBatchFlood(chatID, wait)
				metrics.recordFloodWait(wait)
				result.NextCursor = strconv.Itoa(backfillOffset)
				return result, messages, files, nil
			}
			return result, nil, nil, pageErr
		}
		s.noteAdaptiveBatchSuccess()
		modified, ok := page.AsModified()
		if !ok {
			result.NextCursor = ""
			result.BackfillDone = true
			return result, messages, files, nil
		}

		pageMessages := modified.GetMessages()
		if len(pageMessages) == 0 {
			result.NextCursor = ""
			result.BackfillDone = true
			return result, messages, files, nil
		}
		entities := buildEntityLookup(modified.GetUsers(), modified.GetChats())

		pageMinID := 0
		for _, msgClass := range pageMessages {
			msg, ok := msgClass.(*tg.Message)
			if !ok {
				continue
			}
			if msg.ID > 0 && (pageMinID == 0 || msg.ID < pageMinID) {
				pageMinID = msg.ID
			}

			synced := toSyncedMessage(dialog.dialog.ChatID, msg, entities)
			messages = append(messages, synced)
			files = append(files, extractSyncedPDFFiles(dialog.dialog.ChatID, msg)...)
			result.Upserted++
			if synced.Timestamp > result.LastMessageUnix {
				result.LastMessageUnix = synced.Timestamp
			}

			remaining--
			if remaining <= 0 {
				break
			}
		}

		if pageMinID <= 0 || pageMinID == backfillOffset {
			result.NextCursor = ""
			result.BackfillDone = true
			return result, messages, files, nil
		}

		if len(pageMessages) < requestLimit {
			result.NextCursor = ""
			result.BackfillDone = true
			return result, messages, files, nil
		}
		backfillOffset = pageMinID
	}

	result.NextCursor = strconv.Itoa(backfillOffset)
	return result, messages, files, nil
}

func shouldMarkDialogRead(dialog Dialog) bool {
	return strings.ToLower(strings.TrimSpace(dialog.Type)) != "channel"
}

func markPeerRead(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, maxMsgID int) error {
	if api == nil || peer == nil || maxMsgID <= 0 {
		return nil
	}

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		_, err := api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{
			Channel: &tg.InputChannel{
				ChannelID:  p.ChannelID,
				AccessHash: p.AccessHash,
			},
			MaxID: maxMsgID,
		})
		return err
	default:
		_, err := api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{
			Peer:  peer,
			MaxID: maxMsgID,
		})
		return err
	}
}

func sendEmojiReaction(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgID int, emoji string) error {
	if api == nil || peer == nil || msgID <= 0 || strings.TrimSpace(emoji) == "" {
		return nil
	}
	_, err := api.MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		Peer:  peer,
		MsgID: msgID,
		Reaction: []tg.ReactionClass{
			&tg.ReactionEmoji{Emoticon: emoji},
		},
	})
	return err
}

func isReactionUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if wait, ok := tgerr.AsFloodWait(err); ok && wait > 0 {
		return true
	}
	if rpcErr, ok := tgerr.As(err); ok {
		return rpcErr.IsOneOf(
			"REACTION_INVALID",
			"REACTIONS_TOO_MANY",
			"CHAT_REACTIONS_UNAVAILABLE",
			"CHAT_SEND_REACTIONS_FORBIDDEN",
			"REACTION_EMPTY",
			"MESSAGE_ID_INVALID",
			"TOPIC_DELETED",
		)
	}
	return false
}

type entityLookup struct {
	users    map[int64]*tg.User
	chats    map[int64]*tg.Chat
	channels map[int64]*tg.Channel
}

func buildEntityLookup(users []tg.UserClass, chats []tg.ChatClass) entityLookup {
	lookup := entityLookup{
		users:    make(map[int64]*tg.User, len(users)),
		chats:    map[int64]*tg.Chat{},
		channels: map[int64]*tg.Channel{},
	}
	for _, userClass := range users {
		user, ok := userClass.(*tg.User)
		if ok && user != nil {
			lookup.users[user.ID] = user
		}
	}
	for _, chatClass := range chats {
		switch entry := chatClass.(type) {
		case *tg.Chat:
			if entry != nil {
				lookup.chats[entry.ID] = entry
			}
		case *tg.Channel:
			if entry != nil {
				lookup.channels[entry.ID] = entry
			}
		}
	}
	return lookup
}

func toSyncedMessage(chatID int64, msg *tg.Message, entities entityLookup) SyncedMessage {
	senderID, sender := resolveSender(msg, entities)
	return SyncedMessage{
		ChatID:        chatID,
		MsgID:         int64(msg.ID),
		Timestamp:     int64(msg.Date),
		EditTS:        int64(msg.EditDate),
		SenderID:      senderID,
		SenderDisplay: sender,
		Text:          msg.Message,
		HasURL:        containsURL(msg.Message),
		IsOutgoing:    msg.Out,
	}
}

func resolveSender(msg *tg.Message, entities entityLookup) (int64, string) {
	if msg == nil {
		return 0, ""
	}
	if peer, ok := msg.GetFromID(); ok {
		switch from := peer.(type) {
		case *tg.PeerUser:
			if user, ok := entities.users[from.UserID]; ok && user != nil {
				if user.Self {
					return from.UserID, "You"
				}
				return from.UserID, formatUserDisplay(user)
			}
			return from.UserID, fmt.Sprintf("User %d", from.UserID)
		case *tg.PeerChat:
			if chat, ok := entities.chats[from.ChatID]; ok && chat != nil && strings.TrimSpace(chat.Title) != "" {
				return -from.ChatID, chat.Title
			}
			return -from.ChatID, fmt.Sprintf("Chat %d", from.ChatID)
		case *tg.PeerChannel:
			if channel, ok := entities.channels[from.ChannelID]; ok && channel != nil && strings.TrimSpace(channel.Title) != "" {
				return -(channelChatIDOffset + from.ChannelID), channel.Title
			}
			return -(channelChatIDOffset + from.ChannelID), fmt.Sprintf("Channel %d", from.ChannelID)
		}
	}

	if msg.Out {
		return 0, "You"
	}
	if postAuthor, ok := msg.GetPostAuthor(); ok && strings.TrimSpace(postAuthor) != "" {
		return 0, postAuthor
	}
	return 0, ""
}

func containsURL(text string) bool {
	return urlPattern.MatchString(text)
}

func extractSyncedPDFFiles(chatID int64, msg *tg.Message) []SyncedFile {
	if msg == nil || msg.Media == nil {
		return nil
	}
	media, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || media == nil || media.Document == nil {
		return nil
	}
	doc, ok := media.Document.(*tg.Document)
	if !ok || doc == nil {
		return nil
	}

	filename := documentFilename(doc.Attributes)
	mime := strings.TrimSpace(doc.MimeType)
	if !looksLikePDFDocument(filename, mime) {
		return nil
	}

	return []SyncedFile{{
		ChatID:        chatID,
		MsgID:         int64(msg.ID),
		Timestamp:     int64(msg.Date),
		DocumentID:    doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		DCID:          doc.DCID,
		FileName:      strings.TrimSpace(filename),
		MimeType:      mime,
		Size:          doc.Size,
	}}
}

func looksLikePDFDocument(filename, mime string) bool {
	if strings.EqualFold(strings.TrimSpace(mime), "application/pdf") {
		return true
	}
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return false
	}
	return strings.EqualFold(path.Ext(filename), ".pdf")
}

func documentFilename(attrs []tg.DocumentAttributeClass) string {
	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		if named, ok := attr.(*tg.DocumentAttributeFilename); ok && named != nil {
			return named.FileName
		}
	}
	return ""
}

func peerToChatID(peer tg.PeerClass) (int64, bool) {
	switch p := peer.(type) {
	case *tg.PeerUser:
		return p.UserID, true
	case *tg.PeerChat:
		return -p.ChatID, true
	case *tg.PeerChannel:
		return -(channelChatIDOffset + p.ChannelID), true
	default:
		return 0, false
	}
}

func inputPeerToChatID(peer tg.InputPeerClass, selfID int64) (int64, bool) {
	switch p := peer.(type) {
	case *tg.InputPeerSelf:
		if selfID <= 0 {
			return 0, false
		}
		return selfID, true
	case *tg.InputPeerUser:
		return p.UserID, true
	case *tg.InputPeerChat:
		return -p.ChatID, true
	case *tg.InputPeerChannel:
		return -(channelChatIDOffset + p.ChannelID), true
	default:
		return 0, false
	}
}

func buildEntityLookupFromUpdate(entities tg.Entities) entityLookup {
	lookup := entityLookup{
		users:    make(map[int64]*tg.User, len(entities.Users)),
		chats:    make(map[int64]*tg.Chat, len(entities.Chats)),
		channels: make(map[int64]*tg.Channel, len(entities.Channels)),
	}
	for id, user := range entities.Users {
		lookup.users[id] = user
	}
	for id, chat := range entities.Chats {
		lookup.chats[id] = chat
	}
	for id, channel := range entities.Channels {
		lookup.channels[id] = channel
	}
	return lookup
}

func registerKnownMessage(index map[int64]map[int64]struct{}, msgID int64, chatID int64) {
	if msgID <= 0 {
		return
	}
	chatSet, ok := index[msgID]
	if !ok {
		chatSet = map[int64]struct{}{}
		index[msgID] = chatSet
	}
	chatSet[chatID] = struct{}{}
}

func parseCursor(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func dialogFromElem(elem dialogs.Elem) (Dialog, bool) {
	switch peer := elem.Dialog.GetPeer().(type) {
	case *tg.PeerUser:
		user, ok := elem.Entities.User(peer.UserID)
		if !ok || user == nil {
			return Dialog{}, false
		}
		dialogType := "private"
		title := formatUserDisplay(user)
		if user.Self {
			dialogType = "saved"
			title = "Saved Messages"
		}
		return Dialog{
			ChatID: peer.UserID,
			Title:  title,
			Type:   dialogType,
		}, true

	case *tg.PeerChat:
		chat, ok := elem.Entities.Chat(peer.ChatID)
		if !ok || chat == nil {
			return Dialog{}, false
		}
		return Dialog{
			ChatID: -peer.ChatID,
			Title:  chat.Title,
			Type:   "group",
		}, true

	case *tg.PeerChannel:
		channel, ok := elem.Entities.Channel(peer.ChannelID)
		if !ok || channel == nil {
			return Dialog{}, false
		}
		dialogType := "channel"
		if channel.Megagroup {
			dialogType = "group"
		}
		return Dialog{
			ChatID: -(channelChatIDOffset + peer.ChannelID),
			Title:  channel.Title,
			Type:   dialogType,
		}, true
	}

	return Dialog{}, false
}

func formatUserDisplay(user *tg.User) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	if name != "" {
		return name
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	return fmt.Sprintf("User %d", user.ID)
}

func (s *Service) pendingCode() (phone string, hash string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.pendingPhone == "" || s.pendingHash == "" {
		return "", "", false
	}
	return s.pendingPhone, s.pendingHash, true
}

func (s *Service) pending() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingHash != "", s.pendingPhone
}

func (s *Service) setPending(phone, hash string) {
	s.mu.Lock()
	s.pendingPhone = phone
	s.pendingHash = hash
	s.mu.Unlock()
}

func (s *Service) ListChatFolders(ctx context.Context) ([]domain.ChatFolder, error) {
	apiID, apiHash, err := s.credentials()
	if err != nil {
		return nil, err
	}

	var folders []domain.ChatFolder
	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		self, selfErr := client.Self(runCtx)
		if selfErr != nil {
			return selfErr
		}

		chatIDsForFolder := func(folderID int) ([]int64, error) {
			q := dialogs.QueryFunc(func(qCtx context.Context, req dialogs.Request) (tg.MessagesDialogsClass, error) {
				r := &tg.MessagesGetDialogsRequest{
					Limit:      req.Limit,
					OffsetDate: req.OffsetDate,
					OffsetID:   req.OffsetID,
					OffsetPeer: req.OffsetPeer,
				}
				r.SetFolderID(folderID)
				return client.API().MessagesGetDialogs(qCtx, r)
			})

			iter := dialogs.NewIterator(q, 100)
			chatIDs := make([]int64, 0, 256)
			chatIDSet := make(map[int64]struct{}, 256)
			for iter.Next(runCtx) {
				elem := iter.Value()
				dialog, ok := elem.Dialog.(*tg.Dialog)
				if !ok || dialog == nil {
					continue
				}
				id, ok := peerToChatID(dialog.GetPeer())
				if !ok {
					continue
				}
				if _, seen := chatIDSet[id]; seen {
					continue
				}
				chatIDSet[id] = struct{}{}
				chatIDs = append(chatIDs, id)
			}
			if err := iter.Err(); err != nil {
				return nil, err
			}
			return chatIDs, nil
		}

		df, dfErr := client.API().MessagesGetDialogFilters(runCtx)
		if dfErr != nil {
			return dfErr
		}

		for _, raw := range df.Filters {
			var (
				id       int
				title    string
				emoticon string
				colorID  int
				pinnedIP []tg.InputPeerClass
				include  []tg.InputPeerClass
			)

			switch filter := raw.(type) {
			case *tg.DialogFilter:
				if filter == nil {
					continue
				}
				id = filter.ID
				title = strings.TrimSpace(filter.Title.Text)
				emoticon = strings.TrimSpace(filter.Emoticon)
				colorID = filter.Color
				pinnedIP = filter.PinnedPeers
				include = filter.IncludePeers
			case *tg.DialogFilterChatlist:
				if filter == nil {
					continue
				}
				id = filter.ID
				title = strings.TrimSpace(filter.Title.Text)
				emoticon = strings.TrimSpace(filter.Emoticon)
				colorID = filter.Color
				pinnedIP = filter.PinnedPeers
				include = filter.IncludePeers
			default:
				continue
			}

			if title == "" {
				title = fmt.Sprintf("Folder %d", id)
			}

			pinned := make([]int64, 0, len(pinnedIP))
			pinnedSet := make(map[int64]struct{}, len(pinnedIP))
			for _, peer := range pinnedIP {
				pid, ok := inputPeerToChatID(peer, self.ID)
				if !ok {
					continue
				}
				if _, seen := pinnedSet[pid]; seen {
					continue
				}
				pinnedSet[pid] = struct{}{}
				pinned = append(pinned, pid)
			}

			chatIDs, listErr := chatIDsForFolder(id)
			if listErr != nil {
				// Best-effort fallback: use pinned+included peers if server-side listing fails.
				seen := make(map[int64]struct{}, len(pinnedIP)+len(include))
				chatIDs = make([]int64, 0, len(pinnedIP)+len(include))
				for _, pid := range pinned {
					seen[pid] = struct{}{}
					chatIDs = append(chatIDs, pid)
				}
				for _, peer := range include {
					cid, ok := inputPeerToChatID(peer, self.ID)
					if !ok {
						continue
					}
					if _, ok := seen[cid]; ok {
						continue
					}
					seen[cid] = struct{}{}
					chatIDs = append(chatIDs, cid)
				}
			}

			// Put pinned chats at the front (preserve dialog order for the rest).
			if len(pinned) > 0 && len(chatIDs) > 0 {
				inPinned := make(map[int64]struct{}, len(pinned))
				for _, pid := range pinned {
					inPinned[pid] = struct{}{}
				}
				rest := make([]int64, 0, len(chatIDs))
				for _, cid := range chatIDs {
					if _, ok := inPinned[cid]; ok {
						continue
					}
					rest = append(rest, cid)
				}
				chatIDs = append(append([]int64{}, pinned...), rest...)
			}

			folders = append(folders, domain.ChatFolder{
				ID:            id,
				Title:         title,
				Emoticon:      emoticon,
				Color:         colorID,
				ChatIDs:       chatIDs,
				PinnedChatIDs: pinned,
			})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return folders, nil
}

func (s *Service) clearPending() {
	s.mu.Lock()
	s.pendingPhone = ""
	s.pendingHash = ""
	s.mu.Unlock()
}

func (s *Service) credentials() (int, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.apiID <= 0 || strings.TrimSpace(s.apiHash) == "" {
		return 0, "", ErrNotConfigured
	}
	return s.apiID, s.apiHash, nil
}

func (s *Service) withClient(ctx context.Context, apiID int, apiHash string, fn func(context.Context, *tdtelegram.Client) error) error {
	s.runMu.Lock()
	defer s.runMu.Unlock()

	return s.withClientUsingOptions(ctx, apiID, apiHash, tdtelegram.Options{
		SessionStorage: &SafeFileSessionStorage{
			Path: s.sessionPath,
		},
	}, fn)
}

func (s *Service) withClientUsingOptions(ctx context.Context, apiID int, apiHash string, opts tdtelegram.Options, fn func(context.Context, *tdtelegram.Client) error) error {
	if err := os.MkdirAll(filepath.Dir(s.sessionPath), 0o755); err != nil {
		return err
	}

	client := tdtelegram.NewClient(apiID, apiHash, opts)
	return client.Run(ctx, func(runCtx context.Context) error {
		return fn(runCtx, client)
	})
}
