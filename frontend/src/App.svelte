<script>
  import { onMount } from "svelte";

  const backend = () => window?.go?.main?.App;

  let status = null;
  let telegramStatus = null;
  let onboarding = null;
  let chats = [];
  let query = "";
  let mode = "hybrid";
  let resultLimit = "top5"; // "top5" | "top10" | "all"
  let advanced = false;
  let results = [];
  let loading = false;
  let telegramBusy = false;
  let syncBusy = false;
  let maintenanceBusy = false;
  let mcpBusy = false;
  let onboardingBusy = false;
  let errorText = "";
  let infoText = "";
  let tgPhone = "";
  let tgCode = "";
  let tgPassword = "";
  let telegramEditMode = false;
  let backupPath = "";
  let restorePath = "";
  let embBaseURL = "";
  let embModel = "";
  let embDims = 3072;
  let embAPIKey = "";
  let embConfigured = false;
  let embTestBusy = false;
  let embTest = null;
  let embProgress = null;
  let embProgressBusy = false;
  let embProgressTimer = null;
  let statusPollTimer = null;
  let statusPollBusy = false;
  let statusPollTick = 0;
  let embeddingsEditMode = false;
  let semanticStrictness = "similar"; // "very" | "similar" | "weak"
  let semanticStrictnessBusy = false;
  let autostartEnabled = false;
  let backgroundPaused = false;
  let mcpPort = 0;
  let trayStatus = "unknown";
  let dataDirPath = "";
  let dataDirBusy = false;
  let storageEditMode = false;
  let currentPage = "search";
  let settingsTab = "runtime"; // "runtime" | "mcp" | "storage" | "telegram" | "embeddings" | "backup"

  const THEME_STORAGE_KEY = "asktg.theme";
  const SELECTED_FOLDER_ID = 1000000000;
  let themePreference = "system"; // "system" | "light" | "dark"
  let themeMediaQuery = null;

  let chatFolders = [];
  let activeChatFolderId = 0;
  let chatsById = {};
  let chatEdits = {};
  let applyAllBusy = false;
  let applyAllDone = 0;
  let applyAllTotal = 0;
  let chatFilter = "";

  $: onboardingIncomplete = Boolean(onboarding && !onboarding.completed);
  $: searchLocked = Boolean(onboarding && onboarding.enabled_chats < 1);
  $: activeChatFolder = chatFolders.find((f) => f.id === activeChatFolderId) || chatFolders[0] || null;
  $: visibleChatsInFolder = chatsForFolder(chats, activeChatFolder);
  $: visibleChats =
    (chatFilter || "").trim() === ""
      ? visibleChatsInFolder
      : visibleChatsInFolder.filter((c) => (c?.title || "").toLocaleLowerCase().includes(chatFilter.trim().toLocaleLowerCase()));
  $: dirtyChatIds = Object.keys(chatEdits).filter((id) => chatEdits[id]?.dirty);
  $: dirtyCount = dirtyChatIds.length;
  $: semanticResultCount = (results || []).filter((r) => r && r.match_semantic).length;
  $: ftsResultCount = (results || []).filter((r) => r && r.match_fts).length;
  $: embProgressPercent =
    embProgress && embProgress.total_eligible > 0
      ? Math.max(0, Math.min(100, Math.floor((embProgress.embedded / embProgress.total_eligible) * 100)))
      : 0;
  $: telegramConfigured = Boolean(
    telegramStatus &&
      (telegramStatus.configured || telegramStatus.authorized || String(telegramStatus.phone || "").trim() !== "")
  );

  function syncChatEditsFromChats(items) {
    const nextChatsById = {};
    const nextEdits = { ...chatEdits };

    for (const chat of items || []) {
      nextChatsById[chat.chat_id] = chat;

      const prev = nextEdits[chat.chat_id];
      const base = chat;
      if (!prev) {
        nextEdits[chat.chat_id] = {
          enabled: base.enabled,
          allow_embeddings: base.allow_embeddings,
          history_mode: base.history_mode,
          urls_mode: base.urls_mode,
          dirty: false,
          saving: false,
        };
        continue;
      }

      // If the row isn't being edited, keep it in sync with backend.
      if (!prev.dirty && !prev.saving) {
        nextEdits[chat.chat_id] = {
          enabled: base.enabled,
          allow_embeddings: base.allow_embeddings,
          history_mode: base.history_mode,
          urls_mode: base.urls_mode,
          dirty: false,
          saving: false,
        };
        continue;
      }

      const dirty =
        prev.enabled !== base.enabled ||
        prev.allow_embeddings !== base.allow_embeddings ||
        prev.history_mode !== base.history_mode ||
        prev.urls_mode !== base.urls_mode;
      nextEdits[chat.chat_id] = { ...prev, dirty };
    }

    for (const key of Object.keys(nextEdits)) {
      if (!nextChatsById[key]) {
        delete nextEdits[key];
      }
    }

    chatsById = nextChatsById;
    chatEdits = nextEdits;
  }

  function setChatEdit(chatId, patch) {
    const base = chatsById[chatId];
    if (!base) return;

    let prev = chatEdits[chatId];
    if (!prev) {
      prev = {
        enabled: base.enabled,
        allow_embeddings: base.allow_embeddings,
        history_mode: base.history_mode,
        urls_mode: base.urls_mode,
        dirty: false,
        saving: false,
      };
      chatEdits = { ...chatEdits, [chatId]: prev };
    }

    const next = { ...prev, ...patch };
    next.dirty =
      next.enabled !== base.enabled ||
      next.allow_embeddings !== base.allow_embeddings ||
      next.history_mode !== base.history_mode ||
      next.urls_mode !== base.urls_mode;
    chatEdits = { ...chatEdits, [chatId]: next };
  }

  function discardAllChatEdits() {
    const next = { ...chatEdits };
    for (const [chatId, edit] of Object.entries(next)) {
      const base = chatsById[chatId];
      if (!base) continue;
      next[chatId] = {
        enabled: base.enabled,
        allow_embeddings: base.allow_embeddings,
        history_mode: base.history_mode,
        urls_mode: base.urls_mode,
        dirty: false,
        saving: false,
      };
    }
    chatEdits = next;
  }

  async function applyAllChatPolicies() {
    const ids = dirtyChatIds.map((v) => Number(v)).filter((v) => Number.isFinite(v));
    if (ids.length === 0) return;

    applyAllBusy = true;
    applyAllDone = 0;
    applyAllTotal = ids.length;
    errorText = "";
    infoText = "";

    try {
      for (const chatId of ids) {
        const key = String(chatId);
        const edit = chatEdits[key];
        if (!edit || !edit.dirty) {
          applyAllDone++;
          continue;
        }
        chatEdits = { ...chatEdits, [key]: { ...edit, saving: true } };

        await backend().SetChatPolicy(chatId, edit.enabled, edit.history_mode, edit.allow_embeddings, edit.urls_mode);

        const after = chatEdits[key];
        if (after) {
          chatEdits = { ...chatEdits, [key]: { ...after, saving: false, dirty: false } };
        }
        applyAllDone++;
      }

      await refreshStatus();
      await refreshOnboardingStatus();
      await refreshChats();
      infoText = `Saved ${applyAllTotal} chat${applyAllTotal === 1 ? "" : "s"}`;
    } catch (error) {
      errorText = String(error);
      await refreshChats();
    } finally {
      applyAllBusy = false;
    }
  }

  function chatsForFolder(items, folder) {
    if (!folder) {
      return items || [];
    }
    if (folder.id === 0) {
      return items || [];
    }
    if (!Array.isArray(folder.chat_ids) || folder.chat_ids.length === 0) {
      return [];
    }
    const byId = new Map((items || []).map((c) => [c.chat_id, c]));
    return folder.chat_ids.map((id) => byId.get(id)).filter(Boolean);
  }

  function resolveTheme(preference) {
    if (preference === "light" || preference === "dark") return preference;
    if (typeof window === "undefined") return "light";
    return window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  function applyTheme(preference) {
    const theme = resolveTheme(preference);
    document.documentElement.dataset.theme = theme;
  }

  function escapeHtml(text) {
    return String(text)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function snippetToHtml(snippet) {
    const s = String(snippet ?? "");
    if (!s) return "";

    const open = "<mark>";
    const close = "</mark>";

    let out = "";
    let i = 0;
    while (i < s.length) {
      const j = s.indexOf(open, i);
      if (j < 0) {
        out += escapeHtml(s.slice(i));
        break;
      }
      out += escapeHtml(s.slice(i, j));
      const k = s.indexOf(close, j + open.length);
      if (k < 0) {
        // Treat unmatched <mark> as plain text.
        out += escapeHtml(s.slice(j));
        break;
      }
      out += "<mark>" + escapeHtml(s.slice(j + open.length, k)) + "</mark>";
      i = k + close.length;
    }
    return out;
  }

  function isPDFMime(mime) {
    const m = String(mime || "").toLowerCase();
    return m.includes("application/pdf") || m.includes("application/x-pdf");
  }

  function isPDFURLResult(result) {
    if (!result || result.source_type !== "url") return false;
    if (isPDFMime(result.url_mime)) return true;
    const u = String(result.url_final || result.url || "").toLowerCase();
    return u.includes(".pdf");
  }

  function saveThemePreference(preference) {
    themePreference = preference;
    try {
      localStorage.setItem(THEME_STORAGE_KEY, preference);
    } catch {
      // ignore
    }
    applyTheme(preference);
  }

  function searchLimitValue() {
    if (resultLimit === "top5") return 5;
    if (resultLimit === "all") return -1;
    return 10;
  }

  async function refreshStatus() {
    try {
      status = await backend().Status();
      mcpPort = Number(status?.mcp_port || 0);
      trayStatus = await backend().TrayStatus();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshChats() {
    try {
      chats = await backend().ListChats();
      syncChatEditsFromChats(chats);
      await refreshChatFolders();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshChatFolders() {
    try {
      chatFolders = await backend().TelegramChatFolders();
      if (!Array.isArray(chatFolders) || chatFolders.length === 0) {
        throw new Error("no folders");
      }
      if (!chatFolders.some((f) => f.id === activeChatFolderId)) {
        activeChatFolderId = chatFolders.some((f) => f.id === SELECTED_FOLDER_ID) ? SELECTED_FOLDER_ID : chatFolders[0].id;
      }
    } catch (error) {
      // Best-effort: ensure at least "All" + "Выбранные" tabs exist even if Telegram folder API isn't available.
      const allIDs = (chats || []).map((c) => c.chat_id);
      const selectedIDs = (chats || []).filter((c) => c.enabled).map((c) => c.chat_id);
      chatFolders = [
        { id: 0, title: "All", chat_ids: allIDs },
        { id: SELECTED_FOLDER_ID, title: "Выбранные", emoticon: "⭐", chat_ids: selectedIDs },
      ];
      if (!chatFolders.some((f) => f.id === activeChatFolderId)) {
        activeChatFolderId = chatFolders.some((f) => f.id === SELECTED_FOLDER_ID) ? SELECTED_FOLDER_ID : 0;
      }
    }
  }

  async function refreshTelegramStatus() {
    try {
      telegramStatus = await backend().TelegramAuthStatus();
      if (!telegramEditMode) {
        tgPhone = telegramStatus?.phone || "";
        tgCode = "";
        tgPassword = "";
      }
      if (!telegramStatus?.configured && !telegramStatus?.authorized && !telegramEditMode) {
        telegramEditMode = true;
      }
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshOnboardingStatus() {
    try {
      onboarding = await backend().OnboardingStatus();
    } catch (error) {
      errorText = String(error);
    }
  }

  function openPage(page) {
    currentPage = page;
    if (page === "chats") {
      if (chatFolders.some((f) => f.id === SELECTED_FOLDER_ID)) {
        activeChatFolderId = SELECTED_FOLDER_ID;
      }
    }
    if (page === "settings") {
      refreshEmbeddingsProgress();
      startEmbeddingsProgressPolling();
    } else {
      stopEmbeddingsProgressPolling();
    }
  }

  function startStorageEdit() {
    storageEditMode = true;
    errorText = "";
    infoText = "";
  }

  async function cancelStorageEdit() {
    storageEditMode = false;
    await refreshDataDir();
  }

  async function applyStorageEdit() {
    const ok = await applyDataDir();
    if (ok) {
      storageEditMode = false;
    }
  }

  function startTelegramEdit() {
    telegramEditMode = true;
    if (!tgPhone && telegramStatus?.phone) {
      tgPhone = telegramStatus.phone;
    }
    errorText = "";
    infoText = "";
  }

  async function cancelTelegramEdit() {
    telegramEditMode = false;
    tgCode = "";
    tgPassword = "";
    await refreshTelegramStatus();
  }

  async function applyTelegramEdit() {
    if (!String(tgPhone || "").trim()) {
      errorText = "Phone is required";
      return;
    }
    if (String(tgCode || "").trim()) {
      const ok = await signInTelegram();
      if (ok) {
        telegramEditMode = false;
        tgCode = "";
        tgPassword = "";
      }
      return;
    }
    const sent = await requestTelegramCode();
    if (sent) {
      infoText = "Code sent. Enter login code and click Apply again.";
    }
  }

  function startEmbeddingsEdit() {
    embeddingsEditMode = true;
    embAPIKey = "";
    embTest = null;
    errorText = "";
    infoText = "";
  }

  async function cancelEmbeddingsEdit() {
    embeddingsEditMode = false;
    embAPIKey = "";
    embTest = null;
    await refreshEmbeddingsConfig();
  }

  async function applyEmbeddingsEdit() {
    const ok = await saveEmbeddingsConfig();
    if (ok) {
      embeddingsEditMode = false;
    }
  }

  async function refreshEmbeddingsConfig() {
    try {
      const cfg = await backend().EmbeddingsConfig();
      if (!embeddingsEditMode) {
        embBaseURL = cfg.base_url || "https://api.openai.com/v1";
        embModel = cfg.model || "text-embedding-3-large";
        embDims = Number(cfg.dimensions || 3072);
      }
      embConfigured = Boolean(cfg.configured);
      if (!embeddingsEditMode) {
        embAPIKey = "";
      }
      if (!embConfigured && !embeddingsEditMode) {
        embeddingsEditMode = true;
      }
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshSemanticStrictness() {
    if (!backend()?.SemanticStrictness) {
      return;
    }
    try {
      semanticStrictness = (await backend().SemanticStrictness()) || "similar";
    } catch {
      // ignore
    }
  }

  async function refreshSearchUISettings() {
    if (!backend()?.SearchUISettings) {
      return;
    }
    try {
      const cfg = await backend().SearchUISettings();
      if (cfg?.mode) mode = cfg.mode;
      if (cfg?.result_limit) resultLimit = cfg.result_limit;
      if (typeof cfg?.advanced === "boolean") advanced = cfg.advanced;
    } catch {
      // ignore
    }
  }

  async function saveSearchUISettings() {
    if (!backend()?.SetSearchUISettings) {
      return;
    }
    try {
      const cfg = await backend().SetSearchUISettings(mode, resultLimit, advanced);
      if (cfg?.mode) mode = cfg.mode;
      if (cfg?.result_limit) resultLimit = cfg.result_limit;
      if (typeof cfg?.advanced === "boolean") advanced = cfg.advanced;
    } catch {
      // ignore
    }
  }

  async function saveSemanticStrictness(next) {
    if (!backend()?.SetSemanticStrictness) {
      return;
    }
    semanticStrictnessBusy = true;
    try {
      await backend().SetSemanticStrictness(next);
      semanticStrictness = next;
    } catch (error) {
      errorText = String(error);
      await refreshSemanticStrictness();
    } finally {
      semanticStrictnessBusy = false;
    }
  }

  async function refreshEmbeddingsProgress(options = {}) {
    if (!backend()?.EmbeddingsProgress) {
      return;
    }
    const silent = Boolean(options?.silent);
    if (!silent) {
      embProgressBusy = true;
    }
    try {
      embProgress = await backend().EmbeddingsProgress();
    } catch {
      // ignore (progress is best-effort)
    } finally {
      if (!silent) {
        embProgressBusy = false;
      }
    }
  }

  function embeddingsProgressDone(p) {
    if (!p) return true;
    if (p.queue_pending > 0 || p.queue_running > 0) return false;
    if (p.total_eligible > 0 && p.embedded < p.total_eligible) return false;
    return true;
  }

  function startEmbeddingsProgressPolling() {
    if (embProgressTimer) return;
    embProgressTimer = setInterval(async () => {
      if (currentPage !== "settings") {
        stopEmbeddingsProgressPolling();
        return;
      }
      await refreshEmbeddingsProgress({ silent: true });
      if (embeddingsProgressDone(embProgress)) {
        stopEmbeddingsProgressPolling();
      }
    }, 2000);
  }

  function stopEmbeddingsProgressPolling() {
    if (!embProgressTimer) return;
    clearInterval(embProgressTimer);
    embProgressTimer = null;
  }

  async function refreshLiveStatuses() {
    if (statusPollBusy) return;
    statusPollBusy = true;
    statusPollTick++;
    try {
      await refreshStatus();
      if (currentPage === "settings") {
        if (statusPollTick % 2 === 0) {
          await refreshBackgroundPaused();
          await refreshAutostart();
        }
        if (!embeddingsEditMode && !maintenanceBusy && statusPollTick % 4 === 0) {
          await refreshEmbeddingsConfig();
        }
        if (!maintenanceBusy && statusPollTick % 4 === 0) {
          await refreshEmbeddingsProgress({ silent: true });
        }
      }
    } finally {
      statusPollBusy = false;
    }
  }

  function startStatusPolling() {
    if (statusPollTimer) return;
    statusPollTimer = setInterval(async () => {
      await refreshLiveStatuses();
    }, 3000);
  }

  function stopStatusPolling() {
    if (!statusPollTimer) return;
    clearInterval(statusPollTimer);
    statusPollTimer = null;
  }

  async function refreshAutostart() {
    try {
      autostartEnabled = await backend().AutostartEnabled();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshBackgroundPaused() {
    try {
      backgroundPaused = await backend().BackgroundPaused();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshDataDir() {
    try {
      const next = await backend().DataDir();
      if (!storageEditMode) {
        dataDirPath = next;
      }
      if (!String(next || "").trim() && !storageEditMode) {
        storageEditMode = true;
      }
    } catch (error) {
      errorText = String(error);
    }
  }

  async function requestTelegramCode() {
    telegramBusy = true;
    errorText = "";
    let ok = false;
    try {
      telegramStatus = await backend().TelegramRequestCode(tgPhone);
      ok = true;
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
    return ok;
  }

  async function signInTelegram() {
    telegramBusy = true;
    errorText = "";
    let ok = false;
    try {
      telegramStatus = await backend().TelegramSignIn(tgCode, tgPassword);
      await refreshTelegramStatus();
      await refreshChats();
      await refreshOnboardingStatus();
      ok = true;
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
    return ok;
  }

  async function loadTelegramChats() {
    telegramBusy = true;
    errorText = "";
    let ok = false;
    try {
      chats = await backend().TelegramLoadChats();
      syncChatEditsFromChats(chats);
      await refreshChatFolders();
      await refreshStatus();
      await refreshOnboardingStatus();
      ok = true;
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
    return ok;
  }

  async function runSearch() {
    if (searchLocked) {
      errorText = "Enable at least one chat (Chats) to search";
      return;
    }
    if (!query.trim()) {
      return;
    }
    loading = true;
    errorText = "";
    try {
      results = await backend().Search(
        query,
        mode,
        advanced,
        selectedChatIds(),
        0,
        0,
        searchLimitValue()
      );
    } catch (error) {
      errorText = String(error);
    } finally {
      loading = false;
    }
  }

  async function runSyncNow() {
    syncBusy = true;
    errorText = "";
    try {
      status = await backend().SyncNow();
      await refreshChats();
    } catch (error) {
      errorText = String(error);
    } finally {
      syncBusy = false;
    }
  }

  async function toggleBackgroundPause() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    const shouldResume = backgroundPaused;
    try {
      if (shouldResume) {
        status = await backend().ResumeBackground();
      } else {
        status = await backend().PauseBackground();
      }
      await refreshBackgroundPaused();
      infoText = shouldResume ? "Background workers resumed" : "Background workers paused";
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function toggleMCPEnabled() {
    if (!status) {
      return;
    }
    const wasEnabled = Boolean(status.mcp_enabled);
    mcpBusy = true;
    errorText = "";
    infoText = "";
    try {
      await backend().ToggleMCP(!wasEnabled);
      await refreshStatus();
      infoText = wasEnabled ? "MCP disabled" : "MCP enabled";
    } catch (error) {
      errorText = String(error);
      await refreshStatus();
    } finally {
      mcpBusy = false;
    }
  }

  async function saveMCPPort() {
    mcpBusy = true;
    errorText = "";
    infoText = "";
    try {
      const parsed = Number(mcpPort);
      if (!Number.isInteger(parsed) || parsed < 0 || parsed > 65535) {
        throw new Error("MCP port must be an integer in range 0..65535");
      }
      status = await backend().SetMCPPort(parsed);
      await refreshStatus();
      infoText = `MCP port saved: ${parsed}`;
    } catch (error) {
      errorText = String(error);
      await refreshStatus();
    } finally {
      mcpBusy = false;
    }
  }

  async function copyMCPEndpoint() {
    if (!status || !status.mcp_endpoint) {
      return;
    }
    errorText = "";
    infoText = "";
    try {
      if (!navigator?.clipboard?.writeText) {
        throw new Error("Clipboard API is unavailable");
      }
      await navigator.clipboard.writeText(status.mcp_endpoint);
      infoText = "MCP endpoint copied";
    } catch (error) {
      errorText = String(error);
    }
  }

  async function browseDataDir() {
    dataDirBusy = true;
    errorText = "";
    try {
      const selected = await backend().BrowseDataDir();
      if (selected && String(selected).trim()) {
        dataDirPath = selected;
      }
    } catch (error) {
      errorText = String(error);
    } finally {
      dataDirBusy = false;
    }
  }

  async function applyDataDir() {
    dataDirBusy = true;
    errorText = "";
    infoText = "";
    let ok = false;
    try {
      infoText = await backend().SetDataDir(dataDirPath);
      await refreshDataDir();
      ok = true;
    } catch (error) {
      errorText = String(error);
    } finally {
      dataDirBusy = false;
    }
    return ok;
  }

  function selectedChatIds() {
    return chats.filter((chat) => chat.enabled).map((chat) => chat.chat_id);
  }

  async function completeOnboarding() {
    onboardingBusy = true;
    errorText = "";
    infoText = "";
    try {
      onboarding = await backend().CompleteOnboarding();
      infoText = "Onboarding completed";
      await refreshStatus();
      currentPage = "search";
    } catch (error) {
      errorText = String(error);
    } finally {
      onboardingBusy = false;
    }
  }

  async function openInTelegram(result) {
    try {
      await backend().OpenInTelegram(result.chat_id, result.msg_id, result.deep_link || "");
    } catch (error) {
      errorText = String(error);
    }
  }

  async function copyResultReference(result) {
    const ref = (result?.deep_link || "").trim();
    if (!ref) {
      return;
    }
    errorText = "";
    infoText = "";
    try {
      if (!navigator?.clipboard?.writeText) {
        throw new Error("Clipboard API is unavailable");
      }
      await navigator.clipboard.writeText(ref);
      infoText = "Reference copied";
    } catch (error) {
      errorText = String(error);
    }
  }

  async function createBackup() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      const path = await backend().CreateBackup(backupPath);
      backupPath = path;
      infoText = `Backup created: ${path}`;
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function restoreBackup() {
    if (!restorePath.trim()) {
      errorText = "Set backup .zip path first";
      return;
    }
    if (!confirm("Restore backup now? Current in-memory state will be replaced.")) {
      return;
    }
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      infoText = await backend().RestoreBackup(restorePath);
      results = [];
      await refreshStatus();
      await refreshTelegramStatus();
      await refreshChats();
      await refreshOnboardingStatus();
      await refreshEmbeddingsConfig();
      await refreshEmbeddingsProgress();
      await refreshAutostart();
      await refreshBackgroundPaused();
      await refreshDataDir();
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function purgeChatData(chat) {
    if (!confirm(`Purge indexed data for chat "${chat.title}"?`)) {
      return;
    }
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      status = await backend().PurgeChat(chat.chat_id);
      results = [];
      infoText = `Purged data for chat: ${chat.title}`;
      await refreshChats();
      await refreshOnboardingStatus();
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function purgeAllData() {
    if (!confirm("Purge all indexed messages, URL docs, tasks, and vector cache?")) {
      return;
    }
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      status = await backend().PurgeAll();
      results = [];
      infoText = "All indexed data purged";
      await refreshChats();
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function saveEmbeddingsConfig() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    let ok = false;
    try {
      const dims = Number(embDims);
      await backend().SetEmbeddingsConfig(embBaseURL, embModel, embAPIKey, dims);
      await refreshEmbeddingsConfig();
      infoText = "Embeddings config saved";
      ok = true;
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
    return ok;
  }

  async function testEmbeddings() {
    embTestBusy = true;
    errorText = "";
    infoText = "";
    try {
      embTest = await backend().TestEmbeddings();
      if (embTest && embTest.ok) {
        infoText = `Embeddings OK (${embTest.vector_len} dims, ${embTest.took_ms} ms)`;
      } else if (embTest) {
        errorText = `Embeddings test failed: ${embTest.error || "unknown error"}`;
      }
    } catch (error) {
      errorText = String(error);
    } finally {
      embTestBusy = false;
    }
  }

  async function rebuildSemanticIndex() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      infoText = await backend().RebuildSemanticIndex();
      await refreshStatus();
      await refreshEmbeddingsProgress();
      startEmbeddingsProgressPolling();
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function toggleAutostart() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      autostartEnabled = await backend().SetAutostartEnabled(!autostartEnabled);
      infoText = autostartEnabled ? "Autostart enabled" : "Autostart disabled";
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  refreshStatus();
  refreshTelegramStatus();
  refreshOnboardingStatus();
  refreshEmbeddingsConfig();
  refreshSemanticStrictness();
  refreshSearchUISettings();
  refreshAutostart();
  refreshBackgroundPaused();
  refreshDataDir();
  refreshChats();
  refreshChatFolders();

  onMount(() => {
    try {
      const stored = localStorage.getItem(THEME_STORAGE_KEY);
      if (stored === "light" || stored === "dark" || stored === "system") {
        themePreference = stored;
      }
    } catch {
      // ignore
    }

    applyTheme(themePreference);
    refreshLiveStatuses();
    startStatusPolling();

    let cleanupThemeListener = null;
    if (window.matchMedia) {
      themeMediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
      const handler = () => themePreference === "system" && applyTheme("system");
      if (themeMediaQuery.addEventListener) themeMediaQuery.addEventListener("change", handler);
      else themeMediaQuery.addListener(handler);

      cleanupThemeListener = () => {
        if (!themeMediaQuery) return;
        if (themeMediaQuery.removeEventListener) themeMediaQuery.removeEventListener("change", handler);
        else themeMediaQuery.removeListener(handler);
      };
    }

    return () => {
      stopStatusPolling();
      if (cleanupThemeListener) cleanupThemeListener();
    };
  });
</script>

<main class="layout">
  <section class="hero">
    <div class="heroHeader">
      <div>
        <h1>Telegram Sidecar Search</h1>
        <p>Local-first FTS + Hybrid search with read-only MCP endpoint.</p>
      </div>
      <div class="heroControls">
        <label>
          Theme
          <select bind:value={themePreference} on:change={(e) => saveThemePreference(e.currentTarget.value)}>
            <option value="system">System</option>
            <option value="light">Light</option>
            <option value="dark">Dark</option>
          </select>
        </label>
      </div>
    </div>
    <div class="row wrap navRow">
      <button class:active={currentPage === "search"} on:click={() => openPage("search")}>Search</button>
      <button class:active={currentPage === "chats"} on:click={() => openPage("chats")}>Chats</button>
      <button class:active={currentPage === "settings"} on:click={() => openPage("settings")}>Settings</button>
    </div>
    {#if searchLocked}
      <p class="mutedLine">Search is locked until at least one chat is enabled (Chats).</p>
    {/if}
  </section>

  {#if errorText}
    <section class="panel">
      <div class="error">{errorText}</div>
    </section>
  {/if}
  {#if infoText}
    <section class="panel">
      <div class="info">{infoText}</div>
    </section>
  {/if}

  {#if currentPage === "search"}
    <section class="panel">
      <div class="row">
        <input
          bind:value={query}
          class="searchInput"
          on:keydown={(event) => event.key === "Enter" && runSearch()}
          placeholder="Search messages..."
        />
        <select bind:value={mode} on:change={saveSearchUISettings}>
          <option value="hybrid">Hybrid</option>
          <option value="fts">FTS</option>
        </select>
        <select bind:value={resultLimit} title="How many results to show" on:change={saveSearchUISettings}>
          <option value="top5">Top 5</option>
          <option value="top10">Top 10</option>
          <option value="all">All</option>
        </select>
        {#if mode === "hybrid"}
          <div class="semanticToggle" title="Насколько строго показывать семантические совпадения. Строже = меньше, но релевантнее.">
            <span class="fieldLabel">Semantic</span>
            <div class="pills" aria-label="Semantic strictness">
              <label
                class:active={semanticStrictness === "very"}
                class:disabled={!embConfigured || semanticStrictnessBusy}
                title="Очень похоже (самое строгое)"
              >
                <input
                  type="radio"
                  name="semantic-strictness"
                  value="very"
                  checked={semanticStrictness === "very"}
                  disabled={!embConfigured || semanticStrictnessBusy}
                  on:change={() => saveSemanticStrictness("very")}
                />
                Очень
              </label>
              <label
                class:active={semanticStrictness === "similar"}
                class:disabled={!embConfigured || semanticStrictnessBusy}
                title="Похоже (по умолчанию)"
              >
                <input
                  type="radio"
                  name="semantic-strictness"
                  value="similar"
                  checked={semanticStrictness === "similar"}
                  disabled={!embConfigured || semanticStrictnessBusy}
                  on:change={() => saveSemanticStrictness("similar")}
                />
                Похоже
              </label>
              <label
                class:active={semanticStrictness === "weak"}
                class:disabled={!embConfigured || semanticStrictnessBusy}
                title="Слабо похоже (мягче, больше результатов)"
              >
                <input
                  type="radio"
                  name="semantic-strictness"
                  value="weak"
                  checked={semanticStrictness === "weak"}
                  disabled={!embConfigured || semanticStrictnessBusy}
                  on:change={() => saveSemanticStrictness("weak")}
                />
                Слабо
              </label>
            </div>
          </div>
        {/if}
        <label class="toggle">
          <input bind:checked={advanced} type="checkbox" on:change={saveSearchUISettings} />
          Advanced
        </label>
        <button on:click={runSearch} disabled={loading || searchLocked || !query.trim()}>
          {loading ? "Searching..." : "Search"}
        </button>
      </div>
      {#if status}
        <div class="status">
          <span>Messages: {status.message_count}</span>
          <span>Sync: {status.sync_state || "idle"}</span>
          <span>Backfill: {status.backfill_progress}%</span>
        </div>
      {/if}
    </section>

    <section class="panel">
      <h2>Results</h2>
      {#if mode === "hybrid"}
        <p class="mutedLine">
          FTS matches: {ftsResultCount} · Semantic matches: {semanticResultCount}
          {!embConfigured ? " (embeddings not configured)" : ""}
        </p>
      {:else if mode === "fts"}
        <p class="mutedLine">FTS matches: {results.length}</p>
      {/if}
      {#if results.length === 0}
        <div class="empty">No results yet. Run a search.</div>
      {/if}
      {#each results as result}
        <div class="resultCard">
          <div class="resultHead">
            <div class="resultTitle">
              <strong>{result.chat_title}</strong>
              {#if result.source_type === "file"}
                <span class="badge pdf">PDF</span>
                {#if result.file_name}
                  <span class="fileName" title={result.file_name}>{result.file_name}</span>
                {/if}
              {:else if isPDFURLResult(result)}
                <span class="badge pdf">PDF</span>
                {#if result.url_title}
                  <span class="fileName" title={result.url_title}>{result.url_title}</span>
                {/if}
              {/if}
              {#if result.match_semantic}
                <span class="badge semantic">Semantic</span>
              {/if}
            </div>
            <small>{new Date(result.timestamp * 1000).toLocaleString()}</small>
          </div>
          {#if result.source_type === "file"}
            <div class="status">
              <span>Matched in PDF</span>
              <span>{result.file_name || "PDF attachment"}</span>
            </div>
          {:else if result.source_type === "url"}
            <div class="status">
              <span>Matched in URL page</span>
              <span>{result.url_title || "Untitled page"}</span>
              {#if result.url_final || result.url}
                <span>{result.url_final || result.url}</span>
              {/if}
            </div>
          {/if}
          <div class="sender">{result.sender}</div>
          <div
            class="snippet"
            class:snippetClamp={result.match_semantic && (result.source_type === "url" || result.source_type === "file") && !!result.extracted_snippet}
          >
            {@html snippetToHtml(result.snippet || result.message_text)}
          </div>
          {#if result.source_type === "url"}
            <div class="sender">Source message: {result.message_text}</div>
          {/if}
          <div class="row">
            <button on:click={() => openInTelegram(result)}>Open in Telegram</button>
            {#if result.deep_link}
              <button on:click={() => copyResultReference(result)}>Copy link/reference</button>
            {/if}
          </div>
        </div>
      {/each}
    </section>
  {/if}

  {#if currentPage === "settings"}
    <section class="panel">
      <h2>Settings</h2>
      <div class="folderTabs">
        <button class:active={settingsTab === "runtime"} on:click={() => (settingsTab = "runtime")}>Runtime</button>
        <button class:active={settingsTab === "mcp"} on:click={() => (settingsTab = "mcp")}>MCP</button>
        <button class:active={settingsTab === "storage"} on:click={() => (settingsTab = "storage")}>Storage</button>
        <button class:active={settingsTab === "telegram"} on:click={() => (settingsTab = "telegram")}>Telegram</button>
        <button class:active={settingsTab === "embeddings"} on:click={() => (settingsTab = "embeddings")}>Embeddings</button>
        <button class:active={settingsTab === "backup"} on:click={() => (settingsTab = "backup")}>Backup</button>
      </div>
    </section>

    {#if settingsTab === "runtime"}
    <section class="panel">
      <h2>Runtime</h2>
      {#if status}
        <div class="status">
          <span>Tray: {trayStatus}</span>
          <span>Sync: {status.sync_state || "idle"}</span>
          <span>Backfill: {status.backfill_progress}%</span>
          <span>Messages: {status.message_count}</span>
          <span>Autostart: {autostartEnabled ? "enabled" : "disabled"}</span>
        </div>
      {/if}
      <div class="row">
        <button on:click={runSyncNow} disabled={syncBusy}>
          {syncBusy ? "Syncing..." : "Sync now"}
        </button>
        <button on:click={toggleBackgroundPause} disabled={maintenanceBusy}>
          {maintenanceBusy ? "Working..." : (backgroundPaused ? "Resume background" : "Pause background")}
        </button>
        <button on:click={toggleAutostart} disabled={maintenanceBusy}>
          {maintenanceBusy ? "Working..." : (autostartEnabled ? "Disable autostart" : "Enable autostart")}
        </button>
      </div>
    </section>
    {/if}

    {#if settingsTab === "mcp"}
    <section class="panel">
      <h2>MCP</h2>
      {#if status}
        <div class="status">
          <span>MCP: {status.mcp_enabled ? "ON" : "OFF"}</span>
          <span>MCP status: {status.mcp_status || "unknown"}</span>
          <span>MCP port: {status.mcp_port || 0}</span>
          <span>Endpoint: {status.mcp_endpoint || "n/a"}</span>
        </div>
      {/if}
      <div class="row wrap">
        <input bind:value={mcpPort} type="number" min="0" max="65535" placeholder="MCP port (0 = random free)" />
        <button on:click={saveMCPPort} disabled={mcpBusy || maintenanceBusy}>
          {mcpBusy ? "Working..." : "Save MCP port"}
        </button>
        <button on:click={toggleMCPEnabled} disabled={mcpBusy || maintenanceBusy}>
          {mcpBusy ? "Working..." : (status && status.mcp_enabled ? "Disable MCP" : "Enable MCP")}
        </button>
        <button on:click={copyMCPEndpoint} disabled={!status || !status.mcp_endpoint}>
          Copy endpoint
        </button>
      </div>
    </section>
    {/if}

    {#if settingsTab === "storage"}
    <section class="panel">
      <h2>Storage</h2>
      <div class="row wrap">
        <input bind:value={dataDirPath} class="pathInput" placeholder="Data directory path" disabled={!storageEditMode} />
        {#if storageEditMode}
          <button on:click={browseDataDir} disabled={dataDirBusy || onboardingBusy}>
            {dataDirBusy ? "Working..." : "Browse data dir"}
          </button>
          <button on:click={applyStorageEdit} disabled={dataDirBusy || onboardingBusy || !dataDirPath.trim()}>
            {dataDirBusy ? "Working..." : "Apply"}
          </button>
          <button on:click={cancelStorageEdit} disabled={dataDirBusy || onboardingBusy}>Cancel</button>
        {:else}
          <button on:click={startStorageEdit} disabled={dataDirBusy || onboardingBusy}>Change</button>
        {/if}
      </div>
      <p class="mutedLine">If path changes, restart app to apply.</p>
    </section>
    {/if}

    {#if settingsTab === "telegram"}
    <section class="panel">
      <h2>Telegram Account</h2>
      <div class="tgGrid">
        <input bind:value={tgPhone} placeholder="Phone (+123...)" disabled={!telegramEditMode} />
        <input bind:value={tgCode} placeholder="Login code" disabled={!telegramEditMode} />
        <input bind:value={tgPassword} placeholder="2FA password (optional)" type="password" disabled={!telegramEditMode} />
      </div>
      <div class="row">
        {#if telegramEditMode}
          <button on:click={requestTelegramCode} disabled={telegramBusy || !tgPhone.trim()}>
            {telegramBusy ? "Working..." : "Send code"}
          </button>
          <button on:click={applyTelegramEdit} disabled={telegramBusy || !tgPhone.trim()}>
            {telegramBusy ? "Working..." : "Apply"}
          </button>
          <button on:click={cancelTelegramEdit} disabled={telegramBusy}>Cancel</button>
        {:else}
          <button on:click={startTelegramEdit} disabled={telegramBusy}>Change</button>
        {/if}
        <button on:click={loadTelegramChats} disabled={telegramBusy || (!telegramEditMode && !telegramConfigured)}>
          {telegramBusy ? "Working..." : "Load chats"}
        </button>
      </div>
      {#if telegramStatus}
        <div class="status">
          <span>Configured: {telegramStatus.configured ? "yes" : "no"}</span>
          <span>Authorized: {telegramStatus.authorized ? "yes" : "no"}</span>
          <span>Code pending: {telegramStatus.awaiting_code ? "yes" : "no"}</span>
          <span>Phone: {telegramStatus.phone || "n/a"}</span>
          <span>User: {telegramStatus.user_display || "n/a"}</span>
        </div>
      {/if}
    </section>
    {/if}

    {#if settingsTab === "embeddings"}
    <section class="panel">
      <h2>Embeddings</h2>
      <div class="status">
        <span>Embeddings: {embConfigured ? "configured" : "not configured"}</span>
      </div>
      <div class="row wrap">
        <input
          bind:value={embBaseURL}
          class="pathInput"
          placeholder="Embeddings base URL (OpenAI-compatible)"
          disabled={!embeddingsEditMode}
        />
        <input bind:value={embModel} placeholder="Model" disabled={!embeddingsEditMode} />
        <input bind:value={embDims} type="number" min="1" max="8192" placeholder="Dims" disabled={!embeddingsEditMode} />
        <input
          bind:value={embAPIKey}
          type="password"
          class="pathInput"
          placeholder={embeddingsEditMode ? "Embeddings API key (leave empty to keep current)" : (embConfigured ? "Configured API key" : "Embeddings API key")}
          disabled={!embeddingsEditMode}
        />
        {#if embeddingsEditMode}
          <button on:click={testEmbeddings} disabled={embTestBusy || maintenanceBusy}>
            {embTestBusy ? "Testing..." : "Test embeddings"}
          </button>
          <button on:click={applyEmbeddingsEdit} disabled={maintenanceBusy}>
            {maintenanceBusy ? "Working..." : "Apply"}
          </button>
          <button on:click={cancelEmbeddingsEdit} disabled={maintenanceBusy}>Cancel</button>
        {:else}
          <button on:click={startEmbeddingsEdit} disabled={maintenanceBusy}>Change</button>
        {/if}
        <button on:click={rebuildSemanticIndex} disabled={maintenanceBusy || !embConfigured}>
          {maintenanceBusy ? "Working..." : "Rebuild semantic index"}
        </button>
      </div>
      {#if embProgress}
        <div class="progressPanel">
          <div class="progressHead">
            <div class="mutedLine">
              Semantic index: {embProgress.embedded}/{embProgress.total_eligible} ({embProgressPercent}%)
              {embProgress.queue_running > 0 ? ` · running ${embProgress.queue_running}` : ""}
              {embProgress.queue_pending > 0 ? ` · queued ${embProgress.queue_pending}` : ""}
              {embProgress.queue_failed > 0 ? ` · failed ${embProgress.queue_failed}` : ""}
            </div>
            <button class="small" on:click={refreshEmbeddingsProgress} disabled={embProgressBusy || maintenanceBusy}>
              {embProgressBusy ? "Refreshing..." : "Refresh"}
            </button>
          </div>
          <progress max="100" value={embProgressPercent}></progress>
        </div>
      {/if}
    </section>
    {/if}

    {#if settingsTab === "backup"}
      <section class="panel">
        <h2>Backup</h2>
        <div class="row wrap">
          <input bind:value={backupPath} class="pathInput" placeholder="Backup path (.zip) or folder (optional)" />
          <button on:click={createBackup} disabled={maintenanceBusy}>
            {maintenanceBusy ? "Working..." : "Create backup"}
          </button>
        </div>
        <div class="row wrap">
          <input bind:value={restorePath} class="pathInput" placeholder="Restore from backup .zip path" />
          <button on:click={restoreBackup} disabled={maintenanceBusy}>
            {maintenanceBusy ? "Working..." : "Restore backup"}
          </button>
          <button class="danger" on:click={purgeAllData} disabled={maintenanceBusy}>
            {maintenanceBusy ? "Working..." : "Purge all data"}
          </button>
        </div>
      </section>
    {/if}
  {/if}

  {#if currentPage === "chats"}
    <section class="panel">
      <h2>Chats</h2>
      {#if chatFolders.length > 0}
        <div class="folderTabs">
          {#each chatFolders as folder (folder.id)}
            <button class:active={activeChatFolderId === folder.id} on:click={() => (activeChatFolderId = folder.id)}>
              {(folder.emoticon ? folder.emoticon + " " : "") + folder.title}
            </button>
          {/each}
        </div>
      {/if}

      <div class="chatToolbar">
        <div class="mutedLine">Unsaved changes: {dirtyCount}</div>
        <div class="row wrap">
          <input
            class="chatFilterInput"
            type="search"
            bind:value={chatFilter}
            placeholder="Filter chats by name..."
            title="Type to filter chats in this folder by chat name."
          />
          <button class="small" on:click={discardAllChatEdits} disabled={dirtyCount === 0 || applyAllBusy}>
            Discard
          </button>
          <button class="small primary" on:click={applyAllChatPolicies} disabled={dirtyCount === 0 || applyAllBusy}>
            {applyAllBusy ? `Applying ${applyAllDone}/${applyAllTotal}...` : "Apply changes"}
          </button>
        </div>
      </div>
      <p class="mutedLine">
        Changes are staged and applied only after clicking <strong>Apply changes</strong>. Hover controls for details.
      </p>

      {#if visibleChats.length === 0}
        <div class="empty">
          {(chatFilter || "").trim() ? "No chats match the filter in this folder." : "No chats in this folder."}
        </div>
      {/if}

      <div class="chatList">
        {#each visibleChats as chat (chat.chat_id)}
          <div class="chatRow" class:dirtyRow={chatEdits[chat.chat_id]?.dirty}>
            <div class="chatMeta">
              <strong>{chat.title}{chatEdits[chat.chat_id]?.dirty ? " *" : ""}</strong>
              <small>{chat.type}</small>
            </div>

            <label class="switchWrap" title="Include this chat in syncing and search indexing.">
              <span class="fieldLabel">Enabled</span>
              <span class="switch">
                <input
                  type="checkbox"
                  checked={Boolean(chatEdits[chat.chat_id]?.enabled ?? chat.enabled)}
                  disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                  on:change={(e) => setChatEdit(chat.chat_id, { enabled: e.currentTarget.checked })}
                />
                <span class="slider"></span>
              </span>
            </label>

            <label class="switchWrap" title="Allow semantic embeddings for this chat (only used if embeddings are configured).">
              <span class="fieldLabel">Embeddings</span>
              <span class="switch">
                <input
                  type="checkbox"
                  checked={Boolean(chatEdits[chat.chat_id]?.allow_embeddings ?? chat.allow_embeddings)}
                  disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                  on:change={(e) => setChatEdit(chat.chat_id, { allow_embeddings: e.currentTarget.checked })}
                />
                <span class="slider"></span>
              </span>
            </label>

            <div class="enumField">
              <div class="enumMeta">
                <div class="labelRow">
                  <span class="fieldLabel">History</span>
                  <span
                    class="helpIcon"
                    title="Backfill indexes older messages (more complete, may take longer). New only indexes just new incoming messages."
                    >?</span
                  >
                </div>
              </div>
              <div class="pills">
                <label
                  class:active={(chatEdits[chat.chat_id]?.history_mode ?? chat.history_mode) === "full"}
                  title="Backfill: index older messages (more complete, may take longer)."
                >
                  <input
                    type="radio"
                    name={`hist-${chat.chat_id}`}
                    value="full"
                    checked={(chatEdits[chat.chat_id]?.history_mode ?? chat.history_mode) === "full"}
                    disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                    on:change={() => setChatEdit(chat.chat_id, { history_mode: "full" })}
                  />
                  Backfill
                </label>
                <label
                  class:active={(chatEdits[chat.chat_id]?.history_mode ?? chat.history_mode) === "lazy"}
                  title="New only: index only new incoming messages."
                >
                  <input
                    type="radio"
                    name={`hist-${chat.chat_id}`}
                    value="lazy"
                    checked={(chatEdits[chat.chat_id]?.history_mode ?? chat.history_mode) === "lazy"}
                    disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                    on:change={() => setChatEdit(chat.chat_id, { history_mode: "lazy" })}
                  />
                  New only
                </label>
              </div>
            </div>

            <div class="enumField">
              <div class="enumMeta">
                <div class="labelRow">
                  <span class="fieldLabel">URLs</span>
                  <span
                    class="helpIcon"
                    title="Off disables URL indexing. Low indexes URLs with lower priority. High indexes more aggressively."
                    >?</span
                  >
                </div>
              </div>
              <div class="pills">
                <label class:active={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "off"} title="Off: do not index URLs.">
                  <input
                    type="radio"
                    name={`urls-${chat.chat_id}`}
                    value="off"
                    checked={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "off"}
                    disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                    on:change={() => setChatEdit(chat.chat_id, { urls_mode: "off" })}
                  />
                  Off
                </label>
                <label
                  class:active={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "lazy"}
                  title="Low: index URLs with lower priority."
                >
                  <input
                    type="radio"
                    name={`urls-${chat.chat_id}`}
                    value="lazy"
                    checked={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "lazy"}
                    disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                    on:change={() => setChatEdit(chat.chat_id, { urls_mode: "lazy" })}
                  />
                  Low
                </label>
                <label
                  class:active={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "full"}
                  title="High: index URLs more aggressively."
                >
                  <input
                    type="radio"
                    name={`urls-${chat.chat_id}`}
                    value="full"
                    checked={(chatEdits[chat.chat_id]?.urls_mode ?? chat.urls_mode) === "full"}
                    disabled={applyAllBusy || chatEdits[chat.chat_id]?.saving}
                    on:change={() => setChatEdit(chat.chat_id, { urls_mode: "full" })}
                  />
                  High
                </label>
              </div>
            </div>

            <button class="danger small" on:click={() => purgeChatData(chat)} disabled={applyAllBusy || maintenanceBusy || telegramBusy || syncBusy}>
              purge
            </button>
          </div>
        {/each}
      </div>
    </section>
  {/if}
</main>
