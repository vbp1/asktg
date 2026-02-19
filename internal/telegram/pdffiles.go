package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	tdtelegram "github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

var (
	ErrFileTooLarge         = errors.New("file exceeds maximum size")
	ErrFileReferenceExpired = errors.New("telegram file reference expired")
)

func (s *Service) DownloadDocumentBytes(ctx context.Context, documentID int64, accessHash int64, fileReference []byte, maxBytes int64) ([]byte, error) {
	if documentID == 0 || accessHash == 0 {
		return nil, errors.New("document id and access hash are required")
	}
	if maxBytes <= 0 {
		return nil, errors.New("maxBytes must be > 0")
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return nil, err
	}

	var out []byte
	err = s.withClient(ctx, apiID, apiHash, func(runCtx context.Context, client *tdtelegram.Client) error {
		authStatus, statusErr := client.Auth().Status(runCtx)
		if statusErr != nil {
			return statusErr
		}
		if !authStatus.Authorized {
			return ErrUnauthorized
		}

		location := &tg.InputDocumentFileLocation{
			ID:            documentID,
			AccessHash:    accessHash,
			FileReference: fileReference,
		}

		var buf cappedBuffer
		buf.Max = maxBytes
		d := downloader.NewDownloader()
		_, downloadErr := d.Download(client.API(), location).Stream(runCtx, &buf)
		if downloadErr != nil {
			if isFileReferenceError(downloadErr) {
				return errors.Join(ErrFileReferenceExpired, downloadErr)
			}
			return downloadErr
		}
		out = buf.Bytes()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Service) FetchMessagePDFMeta(ctx context.Context, chatID int64, msgID int64) ([]SyncedFile, error) {
	if chatID == 0 || msgID == 0 {
		return nil, errors.New("chat id and msg id are required")
	}
	apiID, apiHash, err := s.credentials()
	if err != nil {
		return nil, err
	}

	var files []SyncedFile
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
		resolved, ok := dialogLookup[chatID]
		if !ok {
			return fmt.Errorf("chat %d not found in dialogs", chatID)
		}

		msg, fetchErr := fetchSingleMessage(runCtx, client.API(), resolved.peer, int(msgID))
		if fetchErr != nil {
			return fetchErr
		}
		files = extractSyncedPDFFiles(chatID, msg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Service) BackfillPDFFilesSince(ctx context.Context, chatIDs []int64, sinceUnix int64, maxPerChat int) ([]SyncedFile, error) {
	if len(chatIDs) == 0 {
		return nil, nil
	}
	if sinceUnix <= 0 {
		return nil, errors.New("sinceUnix must be > 0")
	}
	if maxPerChat <= 0 {
		maxPerChat = 2000
	}

	apiID, apiHash, err := s.credentials()
	if err != nil {
		return nil, err
	}

	filter := make(map[int64]struct{}, len(chatIDs))
	for _, id := range chatIDs {
		filter[id] = struct{}{}
	}

	out := make([]SyncedFile, 0, minInt(maxPerChat, 256))
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

		for chatID := range filter {
			resolved, ok := dialogLookup[chatID]
			if !ok {
				continue
			}

			remaining := maxPerChat
			offsetID := 0
			for remaining > 0 {
				page, pageErr := client.API().MessagesSearch(runCtx, &tg.MessagesSearchRequest{
					Peer:      resolved.peer,
					Q:         "",
					Filter:    &tg.InputMessagesFilterDocument{},
					MinDate:   int(sinceUnix),
					MaxDate:   0,
					OffsetID:  offsetID,
					AddOffset: 0,
					Limit:     minInt(historyBatchSize, remaining),
					MaxID:     0,
					MinID:     0,
					Hash:      0,
				})
				if pageErr != nil {
					return pageErr
				}
				modified, ok := page.AsModified()
				if !ok {
					break
				}

				pageMessages := modified.GetMessages()
				if len(pageMessages) == 0 {
					break
				}

				pageMinID := 0
				for _, msgClass := range pageMessages {
					msg, ok := msgClass.(*tg.Message)
					if !ok || msg == nil {
						continue
					}
					if msg.ID > 0 && (pageMinID == 0 || msg.ID < pageMinID) {
						pageMinID = msg.ID
					}
					files := extractSyncedPDFFiles(chatID, msg)
					if len(files) == 0 {
						continue
					}
					out = append(out, files...)
					remaining -= len(files)
					if remaining <= 0 {
						break
					}
				}

				if pageMinID <= 0 || pageMinID == offsetID {
					break
				}
				offsetID = pageMinID
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func fetchSingleMessage(ctx context.Context, api *tg.Client, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if api == nil || peer == nil || msgID <= 0 {
		return nil, errors.New("invalid fetch message arguments")
	}
	input := tg.InputMessageClass(&tg.InputMessageID{ID: msgID})

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		resp, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{
				ChannelID:  p.ChannelID,
				AccessHash: p.AccessHash,
			},
			ID: []tg.InputMessageClass{input},
		})
		if err != nil {
			return nil, err
		}
		modified, ok := resp.AsModified()
		if !ok {
			return nil, errors.New("unexpected response type")
		}
		for _, msgClass := range modified.GetMessages() {
			if msg, ok := msgClass.(*tg.Message); ok && msg != nil && msg.ID == msgID {
				return msg, nil
			}
		}
		return nil, errors.New("message not found")
	default:
		resp, err := api.MessagesGetMessages(ctx, []tg.InputMessageClass{input})
		if err != nil {
			return nil, err
		}
		modified, ok := resp.AsModified()
		if !ok {
			return nil, errors.New("unexpected response type")
		}
		for _, msgClass := range modified.GetMessages() {
			if msg, ok := msgClass.(*tg.Message); ok && msg != nil && msg.ID == msgID {
				return msg, nil
			}
		}
		return nil, errors.New("message not found")
	}
}

type cappedBuffer struct {
	Buf bytes.Buffer
	N   int64
	Max int64
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Max > 0 && b.N+int64(len(p)) > b.Max {
		return 0, ErrFileTooLarge
	}
	n, err := b.Buf.Write(p)
	b.N += int64(n)
	return n, err
}

func (b *cappedBuffer) Bytes() []byte {
	return b.Buf.Bytes()
}

func isFileReferenceError(err error) bool {
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		return rpcErr.IsOneOf("FILE_REFERENCE_EXPIRED", "FILE_REFERENCE_INVALID", "FILE_REFERENCE_EMPTY")
	}
	return false
}
