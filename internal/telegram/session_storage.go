package telegram

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/session"
)

// SafeFileSessionStorage implements session.Storage with atomic writes
// to prevent data corruption on crash. It writes to a temporary file
// first, then renames it to the target path.
//
// On load, it validates that the file contains valid JSON and falls back
// to session.ErrNotFound if the file is corrupted (e.g. filled with null bytes).
type SafeFileSessionStorage struct {
	Path string
	mux  sync.Mutex
}

func (s *SafeFileSessionStorage) LoadSession(_ context.Context) ([]byte, error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return nil, session.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, session.ErrNotFound
	}

	// Validate that file contains actual JSON, not null bytes from a crash.
	if !json.Valid(data) {
		return nil, session.ErrNotFound
	}

	return data, nil
}

func (s *SafeFileSessionStorage) StoreSession(_ context.Context, data []byte) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	dir := filepath.Dir(s.Path)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, s.Path)
}
