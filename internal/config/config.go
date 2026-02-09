package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultAppFolder = "asktg"
)

type Config struct {
	DataDir string
}

func Load() Config {
	if envDir := os.Getenv("ASKTG_DATA_DIR"); envDir != "" {
		return Config{DataDir: envDir}
	}
	if persisted, err := loadPersistedDataDir(); err == nil && strings.TrimSpace(persisted) != "" {
		return Config{DataDir: persisted}
	}
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		home, _ := os.UserHomeDir()
		return Config{DataDir: filepath.Join(home, ".asktg")}
	}
	return Config{DataDir: filepath.Join(localAppData, defaultAppFolder)}
}

func (c Config) DBPath() string {
	return filepath.Join(c.DataDir, "app.db")
}

func PersistDataDir(dataDir string) error {
	clean := strings.TrimSpace(filepath.Clean(dataDir))
	if clean == "" {
		return errors.New("data directory is required")
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return err
	}
	bootstrapPath, err := bootstrapConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(bootstrapPath), 0o755); err != nil {
		return err
	}
	payload := bootstrapConfig{DataDir: clean}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := bootstrapPath + ".tmp"
	if err := os.WriteFile(tmpPath, encoded, 0o644); err != nil {
		return err
	}
	if err := os.Remove(bootstrapPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpPath, bootstrapPath)
}

func DefaultDataDir() string {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".asktg")
	}
	return filepath.Join(localAppData, defaultAppFolder)
}

type bootstrapConfig struct {
	DataDir string `json:"data_dir"`
}

func loadPersistedDataDir() (string, error) {
	bootstrapPath, err := bootstrapConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Clean(bootstrapPath))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var payload bootstrapConfig
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	clean := strings.TrimSpace(filepath.Clean(payload.DataDir))
	return clean, nil
}

func bootstrapConfigPath() (string, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if strings.TrimSpace(localAppData) != "" {
		return filepath.Join(localAppData, defaultAppFolder, "bootstrap.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".asktg-bootstrap.json"), nil
}
