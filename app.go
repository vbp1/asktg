package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"asktg/internal/autostart"
	"asktg/internal/config"
	"asktg/internal/domain"
	"asktg/internal/embeddings"
	"asktg/internal/mcpserver"
	"asktg/internal/pdfextract"
	"asktg/internal/security"
	"asktg/internal/store/sqlite"
	"asktg/internal/telegram"
	"asktg/internal/tray"
	"asktg/internal/urlfetch"
	"asktg/internal/vector"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	syncInterval          = 90 * time.Second
	syncMaxMessagesPerRun = 400
	realtimeBurst         = 75 * time.Second
	urlCandidateScanLimit = 40
	urlTaskMaxAttempts    = 3
	pdfTaskMaxAttempts    = 3
	embedTaskMaxAttempts  = 4
	embedCandidateLimit   = 60
	backupFilenamePrefix  = "asktg-backup-"
	hnswM                 = 16
	hnswEfConstruction    = 200
	hnswEfSearch          = 64
	windowStateNormal     = "normal"
	windowStateMaximised  = "maximised"
	windowStateFullscreen = "fullscreen"
)

type semanticProfile struct {
	MaxDistance float64
	Slack       float64
}

var semanticProfiles = map[string]semanticProfile{
	// Embeddings returned by the OpenAI-compatible API are unit-normalized (||v|| ~= 1),
	// and we use squared L2 distance: d^2 = 2 - 2*cos(theta).
	//
	// Note: with short 1-word queries, we commonly observe best distances around ~1.3 in real data,
	// so the defaults must be permissive enough to return *some* semantic candidates, while still
	// allowing users to tighten it.
	//
	// very:   max 1.40 ~= cos >= 0.30
	// similar max 1.70 ~= cos >= 0.15
	// weak:   max 1.90 ~= cos >= 0.05
	"very":    {MaxDistance: 1.40, Slack: 0.25},
	"similar": {MaxDistance: 1.70, Slack: 0.35},
	"weak":    {MaxDistance: 1.90, Slack: 0.45},
}

// App struct
type App struct {
	ctx         context.Context
	cfg         config.Config
	store       *sqlite.Store
	telegramSvc *telegram.Service
	mcpServer   *mcpserver.Server
	mcpEndpoint string
	mcpStatus   string
	mcpPort     int
	trayManager *tray.Manager
	trayStatus  string
	windowState string
	embedClient *embeddings.HTTPClient
	vectorIndex *vector.HNSW
	mu          sync.RWMutex
	maintenance sync.Mutex
	paused      bool
	quitNow     bool

	syncCancel           context.CancelFunc
	syncWG               sync.WaitGroup
	syncRunMu            sync.Mutex
	syncState            string
	syncBackfillProgress int
	syncLastUnix         int64
	realtimeWG           sync.WaitGroup
	urlWG                sync.WaitGroup
	embedWG              sync.WaitGroup
	pdfWG                sync.WaitGroup

	pdfBackfillMu      sync.Mutex
	pdfBackfillRunning bool
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{
		trayStatus: "initializing",
	}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.setTrayStatus("initializing")
	a.cfg = config.Load()
	if err := os.MkdirAll(a.cfg.DataDir, 0o755); err != nil {
		panic(err)
	}

	dbStore, err := sqlite.Open(a.cfg.DBPath())
	if err != nil {
		panic(err)
	}
	a.store = dbStore

	if err := a.store.Migrate(ctx); err != nil {
		panic(err)
	}
	a.telegramSvc = telegram.NewService(filepath.Join(a.cfg.DataDir, "telegram", "session.json"))
	a.seedTelegramCredentials(ctx)
	a.configureTelegramFromStore(ctx)
	a.configureEmbeddingsFromStore(ctx)
	a.bootstrapVectorIndex(ctx)
	a.setSyncStatus("idle", 0, 0)
	paused, pausedErr := a.store.GetSettingBool(ctx, "sync_paused", false)
	if pausedErr == nil {
		a.paused = paused
	}
	if a.paused {
		a.setSyncStatus("paused", 0, 0)
	}
	a.startTray()
	if err := a.startMCP(ctx); err != nil {
		runtime.LogWarningf(ctx, "MCP start warning: %v", err)
	}
	if !a.paused {
		a.startWorkers()
	}
}

func (a *App) seedTelegramCredentials(ctx context.Context) {
	if a.store == nil || a.telegramSvc == nil {
		return
	}

	existingID, _ := a.store.GetSettingInt(ctx, "telegram_api_id", 0)
	existingHash, _ := a.readSecretSetting(ctx, "telegram_api_hash")
	if existingID > 0 && strings.TrimSpace(existingHash) != "" {
		return
	}

	apiID, apiHash, ok := telegramSeedCredentials()
	if !ok {
		return
	}

	if err := a.TelegramSetCredentials(apiID, apiHash); err != nil {
		runtime.LogWarningf(ctx, "Telegram credential seed failed: %v", err)
		return
	}
	runtime.LogInfo(ctx, "Telegram credentials loaded from local environment/build defaults.")
}

func telegramSeedCredentials() (int, string, bool) {
	// Prefer runtime env vars so users can set secrets without baking them into source control.
	idRaw := strings.TrimSpace(os.Getenv("ASKTG_TG_API_ID"))
	hashRaw := strings.TrimSpace(os.Getenv("ASKTG_TG_API_HASH"))

	if idRaw != "" || hashRaw != "" {
		if idRaw == "" || hashRaw == "" {
			return 0, "", false
		}
		apiID, err := strconv.Atoi(idRaw)
		if err != nil || apiID <= 0 {
			return 0, "", false
		}
		return apiID, hashRaw, true
	}

	return embeddedTelegramCredentials()
}

func (a *App) shutdown(ctx context.Context) {
	a.stopWorkers()
	_ = a.stopMCP(ctx)
	a.stopTray()
	if a.store != nil {
		_ = a.store.Close()
	}
}

func (a *App) configureTelegramFromStore(ctx context.Context) {
	if a.store == nil || a.telegramSvc == nil {
		return
	}
	apiID, apiIDErr := a.store.GetSettingInt(ctx, "telegram_api_id", 0)
	if apiIDErr != nil {
		return
	}
	apiHash, hashErr := a.readSecretSetting(ctx, "telegram_api_hash")
	if hashErr != nil || apiID <= 0 || strings.TrimSpace(apiHash) == "" {
		return
	}
	if err := a.telegramSvc.Configure(apiID, apiHash); err != nil {
		runtime.LogWarningf(ctx, "Telegram credentials are present but invalid: %v", err)
	}
}

func (a *App) configureEmbeddingsFromStore(ctx context.Context) {
	if a.store == nil {
		a.embedClient = nil
		return
	}
	apiKey, err := a.readSecretSetting(ctx, "embeddings_api_key")
	if err != nil || strings.TrimSpace(apiKey) == "" {
		a.embedClient = nil
		return
	}
	baseURL, _ := a.store.GetSetting(ctx, "embeddings_base_url", "https://api.openai.com/v1")
	model, _ := a.store.GetSetting(ctx, "embeddings_model", "text-embedding-3-large")
	dims, _ := a.store.GetSettingInt(ctx, "embeddings_dims", 3072)
	a.embedClient = embeddings.NewHTTPClient(baseURL, apiKey, model, dims)
	if a.vectorIndex == nil || a.vectorIndex.Dimensions() != dims {
		a.vectorIndex = vector.NewHNSW(dims, hnswM, hnswEfConstruction, hnswEfSearch)
	}

	// If the user has set History=Backfill, semantic embeddings should include the full history.
	// Ensure embeddings_since_unix is corrected without requiring an explicit rebuild click.
	if _, err := a.store.BackfillEmbeddingsForEnabledChats(ctx); err != nil {
		runtime.LogWarningf(ctx, "embeddings backfill scope update failed: %v", err)
	}
}

func (a *App) readSecretSetting(ctx context.Context, key string) (string, error) {
	raw, err := a.store.GetSetting(ctx, key, "")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	decoded, decodeErr := security.UnprotectString(raw)
	if decodeErr == nil {
		return decoded, nil
	}
	if security.IsProtectedSecret(raw) {
		return "", decodeErr
	}
	return raw, nil
}

func (a *App) writeSecretSetting(ctx context.Context, key string, value string) error {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return a.store.SetSetting(ctx, key, "")
	}
	protected, err := security.ProtectString(clean)
	if err != nil {
		return err
	}
	return a.store.SetSetting(ctx, key, protected)
}

func (a *App) vectorGraphPath() string {
	return filepath.Join(a.cfg.DataDir, "vectors.graph")
}

func (a *App) bootstrapVectorIndex(ctx context.Context) {
	if a.vectorIndex == nil {
		dims, _ := a.store.GetSettingInt(ctx, "embeddings_dims", 3072)
		a.vectorIndex = vector.NewHNSW(dims, hnswM, hnswEfConstruction, hnswEfSearch)
	}
	graphPath := a.vectorGraphPath()
	if _, err := os.Stat(graphPath); err == nil {
		if loadErr := a.vectorIndex.Load(graphPath); loadErr != nil {
			runtime.LogWarningf(ctx, "Vector graph load failed, rebuilding: %v", loadErr)
		}
	}
	if a.vectorIndex.Len() > 0 {
		return
	}
	rebuildCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := a.rebuildVectorIndexFromStore(rebuildCtx, true); err != nil {
		runtime.LogWarningf(ctx, "Vector graph rebuild on startup failed: %v", err)
	}
}

func (a *App) rebuildVectorIndexFromStore(ctx context.Context, persist bool) error {
	if a.vectorIndex == nil {
		dims, _ := a.store.GetSettingInt(ctx, "embeddings_dims", 3072)
		a.vectorIndex = vector.NewHNSW(dims, hnswM, hnswEfConstruction, hnswEfSearch)
	}
	records, err := a.store.ListEmbeddings(ctx, 0)
	if err != nil {
		return err
	}
	urlRecords, err := a.store.ListURLEmbeddings(ctx, 0)
	if err != nil {
		return err
	}
	fileRecords, err := a.store.ListFileEmbeddings(ctx, 0)
	if err != nil {
		return err
	}
	items := make([]vector.Item, 0, len(records)+len(urlRecords)+len(fileRecords))
	for _, record := range records {
		items = append(items, vector.Item{
			ChunkID: record.ChunkID,
			Vector:  record.Vector,
		})
	}
	for _, record := range urlRecords {
		nodeID := vectorNodeIDForURL(record.ChunkID)
		if nodeID == 0 {
			continue
		}
		items = append(items, vector.Item{
			ChunkID: nodeID,
			Vector:  record.Vector,
		})
	}
	for _, record := range fileRecords {
		nodeID := vectorNodeIDForFileDoc(record.ChunkID)
		if nodeID == 0 {
			continue
		}
		items = append(items, vector.Item{
			ChunkID: nodeID,
			Vector:  record.Vector,
		})
	}
	if err := a.vectorIndex.Rebuild(items); err != nil {
		return err
	}
	if persist {
		return a.vectorIndex.Save(a.vectorGraphPath())
	}
	return nil
}

func (a *App) startWorkers() {
	loopCtx, cancel := context.WithCancel(context.Background())
	a.syncCancel = cancel

	if a.store != nil {
		if reset, err := a.store.ResetRunningEmbeddingTasks(a.ctx, time.Now().Unix()); err != nil {
			runtime.LogWarningf(a.ctx, "Embedding task reset failed: %v", err)
		} else if reset > 0 {
			runtime.LogInfof(a.ctx, "Reset %d stuck embedding tasks", reset)
		}
	}

	a.syncWG.Add(1)
	go func() {
		defer a.syncWG.Done()
		a.runBackgroundSyncLoop(loopCtx)
	}()

	a.realtimeWG.Add(1)
	go func() {
		defer a.realtimeWG.Done()
		a.runRealtimeLoop(loopCtx)
	}()

	a.urlWG.Add(1)
	go func() {
		defer a.urlWG.Done()
		a.runURLFetchLoop(loopCtx)
	}()

	a.embedWG.Add(1)
	go func() {
		defer a.embedWG.Done()
		a.runEmbeddingsLoop(loopCtx)
	}()

	a.pdfWG.Add(1)
	go func() {
		defer a.pdfWG.Done()
		a.runPDFFetchLoop(loopCtx)
	}()
}

func (a *App) stopWorkers() {
	if a.syncCancel == nil {
		return
	}
	a.syncCancel()
	a.syncWG.Wait()
	a.realtimeWG.Wait()
	a.urlWG.Wait()
	a.embedWG.Wait()
	a.pdfWG.Wait()
	a.syncCancel = nil
}

func (a *App) startTray() {
	a.trayManager = tray.New(trayIcon, func() {
		a.showMainWindow()
	}, func() {
		a.hideMainWindow()
	}, func() {
		a.ExitApp()
	})
	if err := a.trayManager.Start(); err != nil {
		a.setTrayStatus("unavailable")
		runtime.LogWarningf(a.ctx, "System tray unavailable: %v", err)
		return
	}
	a.setTrayStatus("running")
	a.trayManager.SetWindowVisible(true)
}

func (a *App) stopTray() {
	if a.trayManager == nil {
		return
	}
	a.trayManager.Stop()
	a.setTrayStatus("stopped")
}

func (a *App) showMainWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowShow(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	a.restoreWindowState()
	state := a.windowStateSnapshot()
	if state != windowStateNormal {
		go func(savedState string) {
			time.Sleep(140 * time.Millisecond)
			if a.ctx == nil {
				return
			}
			a.applyWindowState(savedState)
		}(state)
	}
	if a.trayManager != nil {
		a.trayManager.SetWindowVisible(true)
	}
}

func (a *App) hideMainWindow() {
	if a.ctx == nil {
		return
	}
	a.captureWindowState()
	runtime.WindowHide(a.ctx)
	if a.trayManager != nil {
		a.trayManager.SetWindowVisible(false)
	}
}

func (a *App) beforeClose(_ context.Context) (prevent bool) {
	a.mu.RLock()
	quitNow := a.quitNow
	a.mu.RUnlock()
	if quitNow {
		return false
	}
	a.hideMainWindow()
	return true
}

func (a *App) setTrayStatus(status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.trayStatus = status
}

func (a *App) TrayStatus() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if strings.TrimSpace(a.trayStatus) == "" {
		return "unknown"
	}
	return a.trayStatus
}

func (a *App) captureWindowState() {
	state := windowStateNormal
	if runtime.WindowIsFullscreen(a.ctx) {
		state = windowStateFullscreen
	} else if runtime.WindowIsMaximised(a.ctx) {
		state = windowStateMaximised
	}
	a.mu.Lock()
	a.windowState = state
	a.mu.Unlock()
}

func (a *App) windowStateSnapshot() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if strings.TrimSpace(a.windowState) == "" {
		return windowStateNormal
	}
	return a.windowState
}

func (a *App) restoreWindowState() {
	a.applyWindowState(a.windowStateSnapshot())
}

func (a *App) applyWindowState(state string) {
	switch state {
	case windowStateFullscreen:
		runtime.WindowFullscreen(a.ctx)
	case windowStateMaximised:
		runtime.WindowMaximise(a.ctx)
	}
}

func (a *App) startMCP(ctx context.Context) error {
	if a.store == nil {
		a.setMCPRuntime("unavailable", 0)
		return errors.New("store is not initialized")
	}
	enabled, err := a.store.GetSettingBool(ctx, "mcp_enabled", true)
	if err != nil {
		a.setMCPRuntime("failed (settings read error)", 0)
		return err
	}
	if !enabled {
		port, _ := a.store.GetSettingInt(ctx, "mcp_port", 0)
		a.setMCPRuntime("disabled", port)
		return nil
	}

	port, err := a.store.GetSettingInt(ctx, "mcp_port", 0)
	if err != nil {
		a.setMCPRuntime("failed (settings read error)", 0)
		return err
	}
	if a.mcpServer != nil {
		activePort := port
		if parsed, parseErr := url.Parse(a.mcpEndpoint); parseErr == nil {
			if p, convErr := strconv.Atoi(parsed.Port()); convErr == nil {
				activePort = p
			}
		}
		a.setMCPRuntime("running", activePort)
		return nil
	}
	mcpSrv := mcpserver.New(&queryService{app: a})
	if err := mcpSrv.Start(port); err != nil {
		a.setMCPRuntime(mcpFailureStatus(err, port), port)
		return err
	}
	a.mcpServer = mcpSrv
	a.mcpEndpoint = mcpSrv.Endpoint()

	activePort := port
	if parsed, parseErr := url.Parse(a.mcpEndpoint); parseErr == nil {
		if p, convErr := strconv.Atoi(parsed.Port()); convErr == nil {
			activePort = p
			_ = a.store.SetSetting(ctx, "mcp_port", strconv.Itoa(p))
		}
	}
	a.setMCPRuntime("running", activePort)
	return nil
}

func (a *App) stopMCP(ctx context.Context) error {
	if a.mcpServer == nil {
		return nil
	}
	err := a.mcpServer.Stop(ctx)
	a.mcpServer = nil
	a.mcpEndpoint = ""
	return err
}

func (a *App) setMCPRuntime(status string, port int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(status) != "" {
		a.mcpStatus = status
	}
	if port >= 0 {
		a.mcpPort = port
	}
}

func (a *App) mcpRuntimeSnapshot() (string, int) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	status := a.mcpStatus
	if strings.TrimSpace(status) == "" {
		status = "unknown"
	}
	return status, a.mcpPort
}

func mcpFailureStatus(err error, configuredPort int) string {
	if configuredPort > 0 && isAddressInUse(err) {
		return "failed (port in use)"
	}
	return "failed"
}

func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, syscall.EADDRINUSE)
	}
	return false
}

func (a *App) DataDir() string {
	return a.cfg.DataDir
}

func (a *App) BrowseDataDir() (string, error) {
	if a.ctx == nil {
		return "", errors.New("app context is not initialized")
	}
	defaultDir := a.cfg.DataDir
	if _, err := os.Stat(defaultDir); err != nil {
		defaultDir = filepath.Dir(defaultDir)
	}
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "Select asktg data directory",
		DefaultDirectory:     defaultDir,
		CanCreateDirectories: true,
	})
}

func (a *App) SetDataDir(path string) (string, error) {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return "", errors.New("data directory is required")
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", err
	}
	if err := config.PersistDataDir(abs); err != nil {
		return "", err
	}
	if filepath.Clean(abs) == filepath.Clean(a.cfg.DataDir) {
		return "Data directory is already active", nil
	}
	return fmt.Sprintf("Data directory saved: %s. Restart app to apply.", abs), nil
}

func (a *App) AutostartEnabled() (bool, error) {
	return autostart.Enabled()
}

func (a *App) SetAutostartEnabled(enable bool) (bool, error) {
	if err := autostart.SetEnabled(enable); err != nil {
		return false, err
	}
	return autostart.Enabled()
}

func (a *App) BackgroundPaused() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.paused
}

func (a *App) PauseBackground() (domain.IndexStatus, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	if a.BackgroundPaused() {
		return a.getStatus(a.ctx)
	}
	a.stopWorkers()
	a.mu.Lock()
	a.paused = true
	a.mu.Unlock()
	a.setSyncStatus("paused", -1, 0)
	if err := a.store.SetSetting(a.ctx, "sync_paused", "1"); err != nil {
		return domain.IndexStatus{}, err
	}
	return a.getStatus(a.ctx)
}

func (a *App) ResumeBackground() (domain.IndexStatus, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	if !a.BackgroundPaused() {
		return a.getStatus(a.ctx)
	}
	a.mu.Lock()
	a.paused = false
	a.mu.Unlock()
	if err := a.store.SetSetting(a.ctx, "sync_paused", "0"); err != nil {
		return domain.IndexStatus{}, err
	}
	a.setSyncStatus("idle", -1, time.Now().Unix())
	a.startWorkers()
	return a.getStatus(a.ctx)
}

func (a *App) ListChats() ([]domain.ChatPolicy, error) {
	if a.store == nil {
		return nil, errors.New("store is not initialized")
	}
	return a.store.ListChats(a.ctx)
}

func (a *App) SetChatPolicy(chatID int64, enabled bool, historyMode string, allowEmbeddings bool, urlsMode string) error {
	if a.store == nil {
		return errors.New("store is not initialized")
	}
	historyMode = strings.TrimSpace(historyMode)
	if historyMode == "" {
		historyMode = "full"
	}
	urlsMode = strings.TrimSpace(urlsMode)
	if urlsMode == "" {
		urlsMode = "off"
	}
	if err := a.store.SetChatPolicy(a.ctx, chatID, enabled, historyMode, allowEmbeddings, urlsMode); err != nil {
		return err
	}
	go func() {
		scanCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		a.enqueueURLCandidates(scanCtx)
	}()
	return nil
}

func (a *App) TelegramSetCredentials(apiID int, apiHash string) error {
	if a.store == nil {
		return errors.New("store is not initialized")
	}
	if a.telegramSvc == nil {
		return errors.New("telegram service is not initialized")
	}
	if err := a.telegramSvc.Configure(apiID, apiHash); err != nil {
		return err
	}
	if err := a.store.SetSetting(a.ctx, "telegram_api_id", strconv.Itoa(apiID)); err != nil {
		return err
	}
	return a.writeSecretSetting(a.ctx, "telegram_api_hash", apiHash)
}

func (a *App) EmbeddingsConfig() (domain.EmbeddingsConfig, error) {
	if a.store == nil {
		return domain.EmbeddingsConfig{}, errors.New("store is not initialized")
	}
	baseURL, err := a.store.GetSetting(a.ctx, "embeddings_base_url", "https://api.openai.com/v1")
	if err != nil {
		return domain.EmbeddingsConfig{}, err
	}
	model, err := a.store.GetSetting(a.ctx, "embeddings_model", "text-embedding-3-large")
	if err != nil {
		return domain.EmbeddingsConfig{}, err
	}
	dims, err := a.store.GetSettingInt(a.ctx, "embeddings_dims", 3072)
	if err != nil {
		return domain.EmbeddingsConfig{}, err
	}
	apiKey, err := a.readSecretSetting(a.ctx, "embeddings_api_key")
	if err != nil {
		return domain.EmbeddingsConfig{}, err
	}
	return domain.EmbeddingsConfig{
		BaseURL:    baseURL,
		Model:      model,
		Dimensions: dims,
		Configured: strings.TrimSpace(apiKey) != "",
	}, nil
}

func (a *App) SetEmbeddingsConfig(baseURL, model, apiKey string, dimensions int) error {
	if a.store == nil {
		return errors.New("store is not initialized")
	}
	prevKey, _ := a.readSecretSetting(a.ctx, "embeddings_api_key")
	cleanBase := strings.TrimSpace(baseURL)
	if cleanBase == "" {
		cleanBase = "https://api.openai.com/v1"
	}
	cleanModel := strings.TrimSpace(model)
	if cleanModel == "" {
		cleanModel = "text-embedding-3-large"
	}
	if dimensions <= 0 {
		dimensions = 3072
	}
	prevDims, _ := a.store.GetSettingInt(a.ctx, "embeddings_dims", 3072)

	if err := a.store.SetSetting(a.ctx, "embeddings_base_url", cleanBase); err != nil {
		return err
	}
	if err := a.store.SetSetting(a.ctx, "embeddings_model", cleanModel); err != nil {
		return err
	}
	if err := a.store.SetSetting(a.ctx, "embeddings_dims", strconv.Itoa(dimensions)); err != nil {
		return err
	}
	if strings.TrimSpace(apiKey) != "" {
		if err := a.writeSecretSetting(a.ctx, "embeddings_api_key", apiKey); err != nil {
			return err
		}
	}

	a.configureEmbeddingsFromStore(a.ctx)
	if strings.TrimSpace(prevKey) == "" && a.embedClient != nil && a.embedClient.Configured() {
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		defer cancel()
		_, _ = a.store.EnableEmbeddingsForEnabledChats(ctx)
	}
	if prevDims != dimensions {
		ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
		defer cancel()
		if err := a.store.ResetEmbeddings(ctx); err != nil {
			return err
		}
		if a.vectorIndex != nil {
			if err := a.vectorIndex.Rebuild(nil); err != nil {
				return err
			}
			if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
				runtime.LogWarningf(a.ctx, "vector graph save after dimensions update failed: %v", err)
			}
		}
	}
	go func() {
		scanCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		a.enqueueEmbeddingCandidates(scanCtx)
	}()
	return nil
}

func (a *App) TestEmbeddings() (domain.EmbeddingsTestResult, error) {
	if a.store == nil {
		return domain.EmbeddingsTestResult{}, errors.New("store is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()

	baseURL, _ := a.store.GetSetting(ctx, "embeddings_base_url", "https://api.openai.com/v1")
	model, _ := a.store.GetSetting(ctx, "embeddings_model", "text-embedding-3-large")
	dims, _ := a.store.GetSettingInt(ctx, "embeddings_dims", 3072)

	if a.embedClient == nil {
		a.configureEmbeddingsFromStore(ctx)
	}
	if a.embedClient == nil {
		return domain.EmbeddingsTestResult{
			OK:         false,
			BaseURL:    baseURL,
			Model:      model,
			Dimensions: dims,
			Error:      "embeddings client is not configured",
		}, nil
	}

	start := time.Now()
	vectors, err := a.embedClient.Embed(ctx, []string{"ping"})
	took := time.Since(start)
	if err != nil {
		return domain.EmbeddingsTestResult{
			OK:         false,
			BaseURL:    baseURL,
			Model:      model,
			Dimensions: dims,
			TookMs:     took.Milliseconds(),
			Error:      err.Error(),
		}, nil
	}
	vectorLen := 0
	if len(vectors) > 0 {
		vectorLen = len(vectors[0])
	}
	return domain.EmbeddingsTestResult{
		OK:         true,
		BaseURL:    baseURL,
		Model:      model,
		Dimensions: dims,
		VectorLen:  vectorLen,
		TookMs:     took.Milliseconds(),
	}, nil
}

func (a *App) EmbeddingsProgress() (domain.EmbeddingsProgress, error) {
	if a.store == nil {
		return domain.EmbeddingsProgress{}, errors.New("store is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	return a.store.EmbeddingsProgress(ctx)
}

func (a *App) SemanticStrictness() (string, error) {
	if a.store == nil {
		return "", errors.New("store is not initialized")
	}
	raw, err := a.store.GetSetting(a.ctx, "semantic_strictness", "similar")
	if err != nil {
		return "similar", err
	}
	return normalizeSemanticStrictness(raw), nil
}

func (a *App) SetSemanticStrictness(level string) error {
	if a.store == nil {
		return errors.New("store is not initialized")
	}
	return a.store.SetSetting(a.ctx, "semantic_strictness", normalizeSemanticStrictness(level))
}

func (a *App) OnboardingStatus() (domain.OnboardingStatus, error) {
	if a.store == nil {
		return domain.OnboardingStatus{}, errors.New("store is not initialized")
	}

	completed, err := a.store.GetSettingBool(a.ctx, "onboarding_completed", false)
	if err != nil {
		return domain.OnboardingStatus{}, err
	}
	chats, err := a.store.ListChats(a.ctx)
	if err != nil {
		return domain.OnboardingStatus{}, err
	}
	enabledCount := 0
	for _, chat := range chats {
		if chat.Enabled {
			enabledCount++
		}
	}
	if enabledCount == 0 {
		completed = false
	}

	telegramConfigured := false
	telegramAuthorized := false
	if a.telegramSvc != nil {
		ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
		defer cancel()
		status, statusErr := a.telegramSvc.AuthStatus(ctx)
		if statusErr == nil {
			telegramConfigured = status.Configured
			telegramAuthorized = status.Authorized
		}
	}

	return domain.OnboardingStatus{
		Completed:          completed,
		TelegramConfigured: telegramConfigured,
		TelegramAuthorized: telegramAuthorized,
		ChatsDiscovered:    len(chats),
		EnabledChats:       enabledCount,
	}, nil
}

func (a *App) CompleteOnboarding() (domain.OnboardingStatus, error) {
	if a.store == nil {
		return domain.OnboardingStatus{}, errors.New("store is not initialized")
	}
	status, err := a.OnboardingStatus()
	if err != nil {
		return domain.OnboardingStatus{}, err
	}
	if status.EnabledChats < 1 {
		return domain.OnboardingStatus{}, errors.New("enable at least one chat to complete onboarding")
	}
	if err := a.store.SetSetting(a.ctx, "onboarding_completed", "1"); err != nil {
		return domain.OnboardingStatus{}, err
	}
	return a.OnboardingStatus()
}

func (a *App) TelegramAuthStatus() (domain.TelegramAuthStatus, error) {
	if a.telegramSvc == nil {
		return domain.TelegramAuthStatus{}, errors.New("telegram service is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	status, err := a.telegramSvc.AuthStatus(ctx)
	if err != nil {
		return domain.TelegramAuthStatus{}, err
	}
	return domain.TelegramAuthStatus{
		Configured:   status.Configured,
		Authorized:   status.Authorized,
		AwaitingCode: status.AwaitingCode,
		Phone:        status.Phone,
		UserDisplay:  status.UserDisplay,
	}, nil
}

func (a *App) TelegramRequestCode(phone string) (domain.TelegramAuthStatus, error) {
	if a.telegramSvc == nil {
		return domain.TelegramAuthStatus{}, errors.New("telegram service is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	status, err := a.telegramSvc.RequestCode(ctx, phone)
	if err != nil {
		return domain.TelegramAuthStatus{}, err
	}
	return domain.TelegramAuthStatus{
		Configured:   status.Configured,
		Authorized:   status.Authorized,
		AwaitingCode: status.AwaitingCode,
		Phone:        status.Phone,
		UserDisplay:  status.UserDisplay,
	}, nil
}

func (a *App) TelegramSignIn(code string, password string) (domain.TelegramAuthStatus, error) {
	if a.telegramSvc == nil {
		return domain.TelegramAuthStatus{}, errors.New("telegram service is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	status, err := a.telegramSvc.SignIn(ctx, code, password)
	if err != nil {
		return domain.TelegramAuthStatus{}, err
	}
	return domain.TelegramAuthStatus{
		Configured:   status.Configured,
		Authorized:   status.Authorized,
		AwaitingCode: status.AwaitingCode,
		Phone:        status.Phone,
		UserDisplay:  status.UserDisplay,
	}, nil
}

func (a *App) TelegramLoadChats() ([]domain.ChatPolicy, error) {
	if a.store == nil {
		return nil, errors.New("store is not initialized")
	}
	if a.telegramSvc == nil {
		return nil, errors.New("telegram service is not initialized")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	dialogs, err := a.telegramSvc.ListDialogs(ctx)
	if err != nil {
		return nil, err
	}
	for _, dialog := range dialogs {
		if err := a.store.UpsertDiscoveredChat(ctx, dialog.ChatID, dialog.Title, dialog.Type); err != nil {
			return nil, err
		}
	}
	return a.store.ListChats(a.ctx)
}

func (a *App) TelegramChatFolders() ([]domain.ChatFolder, error) {
	if a.store == nil {
		return nil, errors.New("store is not initialized")
	}
	chats, err := a.store.ListChats(a.ctx)
	if err != nil {
		return nil, err
	}
	allIDs := make([]int64, 0, len(chats))
	selectedIDs := make([]int64, 0, len(chats))
	known := make(map[int64]struct{}, len(chats))
	for _, chat := range chats {
		allIDs = append(allIDs, chat.ChatID)
		if chat.Enabled {
			selectedIDs = append(selectedIDs, chat.ChatID)
		}
		known[chat.ChatID] = struct{}{}
	}
	folders := []domain.ChatFolder{
		{ID: 0, Title: "All", ChatIDs: allIDs},
		{ID: 1_000_000_000, Title: "Выбранные", Emoticon: "⭐", ChatIDs: selectedIDs},
	}

	if a.telegramSvc == nil {
		return folders, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	custom, err := a.telegramSvc.ListChatFolders(ctx)
	if err != nil {
		// Folders are nice-to-have; return "All" if Telegram isn't ready.
		return folders, nil
	}

	// Only return folders that reference discovered chats.
	for _, f := range custom {
		if strings.EqualFold(strings.TrimSpace(f.Title), "Новые") || strings.EqualFold(strings.TrimSpace(f.Title), "New") {
			continue
		}
		filtered := make([]int64, 0, len(f.ChatIDs))
		filteredPinned := make([]int64, 0, len(f.PinnedChatIDs))
		for _, id := range f.PinnedChatIDs {
			if _, ok := known[id]; ok {
				filteredPinned = append(filteredPinned, id)
			}
		}
		for _, id := range f.ChatIDs {
			if _, ok := known[id]; ok {
				filtered = append(filtered, id)
			}
		}
		f.ChatIDs = filtered
		f.PinnedChatIDs = filteredPinned
		folders = append(folders, f)
	}

	return folders, nil
}

func (a *App) Search(query string, mode string, advanced bool, chatIDs []int64, fromUnix int64, toUnix int64, limit int) ([]domain.SearchResult, error) {
	if a.store == nil {
		return nil, errors.New("store is not initialized")
	}
	searchMode := domain.SearchModeFTS
	if strings.EqualFold(mode, string(domain.SearchModeHybrid)) {
		searchMode = domain.SearchModeHybrid
	}
	req := domain.SearchRequest{
		Query: query,
		Mode:  searchMode,
		Filters: domain.SearchFilters{
			ChatIDs:  chatIDs,
			FromUnix: fromUnix,
			ToUnix:   toUnix,
			Limit:    limit,
			Advanced: advanced,
		},
	}
	results, err := a.searchMessages(a.ctx, req)
	if err != nil {
		return nil, err
	}
	for idx := range results {
		results[idx].DeepLink = BuildBestEffortDeepLink(results[idx].ChatID, results[idx].MsgID)
	}
	return results, nil
}

func (a *App) searchMessages(ctx context.Context, req domain.SearchRequest) ([]domain.SearchResult, error) {
	ftsResults, err := a.store.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	for idx := range ftsResults {
		ftsResults[idx].MatchFTS = true
	}
	if req.Mode != domain.SearchModeHybrid {
		return ftsResults, nil
	}
	if strings.TrimSpace(req.Query) == "" {
		return ftsResults, nil
	}
	if a.embedClient == nil || !a.embedClient.Configured() {
		return ftsResults, nil
	}
	if a.vectorIndex == nil {
		return ftsResults, nil
	}
	enabled, err := a.hasEmbeddingsScope(ctx, req.Filters.ChatIDs)
	if err != nil || !enabled {
		return ftsResults, nil
	}

	vectors, err := a.embedClient.Embed(ctx, []string{req.Query})
	if err != nil || len(vectors) == 0 {
		if err != nil {
			runtime.LogWarningf(a.ctx, "hybrid query embedding failed: %v", err)
		}
		return ftsResults, nil
	}

	vectorCandidates := a.vectorIndex.Search(vectors[0], 300)
	profile := a.semanticProfile(ctx)
	vectorCandidates = filterSemanticCandidates(vectorCandidates, profile)
	if len(vectorCandidates) == 0 {
		return ftsResults, nil
	}
	vectorResults, err := a.lookupVectorCandidates(ctx, req, vectorCandidates)
	if err != nil {
		runtime.LogWarningf(a.ctx, "hybrid vector candidate lookup failed: %v", err)
		return ftsResults, nil
	}
	return fuseByRRF(ftsResults, vectorResults, req.Filters.Limit), nil
}

func (a *App) semanticProfile(ctx context.Context) semanticProfile {
	if a.store == nil {
		return semanticProfiles["similar"]
	}
	raw, err := a.store.GetSetting(ctx, "semantic_strictness", "similar")
	if err != nil {
		return semanticProfiles["similar"]
	}
	profile, ok := semanticProfiles[normalizeSemanticStrictness(raw)]
	if !ok {
		return semanticProfiles["similar"]
	}
	return profile
}

func normalizeSemanticStrictness(level string) string {
	clean := strings.ToLower(strings.TrimSpace(level))
	switch clean {
	case "very", "similar", "weak":
		return clean
	default:
		return "similar"
	}
}

func filterSemanticCandidates(candidates []vector.Candidate, profile semanticProfile) []vector.Candidate {
	if len(candidates) == 0 {
		return candidates
	}
	best := candidates[0].Distance
	maxAllowed := profile.MaxDistance
	if capAllowed := best + profile.Slack; capAllowed < maxAllowed {
		maxAllowed = capAllowed
	}
	out := candidates[:0]
	for _, c := range candidates {
		if c.Distance <= maxAllowed {
			out = append(out, c)
		}
	}
	return out
}

func (a *App) lookupVectorCandidates(ctx context.Context, req domain.SearchRequest, candidates []vector.Candidate) ([]domain.SearchResult, error) {
	msgChunkIDs := make([]int64, 0, len(candidates))
	urlIDs := make([]int64, 0, len(candidates))
	fileDocIDs := make([]int64, 0, len(candidates))
	distance := make(map[int64]float64, len(candidates))
	for _, item := range candidates {
		if item.ChunkID == 0 {
			continue
		}
		distance[item.ChunkID] = item.Distance
		if item.ChunkID < 0 {
			abs := -item.ChunkID
			if abs > vectorFileDocOffset {
				fileDocIDs = append(fileDocIDs, abs-vectorFileDocOffset)
			} else {
				urlIDs = append(urlIDs, abs)
			}
			continue
		}
		msgChunkIDs = append(msgChunkIDs, item.ChunkID)
	}
	if len(msgChunkIDs) == 0 && len(urlIDs) == 0 && len(fileDocIDs) == 0 {
		return nil, nil
	}

	lookupChunks, err := a.store.LookupChunkResults(ctx, req, msgChunkIDs)
	if err != nil {
		return nil, err
	}
	lookupURLs, err := a.store.LookupURLDocResults(ctx, req, urlIDs)
	if err != nil {
		return nil, err
	}
	lookupFiles, err := a.store.LookupFileDocResults(ctx, req, fileDocIDs)
	if err != nil {
		return nil, err
	}
	limit := req.Filters.Limit
	unlimited := limit == -1
	if !unlimited && (limit <= 0 || limit > 100) {
		limit = 25
	}
	resultCap := len(candidates)
	if !unlimited && limit < resultCap {
		resultCap = limit
	}
	if resultCap < 0 {
		resultCap = 0
	}
	results := make([]domain.SearchResult, 0, resultCap)
	for _, candidate := range candidates {
		nodeID := candidate.ChunkID
		if nodeID == 0 {
			continue
		}
		var (
			item domain.SearchResult
			ok   bool
		)
		if nodeID < 0 {
			abs := -nodeID
			if abs > vectorFileDocOffset {
				item, ok = lookupFiles[abs-vectorFileDocOffset]
			} else {
				item, ok = lookupURLs[abs]
			}
		} else {
			item, ok = lookupChunks[nodeID]
		}
		if !ok {
			continue
		}
		item.Score = distance[nodeID]
		item.MatchSemantic = true
		results = append(results, item)
		if !unlimited && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (a *App) hasEmbeddingsScope(ctx context.Context, filterChatIDs []int64) (bool, error) {
	chats, err := a.store.ListChats(ctx)
	if err != nil {
		return false, err
	}
	if len(filterChatIDs) == 0 {
		for _, chat := range chats {
			if chat.Enabled && chat.AllowEmbeddings {
				return true, nil
			}
		}
		return false, nil
	}
	allowed := make(map[int64]struct{}, len(filterChatIDs))
	for _, id := range filterChatIDs {
		allowed[id] = struct{}{}
	}
	for _, chat := range chats {
		if !chat.Enabled || !chat.AllowEmbeddings {
			continue
		}
		if _, ok := allowed[chat.ChatID]; ok {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) GetMessage(chatID int64, msgID int64) (domain.Message, error) {
	if a.store == nil {
		return domain.Message{}, errors.New("store is not initialized")
	}
	return a.store.GetMessage(a.ctx, chatID, msgID)
}

func (a *App) getStatus(ctx context.Context) (domain.IndexStatus, error) {
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	configEnabled, err := a.store.GetSettingBool(ctx, "mcp_enabled", true)
	if err != nil {
		configEnabled = a.mcpServer != nil
	}
	mcpStatus, mcpPort := a.mcpRuntimeSnapshot()
	status, err := a.store.GetIndexStatus(ctx, a.mcpEndpoint, configEnabled, mcpStatus, mcpPort)
	if err != nil {
		return domain.IndexStatus{}, err
	}

	state, progress, lastSync := a.syncSnapshot()
	if strings.TrimSpace(state) != "" {
		status.SyncState = state
	}
	if progress >= 0 {
		status.BackfillProgress = progress
	}
	if lastSync > 0 {
		status.LastSyncUnix = lastSync
	}
	status.Touch()
	return status, nil
}

func (a *App) Status() (domain.IndexStatus, error) {
	return a.getStatus(a.ctx)
}

func (a *App) MCPEndpoint() string {
	return a.mcpEndpoint
}

func (a *App) OpenInTelegram(chatID int64, msgID int64, deepLink string) string {
	target := strings.TrimSpace(deepLink)
	if strings.TrimSpace(target) == "" {
		target = BuildBestEffortDeepLink(chatID, msgID)
	}
	if strings.TrimSpace(target) == "" {
		target = BuildChatDeepLink(chatID)
	}
	runtime.BrowserOpenURL(a.ctx, target)
	return target
}

func (a *App) CreateBackup(destination string) (string, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return "", errors.New("store is not initialized")
	}

	finalPath := strings.TrimSpace(destination)
	if finalPath == "" {
		stamp := time.Now().Format("20060102-150405")
		finalPath = filepath.Join(a.cfg.DataDir, "backups", backupFilenamePrefix+stamp+".zip")
	}
	finalPath = filepath.Clean(finalPath)
	if info, err := os.Stat(finalPath); err == nil && info.IsDir() {
		stamp := time.Now().Format("20060102-150405")
		finalPath = filepath.Join(finalPath, backupFilenamePrefix+stamp+".zip")
	}
	if !strings.EqualFold(filepath.Ext(finalPath), ".zip") {
		finalPath += ".zip"
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return "", err
	}

	tempDir, err := os.MkdirTemp("", "asktg-backup-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	snapshotDBPath := filepath.Join(tempDir, "app.db")
	opCtx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	defer cancel()
	_ = a.store.Checkpoint(opCtx)
	if err := a.store.ExportDatabaseSnapshot(opCtx, snapshotDBPath); err != nil {
		return "", err
	}

	snapshotStore, err := sqlite.Open(snapshotDBPath)
	if err != nil {
		return "", err
	}
	if scrubErr := snapshotStore.ScrubSecretSettings(opCtx); scrubErr != nil {
		_ = snapshotStore.Close()
		return "", scrubErr
	}
	if closeErr := snapshotStore.Close(); closeErr != nil {
		return "", closeErr
	}

	file, err := os.Create(finalPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	zipWriter := zip.NewWriter(file)
	if err := addFileToZip(zipWriter, snapshotDBPath, "app.db"); err != nil {
		_ = zipWriter.Close()
		return "", err
	}
	graphPath := filepath.Join(a.cfg.DataDir, "vectors.graph")
	if _, err := os.Stat(graphPath); err == nil {
		if err := addFileToZip(zipWriter, graphPath, "vectors.graph"); err != nil {
			_ = zipWriter.Close()
			return "", err
		}
	}
	manifest := map[string]any{
		"created_at":   time.Now().UTC().Format(time.RFC3339),
		"db_file":      "app.db",
		"includes_vec": fileExists(graphPath),
		"version":      "0.1.0",
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = zipWriter.Close()
		return "", err
	}
	writer, err := zipWriter.Create("manifest.json")
	if err != nil {
		_ = zipWriter.Close()
		return "", err
	}
	if _, err := writer.Write(manifestBytes); err != nil {
		_ = zipWriter.Close()
		return "", err
	}
	if err := zipWriter.Close(); err != nil {
		return "", err
	}
	return finalPath, nil
}

func (a *App) RestoreBackup(backupPath string) (string, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return "", errors.New("store is not initialized")
	}
	archivePath := filepath.Clean(strings.TrimSpace(backupPath))
	if archivePath == "" {
		return "", errors.New("backup path is required")
	}
	if _, err := os.Stat(archivePath); err != nil {
		return "", err
	}

	tempDir, err := os.MkdirTemp("", "asktg-restore-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)
	if err := unzipArchive(archivePath, tempDir); err != nil {
		return "", err
	}

	extractedDB := filepath.Join(tempDir, "app.db")
	if _, err := os.Stat(extractedDB); err != nil {
		return "", errors.New("backup does not contain app.db")
	}

	a.stopWorkers()
	_ = a.stopMCP(context.Background())
	if a.store != nil {
		_ = a.store.Close()
		a.store = nil
	}
	a.vectorIndex = nil

	if err := os.MkdirAll(a.cfg.DataDir, 0o755); err != nil {
		return "", err
	}
	if err := copyFile(extractedDB, a.cfg.DBPath()); err != nil {
		return "", err
	}
	restoredGraph := filepath.Join(tempDir, "vectors.graph")
	targetGraph := filepath.Join(a.cfg.DataDir, "vectors.graph")
	if fileExists(restoredGraph) {
		if err := copyFile(restoredGraph, targetGraph); err != nil {
			return "", err
		}
	} else {
		_ = os.Remove(targetGraph)
	}

	dbStore, err := sqlite.Open(a.cfg.DBPath())
	if err != nil {
		return "", err
	}
	if err := dbStore.Migrate(a.ctx); err != nil {
		_ = dbStore.Close()
		return "", err
	}
	a.store = dbStore
	a.configureTelegramFromStore(a.ctx)
	a.configureEmbeddingsFromStore(a.ctx)
	a.bootstrapVectorIndex(a.ctx)
	if err := a.startMCP(a.ctx); err != nil {
		runtime.LogWarningf(a.ctx, "MCP restart after restore warning: %v", err)
	}
	restoredPaused, _ := a.store.GetSettingBool(a.ctx, "sync_paused", false)
	a.mu.Lock()
	a.paused = restoredPaused
	a.mu.Unlock()
	if restoredPaused {
		a.setSyncStatus("paused", 0, 0)
	} else {
		a.startWorkers()
		a.setSyncStatus("idle", 0, 0)
	}

	return fmt.Sprintf("Backup restored from %s", archivePath), nil
}

func (a *App) PurgeChat(chatID int64) (domain.IndexStatus, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	if chatID == 0 {
		return domain.IndexStatus{}, errors.New("chat id is required")
	}
	if err := a.store.PurgeChatData(a.ctx, chatID); err != nil {
		return domain.IndexStatus{}, err
	}
	if err := a.rebuildVectorIndexFromStore(a.ctx, true); err != nil {
		runtime.LogWarningf(a.ctx, "vector graph rebuild after chat purge failed: %v", err)
	}
	return a.getStatus(a.ctx)
}

func (a *App) PurgeAll() (domain.IndexStatus, error) {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	if err := a.store.PurgeAllData(a.ctx); err != nil {
		return domain.IndexStatus{}, err
	}
	if a.vectorIndex != nil {
		if err := a.vectorIndex.Rebuild(nil); err != nil {
			runtime.LogWarningf(a.ctx, "vector reset after purge all failed: %v", err)
		} else if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph save after purge all failed: %v", err)
		}
	} else {
		_ = os.Remove(a.vectorGraphPath())
	}
	_ = os.RemoveAll(filepath.Join(a.cfg.DataDir, "cache"))
	return a.getStatus(a.ctx)
}

func BuildBestEffortDeepLink(chatID int64, msgID int64) string {
	if chatID == 0 || msgID <= 0 {
		return ""
	}
	// Prefer Telegram deep links that open in the native app (no browser hop).
	// For supergroups/channels, chat IDs are commonly represented as -100<channel_id>.
	if channelID, ok := toTmeChannelID(chatID); ok {
		return fmt.Sprintf("tg://privatepost?channel=%d&post=%d", channelID, msgID)
	}
	// For private chats, the peer ID is the user ID.
	if chatID > 0 {
		return fmt.Sprintf("tg://openmessage?user_id=%d&message_id=%d", chatID, msgID)
	}
	return fmt.Sprintf("tg://openmessage?chat_id=%d&message_id=%d", chatID, msgID)
}

func BuildChatDeepLink(chatID int64) string {
	if chatID == 0 {
		return ""
	}
	return fmt.Sprintf("tg://openmessage?chat_id=%d", chatID)
}

func toTmeChannelID(chatID int64) (int64, bool) {
	// See https://core.telegram.org/api/links#message-links (private links: t.me/c/<channel>/<id>)
	// For channels/supergroups, chat IDs are commonly represented as -100<channel_id>.
	if chatID > -1000000000000 {
		return 0, false
	}
	channelID := (-chatID) - 1000000000000
	if channelID <= 0 {
		return 0, false
	}
	return channelID, true
}

func (a *App) ToggleMCP(enable bool) error {
	if a.store == nil {
		return errors.New("store is not initialized")
	}
	stored := "0"
	if enable {
		stored = "1"
	}
	if err := a.store.SetSetting(a.ctx, "mcp_enabled", stored); err != nil {
		return err
	}
	if enable {
		if err := a.startMCP(a.ctx); err != nil {
			return err
		}
		return nil
	}
	_ = a.stopMCP(context.Background())
	port, _ := a.store.GetSettingInt(a.ctx, "mcp_port", 0)
	a.setMCPRuntime("disabled", port)
	return nil
}

func (a *App) MCPPort() (int, error) {
	if a.store == nil {
		return 0, errors.New("store is not initialized")
	}
	return a.store.GetSettingInt(a.ctx, "mcp_port", 0)
}

func (a *App) SetMCPPort(port int) (domain.IndexStatus, error) {
	if a.store == nil {
		return domain.IndexStatus{}, errors.New("store is not initialized")
	}
	if port < 0 || port > 65535 {
		return domain.IndexStatus{}, errors.New("mcp port must be in range 0..65535")
	}
	if err := a.store.SetSetting(a.ctx, "mcp_port", strconv.Itoa(port)); err != nil {
		return domain.IndexStatus{}, err
	}
	a.setMCPRuntime("", port)

	enabled, err := a.store.GetSettingBool(a.ctx, "mcp_enabled", true)
	if err != nil {
		return domain.IndexStatus{}, err
	}
	if !enabled {
		return a.getStatus(a.ctx)
	}
	_ = a.stopMCP(context.Background())
	if err := a.startMCP(a.ctx); err != nil {
		return domain.IndexStatus{}, err
	}
	return a.getStatus(a.ctx)
}

func (a *App) ExitApp() {
	a.mu.Lock()
	a.quitNow = true
	a.mu.Unlock()
	runtime.Quit(a.ctx)
}

func (a *App) RebuildSemanticIndex() string {
	a.maintenance.Lock()
	defer a.maintenance.Unlock()
	if a.store == nil {
		return "Store is not initialized"
	}
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	if err := a.store.ResetEmbeddings(ctx); err != nil {
		return fmt.Sprintf("Rebuild failed: %v", err)
	}
	if _, err := a.store.BackfillEmbeddingsForEnabledChats(ctx); err != nil {
		runtime.LogWarningf(a.ctx, "embeddings backfill scope update failed: %v", err)
	}
	if a.vectorIndex != nil {
		if err := a.vectorIndex.Rebuild(nil); err != nil {
			return fmt.Sprintf("Rebuild failed: %v", err)
		}
		if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph reset save failed: %v", err)
		}
	}
	go func() {
		scanCtx, scanCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer scanCancel()
		a.enqueueEmbeddingCandidates(scanCtx)
		a.enqueueURLEmbeddingCandidates(scanCtx)
		a.enqueueFileEmbeddingCandidates(scanCtx)
	}()
	return fmt.Sprintf("Semantic index rebuild started at %s", time.Now().Format(time.RFC3339))
}

func (a *App) SyncNow() (domain.IndexStatus, error) {
	if a.BackgroundPaused() {
		return a.getStatus(a.ctx)
	}
	runCtx, cancel := context.WithTimeout(a.ctx, 4*time.Minute)
	defer cancel()
	if err := a.syncEnabledChats(runCtx, true); err != nil {
		return domain.IndexStatus{}, err
	}
	return a.getStatus(a.ctx)
}

func (a *App) runBackgroundSyncLoop(ctx context.Context) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			_ = a.syncEnabledChats(runCtx, false)
			cancel()
			a.maybeStartPDFBackfill()
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			_ = a.syncEnabledChats(runCtx, false)
			cancel()
			a.maybeStartPDFBackfill()
		}
	}
}

func (a *App) maybeStartPDFBackfill() {
	if a.store == nil {
		return
	}
	nowUnix := time.Now().Unix()

	last, err := a.store.GetSettingInt(a.ctx, "pdf_backfill_last_unix", 0)
	if err == nil && last > 0 && nowUnix-int64(last) < int64((24*time.Hour).Seconds()) {
		return
	}

	a.pdfBackfillMu.Lock()
	if a.pdfBackfillRunning {
		a.pdfBackfillMu.Unlock()
		return
	}
	a.pdfBackfillRunning = true
	a.pdfBackfillMu.Unlock()

	go func() {
		defer func() {
			a.pdfBackfillMu.Lock()
			a.pdfBackfillRunning = false
			a.pdfBackfillMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		sinceUnix := nowUnix - int64((30 * 24 * time.Hour).Seconds())
		a.backfillPDFURLs(ctx, sinceUnix)
		a.backfillTelegramPDFs(ctx, sinceUnix)

		if err := a.store.SetSetting(context.Background(), "pdf_backfill_last_unix", strconv.FormatInt(nowUnix, 10)); err != nil {
			runtime.LogWarningf(a.ctx, "PDF backfill timestamp update failed: %v", err)
		}
	}()
}

func (a *App) backfillPDFURLs(ctx context.Context, sinceUnix int64) {
	if a.store == nil {
		return
	}
	beforeTS := time.Now().Unix() + 1
	beforeChatID := int64(1<<62 - 1)
	beforeMsgID := int64(1<<62 - 1)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		candidates, err := a.store.ListPDFURLBackfillCandidates(ctx, sinceUnix, beforeTS, beforeChatID, beforeMsgID, 400)
		if err != nil {
			runtime.LogWarningf(a.ctx, "PDF URL backfill scan failed: %v", err)
			return
		}
		if len(candidates) == 0 {
			return
		}
		for _, candidate := range candidates {
			urls := urlfetch.ExtractURLs(candidate.Text, security.DefaultMaxURLsMessage)
			for _, rawURL := range urls {
				if !looksLikePDFURL(rawURL) {
					continue
				}
				if err := a.store.EnqueueURLTask(ctx, candidate.ChatID, candidate.MsgID, rawURL, 8); err != nil {
					runtime.LogWarningf(a.ctx, "PDF URL backfill enqueue failed chat=%d msg=%d url=%s err=%v", candidate.ChatID, candidate.MsgID, rawURL, err)
				}
			}
		}

		last := candidates[len(candidates)-1]
		beforeTS = last.Timestamp
		beforeChatID = last.ChatID
		beforeMsgID = last.MsgID
	}
}

func (a *App) backfillTelegramPDFs(ctx context.Context, sinceUnix int64) {
	if a.store == nil || a.telegramSvc == nil {
		return
	}
	chatIDs, err := a.enabledChatIDs(ctx)
	if err != nil || len(chatIDs) == 0 {
		return
	}

	files, err := a.telegramSvc.BackfillPDFFilesSince(ctx, chatIDs, sinceUnix, 250)
	if err != nil {
		if errors.Is(err, telegram.ErrNotConfigured) || errors.Is(err, telegram.ErrUnauthorized) {
			return
		}
		runtime.LogWarningf(a.ctx, "Telegram PDF backfill failed: %v", err)
		return
	}
	if err := a.upsertFilesAndQueuePDFTasks(ctx, files); err != nil {
		runtime.LogWarningf(a.ctx, "Telegram PDF backfill persist failed: %v", err)
	}
}

func (a *App) runRealtimeLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		chatIDs, err := a.enabledChatIDs(ctx)
		if err != nil {
			runtime.LogWarningf(a.ctx, "Realtime precheck failed: %v", err)
			if !sleepOrDone(ctx, 15*time.Second) {
				return
			}
			continue
		}
		if len(chatIDs) == 0 {
			if !sleepOrDone(ctx, 20*time.Second) {
				return
			}
			continue
		}

		burstCtx, cancel := context.WithTimeout(ctx, realtimeBurst)
		err = a.telegramSvc.RunRealtime(burstCtx, chatIDs, func(event telegram.LiveEvent) error {
			switch event.Kind {
			case telegram.LiveEventUpsert:
				if err := a.upsertMessageAndQueueURLs(burstCtx, domain.Message{
					ChatID:        event.Message.ChatID,
					MsgID:         event.Message.MsgID,
					Timestamp:     event.Message.Timestamp,
					EditTS:        event.Message.EditTS,
					SenderID:      event.Message.SenderID,
					SenderDisplay: event.Message.SenderDisplay,
					Text:          event.Message.Text,
					Deleted:       false,
					HasURL:        event.Message.HasURL,
				}); err != nil {
					return err
				}
				return a.upsertFilesAndQueuePDFTasks(burstCtx, event.Files)
			case telegram.LiveEventDelete:
				if event.ChatID == 0 || event.MsgID == 0 {
					return nil
				}
				return a.deleteMessageAndTombstone(burstCtx, event.ChatID, event.MsgID)
			default:
				return nil
			}
		})
		cancel()

		if err != nil &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, telegram.ErrUnauthorized) &&
			!errors.Is(err, telegram.ErrNotConfigured) {
			runtime.LogWarningf(a.ctx, "Realtime burst failed: %v", err)
			if !sleepOrDone(ctx, 10*time.Second) {
				return
			}
			continue
		}

		if !sleepOrDone(ctx, 2*time.Second) {
			return
		}
	}
}

func (a *App) syncEnabledChats(ctx context.Context, manual bool) error {
	if a.store == nil || a.telegramSvc == nil {
		return errors.New("sync services are not initialized")
	}

	a.syncRunMu.Lock()
	defer a.syncRunMu.Unlock()
	_, currentProgress, currentLast := a.syncSnapshot()
	a.setSyncStatus("syncing", currentProgress, currentLast)

	chats, err := a.store.ListChats(ctx)
	if err != nil {
		_, progress, last := a.syncSnapshot()
		a.setSyncStatus("error", progress, last)
		return err
	}

	states := make([]telegram.SyncChatState, 0, len(chats))
	for _, chat := range chats {
		if !chat.Enabled {
			continue
		}
		cursor := chat.SyncCursor
		if strings.EqualFold(chat.HistoryMode, "lazy") {
			cursor = ""
		}
		states = append(states, telegram.SyncChatState{
			ChatID:          chat.ChatID,
			SyncCursor:      cursor,
			LastMessageUnix: chat.LastMessageUnix,
		})
	}

	if len(states) == 0 {
		a.setSyncStatus("idle", 100, time.Now().Unix())
		return nil
	}

	report, err := a.telegramSvc.SyncChats(ctx, states, syncMaxMessagesPerRun)
	if err != nil {
		if errors.Is(err, telegram.ErrNotConfigured) || errors.Is(err, telegram.ErrUnauthorized) {
			_, progress, last := a.syncSnapshot()
			a.setSyncStatus("awaiting_auth", progress, last)
			if manual {
				return err
			}
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			_, progress, last := a.syncSnapshot()
			a.setSyncStatus("idle", progress, last)
			if manual {
				return err
			}
			return nil
		}
		_, progress, last := a.syncSnapshot()
		a.setSyncStatus("error", progress, last)
		return err
	}

	for _, item := range report.Messages {
		if upsertErr := a.upsertMessageAndQueueURLs(ctx, domain.Message{
			ChatID:        item.ChatID,
			MsgID:         item.MsgID,
			Timestamp:     item.Timestamp,
			EditTS:        item.EditTS,
			SenderID:      item.SenderID,
			SenderDisplay: item.SenderDisplay,
			Text:          item.Text,
			Deleted:       false,
			HasURL:        item.HasURL,
		}); upsertErr != nil {
			_, progress, last := a.syncSnapshot()
			a.setSyncStatus("error", progress, last)
			return upsertErr
		}
	}

	if upsertErr := a.upsertFilesAndQueuePDFTasks(ctx, report.Files); upsertErr != nil {
		_, progress, last := a.syncSnapshot()
		a.setSyncStatus("error", progress, last)
		return upsertErr
	}

	for _, item := range report.Chats {
		if syncErr := a.store.UpdateChatSyncState(ctx, item.ChatID, item.NextCursor, item.LastMessageUnix, item.LastSyncedUnix); syncErr != nil {
			_, progress, last := a.syncSnapshot()
			a.setSyncStatus("error", progress, last)
			return syncErr
		}
	}

	progress, progressErr := a.calculateBackfillProgress(ctx)
	if progressErr != nil {
		_, currentProgress, currentLast := a.syncSnapshot()
		a.setSyncStatus("error", currentProgress, currentLast)
		return progressErr
	}
	a.setSyncStatus("idle", progress, report.SyncedAtUnix)
	return nil
}

func (a *App) calculateBackfillProgress(ctx context.Context) (int, error) {
	chats, err := a.store.ListChats(ctx)
	if err != nil {
		return 0, err
	}

	total := 0
	done := 0
	for _, chat := range chats {
		if !chat.Enabled {
			continue
		}
		if strings.EqualFold(chat.HistoryMode, "lazy") {
			continue
		}
		total++
		if strings.TrimSpace(chat.SyncCursor) == "" {
			done++
		}
	}

	if total == 0 {
		return 100, nil
	}
	return int(math.Round(float64(done) * 100 / float64(total))), nil
}

func (a *App) enabledChatIDs(ctx context.Context) ([]int64, error) {
	chats, err := a.store.ListChats(ctx)
	if err != nil {
		return nil, err
	}

	chatIDs := make([]int64, 0, len(chats))
	for _, chat := range chats {
		if chat.Enabled {
			chatIDs = append(chatIDs, chat.ChatID)
		}
	}
	return chatIDs, nil
}

func (a *App) upsertFilesAndQueuePDFTasks(ctx context.Context, files []telegram.SyncedFile) error {
	if a.store == nil {
		return nil
	}
	if len(files) == 0 {
		return nil
	}
	nowUnix := time.Now().Unix()
	for _, f := range files {
		if f.ChatID == 0 || f.MsgID == 0 || f.DocumentID == 0 {
			continue
		}
		if f.Size > int64(security.DefaultMaxPDFSizeBytes) && f.Size > 0 {
			continue
		}

		if err := a.store.UpsertTGFile(ctx, sqlite.TGFile{
			DocumentID:    f.DocumentID,
			AccessHash:    f.AccessHash,
			DCID:          f.DCID,
			FileReference: f.FileReference,
			Mime:          f.MimeType,
			Size:          f.Size,
			Filename:      f.FileName,
			UpdatedAt:     nowUnix,
		}); err != nil {
			return err
		}
		if err := a.store.LinkMessageFile(ctx, f.ChatID, f.MsgID, f.DocumentID); err != nil {
			return err
		}
		if err := a.store.EnqueuePDFTask(ctx, f.ChatID, f.MsgID, f.DocumentID, 9); err != nil {
			runtime.LogWarningf(a.ctx, "PDF task enqueue failed chat=%d msg=%d doc=%d err=%v", f.ChatID, f.MsgID, f.DocumentID, err)
		}
	}
	return nil
}

func (a *App) upsertMessageAndQueueURLs(ctx context.Context, message domain.Message) error {
	oldChunkIDs, _ := a.store.ListChunkIDsByMessage(ctx, message.ChatID, message.MsgID)
	if err := a.store.UpsertMessage(ctx, message); err != nil {
		return err
	}
	for _, chunkID := range oldChunkIDs {
		if a.vectorIndex != nil {
			a.vectorIndex.MarkDeleted(chunkID)
		}
	}
	if len(oldChunkIDs) > 0 && a.vectorIndex != nil {
		if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph save after message upsert failed: %v", err)
		}
	}
	if message.Deleted || !message.HasURL {
		return nil
	}

	chat, err := a.store.GetChatPolicy(ctx, message.ChatID)
	if err != nil {
		return nil
	}
	if !chat.Enabled {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(chat.URLsMode))

	priority := 10
	if mode == "full" {
		priority = 5
	}
	pdfPriority := priority
	enqueueNonPDF := mode != "" && mode != "off"
	if !enqueueNonPDF {
		pdfPriority = 8
	}
	urls := urlfetch.ExtractURLs(message.Text, security.DefaultMaxURLsMessage)
	for _, item := range urls {
		if looksLikePDFURL(item) {
			if err := a.store.EnqueueURLTask(ctx, message.ChatID, message.MsgID, item, pdfPriority); err != nil {
				runtime.LogWarningf(a.ctx, "PDF URL enqueue failed chat=%d msg=%d url=%s err=%v", message.ChatID, message.MsgID, item, err)
			}
			continue
		}
		if !enqueueNonPDF {
			continue
		}
		if err := a.store.EnqueueURLTask(ctx, message.ChatID, message.MsgID, item, priority); err != nil {
			runtime.LogWarningf(a.ctx, "URL enqueue failed chat=%d msg=%d url=%s err=%v", message.ChatID, message.MsgID, item, err)
		}
	}
	return nil
}

func (a *App) deleteMessageAndTombstone(ctx context.Context, chatID int64, msgID int64) error {
	chunkIDs, _ := a.store.ListChunkIDsByMessage(ctx, chatID, msgID)
	if err := a.store.MarkMessageDeleted(ctx, chatID, msgID); err != nil {
		return err
	}
	if a.vectorIndex == nil || len(chunkIDs) == 0 {
		return nil
	}
	for _, chunkID := range chunkIDs {
		a.vectorIndex.MarkDeleted(chunkID)
	}
	if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
		runtime.LogWarningf(a.ctx, "vector graph save after delete failed: %v", err)
	}
	return nil
}

func (a *App) runURLFetchLoop(ctx context.Context) {
	scanTicker := time.NewTicker(20 * time.Second)
	defer scanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTicker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			a.enqueueURLCandidates(scanCtx)
			cancel()
		default:
		}

		taskCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		task, ok, err := a.store.ClaimURLTask(taskCtx, time.Now().Unix())
		cancel()
		if err != nil {
			runtime.LogWarningf(a.ctx, "URL task claim failed: %v", err)
			if !sleepOrDone(ctx, 3*time.Second) {
				return
			}
			continue
		}
		if !ok {
			if !sleepOrDone(ctx, 2*time.Second) {
				return
			}
			continue
		}

		processCtx, processCancel := context.WithTimeout(ctx, 30*time.Second)
		processErr := a.processURLTask(processCtx, task)
		processCancel()
		if processErr == nil {
			if err := a.store.CompleteTask(ctx, task.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "URL task complete failed id=%d err=%v", task.TaskID, err)
			}
			continue
		}

		if task.Attempts >= urlTaskMaxAttempts {
			if err := a.store.FailTask(ctx, task.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "URL task fail mark failed id=%d err=%v", task.TaskID, err)
			}
			continue
		}

		backoff := int64(task.Attempts * 30)
		if retryErr := a.store.RetryTask(ctx, task.TaskID, time.Now().Unix()+backoff); retryErr != nil {
			runtime.LogWarningf(a.ctx, "URL task retry failed id=%d err=%v", task.TaskID, retryErr)
		}
	}
}

func (a *App) enqueueURLCandidates(ctx context.Context) {
	candidates, err := a.store.ListURLQueueCandidates(ctx, urlCandidateScanLimit)
	if err != nil {
		runtime.LogWarningf(a.ctx, "URL candidate scan failed: %v", err)
		return
	}
	for _, candidate := range candidates {
		mode := strings.ToLower(strings.TrimSpace(candidate.URLsMode))
		priority := 10
		if mode == "full" {
			priority = 5
		}
		urls := urlfetch.ExtractURLs(candidate.Text, security.DefaultMaxURLsMessage)
		for _, rawURL := range urls {
			if err := a.store.EnqueueURLTask(ctx, candidate.ChatID, candidate.MsgID, rawURL, priority); err != nil {
				runtime.LogWarningf(a.ctx, "URL candidate enqueue failed chat=%d msg=%d err=%v", candidate.ChatID, candidate.MsgID, err)
			}
		}
	}
}

func (a *App) processURLTask(ctx context.Context, task sqlite.URLTask) error {
	message, err := a.store.GetMessage(ctx, task.ChatID, task.MsgID)
	if err != nil || message.Deleted {
		return nil
	}
	chat, err := a.store.GetChatPolicy(ctx, task.ChatID)
	if err != nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(chat.URLsMode))
	if !chat.Enabled {
		return nil
	}
	if mode == "" || mode == "off" {
		// PDF URLs are always indexed, regardless of URLs mode.
		if !looksLikePDFURL(task.URL) {
			return nil
		}
	}

	result, err := urlfetch.Fetch(ctx, task.URL)
	if err != nil {
		runtime.LogWarningf(a.ctx, "URL fetch failed chat=%d msg=%d url=%s err=%v", task.ChatID, task.MsgID, task.URL, err)
		return err
	}
	if strings.TrimSpace(result.ExtractedText) == "" {
		return nil
	}
	return a.store.UpsertURLDoc(
		ctx,
		task.ChatID,
		task.MsgID,
		task.URL,
		result.FinalURL,
		result.Title,
		result.ExtractedText,
		result.ContentType,
		result.Hash,
		time.Now().Unix(),
	)
}

func (a *App) runPDFFetchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		taskCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		task, ok, err := a.store.ClaimPDFTask(taskCtx, time.Now().Unix())
		cancel()
		if err != nil {
			runtime.LogWarningf(a.ctx, "PDF task claim failed: %v", err)
			if !sleepOrDone(ctx, 3*time.Second) {
				return
			}
			continue
		}
		if !ok {
			if !sleepOrDone(ctx, 2*time.Second) {
				return
			}
			continue
		}

		processCtx, processCancel := context.WithTimeout(ctx, 60*time.Second)
		processErr := a.processPDFTask(processCtx, task)
		processCancel()
		if processErr == nil {
			if err := a.store.CompleteTask(ctx, task.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "PDF task complete failed id=%d err=%v", task.TaskID, err)
			}
			continue
		}

		if task.Attempts >= pdfTaskMaxAttempts {
			if err := a.store.FailTask(ctx, task.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "PDF task fail mark failed id=%d err=%v", task.TaskID, err)
			}
			continue
		}
		backoff := int64(task.Attempts * 45)
		if err := a.store.RetryTask(ctx, task.TaskID, time.Now().Unix()+backoff); err != nil {
			runtime.LogWarningf(a.ctx, "PDF task retry failed id=%d err=%v", task.TaskID, err)
		}
	}
}

func (a *App) processPDFTask(ctx context.Context, task sqlite.PDFTask) error {
	if a.store == nil || a.telegramSvc == nil {
		return nil
	}
	if task.ChatID == 0 || task.MsgID == 0 || task.DocumentID == 0 {
		return nil
	}

	message, err := a.store.GetMessage(ctx, task.ChatID, task.MsgID)
	if err != nil || message.Deleted {
		return nil
	}
	chat, err := a.store.GetChatPolicy(ctx, task.ChatID)
	if err != nil || !chat.Enabled {
		return nil
	}

	meta, err := a.store.GetTGFile(ctx, task.DocumentID)
	if err != nil {
		return nil
	}
	if meta.Size > int64(security.DefaultMaxPDFSizeBytes) && meta.Size > 0 {
		runtime.LogWarningf(a.ctx, "PDF too large, skipping chat=%d msg=%d doc=%d size=%d", task.ChatID, task.MsgID, task.DocumentID, meta.Size)
		return nil
	}

	pdfBytes, err := a.telegramSvc.DownloadDocumentBytes(ctx, meta.DocumentID, meta.AccessHash, meta.FileReference, int64(security.DefaultMaxPDFSizeBytes))
	if err != nil {
		if errors.Is(err, telegram.ErrFileReferenceExpired) {
			refreshed, refreshErr := a.telegramSvc.FetchMessagePDFMeta(ctx, task.ChatID, task.MsgID)
			if refreshErr == nil {
				for _, f := range refreshed {
					if f.DocumentID != task.DocumentID {
						continue
					}
					_ = a.store.UpsertTGFile(ctx, sqlite.TGFile{
						DocumentID:    f.DocumentID,
						AccessHash:    f.AccessHash,
						DCID:          f.DCID,
						FileReference: f.FileReference,
						Mime:          f.MimeType,
						Size:          f.Size,
						Filename:      f.FileName,
						UpdatedAt:     time.Now().Unix(),
					})
					meta.AccessHash = f.AccessHash
					meta.FileReference = f.FileReference
					meta.DCID = f.DCID
					meta.Mime = f.MimeType
					meta.Size = f.Size
					meta.Filename = f.FileName
					break
				}
			}
			pdfBytes, err = a.telegramSvc.DownloadDocumentBytes(ctx, meta.DocumentID, meta.AccessHash, meta.FileReference, int64(security.DefaultMaxPDFSizeBytes))
		}
	}
	if err != nil {
		return err
	}

	hashRaw := sha256.Sum256(pdfBytes)
	hash := hex.EncodeToString(hashRaw[:])

	extracted, extractErr := pdfextract.ExtractText(pdfBytes)
	if extractErr != nil {
		extracted = ""
	}
	if extracted != "" {
		const maxRunes = 200000
		runes := []rune(extracted)
		if len(runes) > maxRunes {
			extracted = string(runes[:maxRunes])
		}
	}

	docID, err := a.store.UpsertFileDoc(ctx, task.ChatID, task.MsgID, task.DocumentID, meta.Filename, meta.Mime, meta.Size, extracted, hash, time.Now().Unix())
	if err != nil {
		return err
	}
	if docID == 0 || strings.TrimSpace(extracted) == "" {
		return nil
	}
	if chat.AllowEmbeddings {
		if err := a.store.EnqueueFileEmbeddingTask(ctx, task.ChatID, task.MsgID, docID, 10); err != nil {
			runtime.LogWarningf(a.ctx, "File embedding enqueue failed doc_id=%d err=%v", docID, err)
		}
	}
	return nil
}

func (a *App) runEmbeddingsLoop(ctx context.Context) {
	scanTicker := time.NewTicker(25 * time.Second)
	defer scanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTicker.C:
			scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			a.enqueueEmbeddingCandidates(scanCtx)
			a.enqueueURLEmbeddingCandidates(scanCtx)
			a.enqueueFileEmbeddingCandidates(scanCtx)
			cancel()
		default:
		}

		client := a.embedClient
		if client == nil || !client.Configured() {
			if !sleepOrDone(ctx, 4*time.Second) {
				return
			}
			continue
		}

		taskCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		work, ok, err := a.store.ClaimEmbeddingWork(taskCtx, time.Now().Unix())
		cancel()
		if err != nil {
			runtime.LogWarningf(a.ctx, "Embedding task claim failed: %v", err)
			if !sleepOrDone(ctx, 3*time.Second) {
				return
			}
			continue
		}
		if !ok {
			if !sleepOrDone(ctx, 2*time.Second) {
				return
			}
			continue
		}

		processCtx, processCancel := context.WithTimeout(ctx, 45*time.Second)
		processErr := a.processEmbeddingWork(processCtx, client, work)
		processCancel()
		if processErr == nil {
			if err := a.store.CompleteTask(ctx, work.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "Embedding task complete failed id=%d err=%v", work.TaskID, err)
			}
			continue
		}

		if work.Attempts >= embedTaskMaxAttempts {
			if err := a.store.FailTask(ctx, work.TaskID); err != nil {
				runtime.LogWarningf(a.ctx, "Embedding task fail mark failed id=%d err=%v", work.TaskID, err)
			}
			continue
		}
		backoff := int64(work.Attempts * 45)
		if err := a.store.RetryTask(ctx, work.TaskID, time.Now().Unix()+backoff); err != nil {
			runtime.LogWarningf(a.ctx, "Embedding task retry failed id=%d err=%v", work.TaskID, err)
		}
	}
}

func (a *App) enqueueEmbeddingCandidates(ctx context.Context) {
	candidates, err := a.store.ListEmbeddingCandidates(ctx, embedCandidateLimit)
	if err != nil {
		runtime.LogWarningf(a.ctx, "Embedding candidate scan failed: %v", err)
		return
	}
	for _, item := range candidates {
		if isMostlyURLMessage(item.Text) {
			if err := a.store.MarkEmbeddingSkipped(ctx, item.ChunkID, "mostly_url", time.Now().Unix()); err != nil {
				runtime.LogWarningf(a.ctx, "Embedding skip mark failed chunk=%d err=%v", item.ChunkID, err)
			}
			continue
		}
		if err := a.store.EnqueueEmbeddingTask(ctx, item.ChunkID, 8); err != nil {
			runtime.LogWarningf(a.ctx, "Embedding enqueue failed chunk=%d err=%v", item.ChunkID, err)
		}
	}
}

func (a *App) enqueueURLEmbeddingCandidates(ctx context.Context) {
	candidates, err := a.store.ListURLEmbeddingCandidates(ctx, embedCandidateLimit)
	if err != nil {
		runtime.LogWarningf(a.ctx, "URL embedding candidate scan failed: %v", err)
		return
	}
	for _, item := range candidates {
		if strings.TrimSpace(item.Extracted) == "" {
			continue
		}
		if err := a.store.EnqueueURLEmbeddingTask(ctx, item.URLID, 9); err != nil {
			runtime.LogWarningf(a.ctx, "URL embedding enqueue failed url_id=%d err=%v", item.URLID, err)
		}
	}
}

func (a *App) enqueueFileEmbeddingCandidates(ctx context.Context) {
	candidates, err := a.store.ListFileEmbeddingCandidates(ctx, embedCandidateLimit)
	if err != nil {
		runtime.LogWarningf(a.ctx, "File embedding candidate scan failed: %v", err)
		return
	}
	for _, item := range candidates {
		if strings.TrimSpace(item.Extracted) == "" {
			continue
		}
		if err := a.store.EnqueueFileEmbeddingTask(ctx, item.ChatID, item.MsgID, item.DocID, 10); err != nil {
			runtime.LogWarningf(a.ctx, "File embedding enqueue failed doc_id=%d err=%v", item.DocID, err)
		}
	}
}

func (a *App) processEmbeddingWork(ctx context.Context, client *embeddings.HTTPClient, work sqlite.EmbeddingWork) error {
	switch strings.ToLower(strings.TrimSpace(work.Kind)) {
	case "chunk":
		return a.processEmbeddingTask(ctx, client, sqlite.EmbeddingTask{
			TaskID:   work.TaskID,
			ChunkID:  work.ChunkID,
			Attempts: work.Attempts,
		})
	case "url":
		return a.processURLEmbeddingTask(ctx, client, work.URLID)
	case "file":
		return a.processFileEmbeddingTask(ctx, client, work.FileDocID)
	default:
		return nil
	}
}

func (a *App) processEmbeddingTask(ctx context.Context, client *embeddings.HTTPClient, task sqlite.EmbeddingTask) error {
	chunk, err := a.store.LoadChunkForEmbedding(ctx, task.ChunkID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	text := strings.TrimSpace(chunk.Text)
	if text == "" {
		return nil
	}
	if isMostlyURLMessage(text) {
		if err := a.store.MarkEmbeddingSkipped(ctx, chunk.ChunkID, "mostly_url", time.Now().Unix()); err != nil {
			runtime.LogWarningf(a.ctx, "Embedding skip mark failed chunk=%d err=%v", chunk.ChunkID, err)
		}
		if err := a.store.DeleteEmbedding(ctx, chunk.ChunkID); err != nil {
			runtime.LogWarningf(a.ctx, "Embedding delete failed chunk=%d err=%v", chunk.ChunkID, err)
		}
		if a.vectorIndex != nil {
			a.vectorIndex.MarkDeleted(chunk.ChunkID)
			if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
				runtime.LogWarningf(a.ctx, "vector graph save after url-only skip failed: %v", err)
			}
		}
		return nil
	}
	vectors, err := client.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return errors.New("empty embedding vector")
	}
	model, _ := a.store.GetSetting(ctx, "embeddings_model", "text-embedding-3-large")
	if err := a.store.UpsertEmbedding(ctx, chunk.ChunkID, model, vectors[0], time.Now().Unix()); err != nil {
		return err
	}
	if a.vectorIndex != nil {
		if err := a.vectorIndex.Add(chunk.ChunkID, vectors[0]); err != nil {
			runtime.LogWarningf(a.ctx, "vector add failed chunk=%d err=%v", chunk.ChunkID, err)
			return nil
		}
		if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph save failed: %v", err)
		}
	}
	return nil
}

func (a *App) processURLEmbeddingTask(ctx context.Context, client *embeddings.HTTPClient, urlID int64) error {
	if urlID == 0 {
		return nil
	}
	doc, err := a.store.LoadURLDocForEmbedding(ctx, urlID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	text := buildURLEmbeddingText(doc)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	vectors, err := client.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return errors.New("empty embedding vector")
	}
	model, _ := a.store.GetSetting(ctx, "embeddings_model", "text-embedding-3-large")
	if err := a.store.UpsertURLEmbedding(ctx, urlID, model, vectors[0], time.Now().Unix()); err != nil {
		return err
	}
	if a.vectorIndex != nil {
		nodeID := vectorNodeIDForURL(urlID)
		if err := a.vectorIndex.Add(nodeID, vectors[0]); err != nil {
			runtime.LogWarningf(a.ctx, "vector add failed url_id=%d err=%v", urlID, err)
			return nil
		}
		if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph save failed: %v", err)
		}
	}
	return nil
}

func (a *App) processFileEmbeddingTask(ctx context.Context, client *embeddings.HTTPClient, docID int64) error {
	if docID == 0 {
		return nil
	}
	doc, err := a.store.LoadFileDocForEmbedding(ctx, docID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	text := buildFileEmbeddingText(doc)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	vectors, err := client.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return errors.New("empty embedding vector")
	}
	model, _ := a.store.GetSetting(ctx, "embeddings_model", "text-embedding-3-large")
	if err := a.store.UpsertFileEmbedding(ctx, docID, model, vectors[0], time.Now().Unix()); err != nil {
		return err
	}
	if a.vectorIndex != nil {
		nodeID := vectorNodeIDForFileDoc(docID)
		if err := a.vectorIndex.Add(nodeID, vectors[0]); err != nil {
			runtime.LogWarningf(a.ctx, "vector add failed file_doc_id=%d err=%v", docID, err)
			return nil
		}
		if err := a.vectorIndex.Save(a.vectorGraphPath()); err != nil {
			runtime.LogWarningf(a.ctx, "vector graph save failed: %v", err)
		}
	}
	return nil
}

func vectorNodeIDForURL(urlID int64) int64 {
	if urlID == 0 {
		return 0
	}
	return -urlID
}

const vectorFileDocOffset int64 = 1_000_000_000_000

func vectorNodeIDForFileDoc(docID int64) int64 {
	if docID <= 0 {
		return 0
	}
	return -(vectorFileDocOffset + docID)
}

func buildURLEmbeddingText(doc sqlite.URLEmbeddingCandidate) string {
	var b strings.Builder
	title := strings.TrimSpace(doc.Title)
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}
	extracted := strings.TrimSpace(doc.Extracted)
	if extracted == "" {
		return strings.TrimSpace(b.String())
	}

	// Keep it bounded to avoid sending huge pages to the embeddings endpoint.
	const maxRunes = 8000
	runes := []rune(extracted)
	if len(runes) > maxRunes {
		extracted = string(runes[:maxRunes])
	}
	b.WriteString(extracted)
	return strings.TrimSpace(b.String())
}

func buildFileEmbeddingText(doc sqlite.FileEmbeddingCandidate) string {
	var b strings.Builder
	name := strings.TrimSpace(doc.Filename)
	if name != "" {
		b.WriteString(name)
		b.WriteString("\n")
	}
	extracted := strings.TrimSpace(doc.Extracted)
	if extracted == "" {
		return strings.TrimSpace(b.String())
	}

	const maxRunes = 8000
	runes := []rune(extracted)
	if len(runes) > maxRunes {
		extracted = string(runes[:maxRunes])
	}
	b.WriteString(extracted)
	return strings.TrimSpace(b.String())
}

func looksLikePDFURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return strings.EqualFold(path.Ext(parsed.Path), ".pdf")
}

func isMostlyURLMessage(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	urls := urlfetch.ExtractURLs(trimmed, security.DefaultMaxURLsMessage)
	if len(urls) == 0 {
		return false
	}
	totalRunes := len([]rune(trimmed))
	if totalRunes == 0 {
		return false
	}

	urlRunes := 0
	without := trimmed
	for _, u := range urls {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		urlRunes += len([]rune(u))
		without = strings.ReplaceAll(without, u, " ")
	}

	restLettersDigits := 0
	for _, r := range without {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			restLettersDigits++
		}
	}

	if totalRunes > 0 && float64(urlRunes)/float64(totalRunes) >= 0.90 {
		return true
	}
	if restLettersDigits <= 3 {
		return true
	}
	if totalRunes > 0 && float64(urlRunes)/float64(totalRunes) >= 0.80 && restLettersDigits <= 20 {
		return true
	}
	return false
}

func (a *App) setSyncStatus(state string, progress int, lastSyncUnix int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncState = state
	if progress >= 0 {
		a.syncBackfillProgress = progress
	}
	if lastSyncUnix > 0 {
		a.syncLastUnix = lastSyncUnix
	}
}

func (a *App) syncSnapshot() (string, int, int64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.syncState, a.syncBackfillProgress, a.syncLastUnix
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func addFileToZip(zw *zip.Writer, sourcePath, archiveName string) error {
	file, err := os.Open(filepath.Clean(sourcePath))
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = archiveName
	header.Method = zip.Deflate
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, file)
	return err
}

func unzipArchive(archivePath, destinationDir string) error {
	reader, err := zip.OpenReader(filepath.Clean(archivePath))
	if err != nil {
		return err
	}
	defer reader.Close()

	base := filepath.Clean(destinationDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	for _, item := range reader.File {
		target := filepath.Join(base, item.Name)
		cleanTarget := filepath.Clean(target)
		if cleanTarget != base && !strings.HasPrefix(cleanTarget, base+string(os.PathSeparator)) {
			return errors.New("invalid archive path")
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
			return err
		}
		src, err := item.Open()
		if err != nil {
			return err
		}
		dst, err := os.Create(cleanTarget)
		if err != nil {
			src.Close()
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = dst.Close()
			_ = src.Close()
			return err
		}
		if err := dst.Close(); err != nil {
			_ = src.Close()
			return err
		}
		if err := src.Close(); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	src, err := os.Open(filepath.Clean(source))
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	tmpPath := destination + ".tmp"
	dst, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tmpPath, destination)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fuseByRRF(ftsResults, vectorResults []domain.SearchResult, limit int) []domain.SearchResult {
	unlimited := limit == -1
	if !unlimited && (limit <= 0 || limit > 100) {
		limit = 25
	}
	type scored struct {
		item domain.SearchResult
		rrf  float64
	}
	merged := map[string]scored{}
	apply := func(items []domain.SearchResult, weight float64) {
		const k = 60.0
		for idx, item := range items {
			key := resultKey(item.ChatID, item.MsgID)
			current, ok := merged[key]
			if !ok {
				current = scored{item: item}
			}
			current.rrf += weight / (k + float64(idx+1))
			if item.Timestamp > current.item.Timestamp {
				current.item.Timestamp = item.Timestamp
			}
			if current.item.Snippet == "" {
				current.item.Snippet = item.Snippet
			}
			current.item.MatchFTS = current.item.MatchFTS || item.MatchFTS
			current.item.MatchSemantic = current.item.MatchSemantic || item.MatchSemantic
			merged[key] = current
		}
	}
	apply(ftsResults, 1.0)
	apply(vectorResults, 1.0)

	out := make([]scored, 0, len(merged))
	nowUnix := time.Now().Unix()
	for _, item := range merged {
		item.rrf += recencyBoostScore(item.item.Timestamp, nowUnix)
		item.item.Score = -item.rrf
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].rrf == out[j].rrf {
			return out[i].item.Timestamp > out[j].item.Timestamp
		}
		return out[i].rrf > out[j].rrf
	})
	resultCap := len(out)
	if !unlimited && limit < resultCap {
		resultCap = limit
	}
	results := make([]domain.SearchResult, 0, resultCap)
	maxIdx := len(out)
	if !unlimited && limit < maxIdx {
		maxIdx = limit
	}
	for idx := 0; idx < maxIdx; idx++ {
		results = append(results, out[idx].item)
	}
	return results
}

func resultKey(chatID int64, msgID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(msgID, 10)
}

func recencyBoostScore(timestamp int64, nowUnix int64) float64 {
	if timestamp <= 0 || nowUnix <= 0 {
		return 0
	}
	ageHours := float64(nowUnix-timestamp) / 3600.0
	if ageHours < 0 {
		ageHours = 0
	}
	const maxBoost = 0.003
	const halfLifeHours = 72.0
	return maxBoost / (1.0 + ageHours/halfLifeHours)
}

type queryService struct {
	app *App
}

func (q *queryService) ListChats(ctx context.Context) ([]domain.ChatPolicy, error) {
	return q.app.store.ListChats(ctx)
}

func (q *queryService) Search(ctx context.Context, req domain.SearchRequest) ([]domain.SearchResult, error) {
	results, err := q.app.searchMessages(ctx, req)
	if err != nil {
		return nil, err
	}
	for idx := range results {
		results[idx].DeepLink = BuildBestEffortDeepLink(results[idx].ChatID, results[idx].MsgID)
	}
	return results, nil
}

func (q *queryService) GetMessage(ctx context.Context, chatID int64, msgID int64) (domain.Message, error) {
	return q.app.store.GetMessage(ctx, chatID, msgID)
}

func (q *queryService) GetStatus(ctx context.Context) (domain.IndexStatus, error) {
	return q.app.getStatus(ctx)
}
