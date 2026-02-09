<script>
  const backend = () => window?.go?.main?.App;

  let status = null;
  let telegramStatus = null;
  let onboarding = null;
  let chats = [];
  let query = "";
  let mode = "hybrid";
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
  let tgApiID = "";
  let tgAPIHash = "";
  let tgPhone = "";
  let tgCode = "";
  let tgPassword = "";
  let backupPath = "";
  let restorePath = "";
  let embBaseURL = "";
  let embModel = "";
  let embDims = 3072;
  let embAPIKey = "";
  let embConfigured = false;
  let autostartEnabled = false;
  let backgroundPaused = false;
  let mcpPort = 0;
  let trayStatus = "unknown";
  let dataDirPath = "";
  let dataDirBusy = false;
  let currentPage = "search";
  $: onboardingIncomplete = Boolean(onboarding && !onboarding.completed);

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
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshTelegramStatus() {
    try {
      telegramStatus = await backend().TelegramAuthStatus();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshOnboardingStatus() {
    try {
      onboarding = await backend().OnboardingStatus();
      if (onboarding && !onboarding.completed && currentPage === "search") {
        currentPage = "wizard";
      }
    } catch (error) {
      errorText = String(error);
    }
  }

  function openPage(page) {
    currentPage = page;
  }

  async function refreshEmbeddingsConfig() {
    try {
      const cfg = await backend().EmbeddingsConfig();
      embBaseURL = cfg.base_url || "https://api.openai.com/v1";
      embModel = cfg.model || "text-embedding-3-large";
      embDims = Number(cfg.dimensions || 3072);
      embConfigured = Boolean(cfg.configured);
      embAPIKey = "";
    } catch (error) {
      errorText = String(error);
    }
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

  async function refreshTrayStatus() {
    try {
      trayStatus = await backend().TrayStatus();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function refreshDataDir() {
    try {
      dataDirPath = await backend().DataDir();
    } catch (error) {
      errorText = String(error);
    }
  }

  async function ensureTelegramCredentials() {
    const id = tgApiID.trim();
    const hash = tgAPIHash.trim();
    if (!id && !hash) {
      return;
    }
    if (!id || !hash) {
      throw new Error("Set both Telegram API ID and API Hash");
    }
    const parsedID = Number(id);
    if (!Number.isInteger(parsedID) || parsedID <= 0) {
      throw new Error("Telegram API ID must be a positive integer");
    }
    await backend().TelegramSetCredentials(parsedID, hash);
  }

  async function requestTelegramCode() {
    telegramBusy = true;
    errorText = "";
    try {
      await ensureTelegramCredentials();
      telegramStatus = await backend().TelegramRequestCode(tgPhone);
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
  }

  async function signInTelegram() {
    telegramBusy = true;
    errorText = "";
    try {
      telegramStatus = await backend().TelegramSignIn(tgCode, tgPassword);
      await refreshTelegramStatus();
      await refreshChats();
      await refreshOnboardingStatus();
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
  }

  async function loadTelegramChats() {
    telegramBusy = true;
    errorText = "";
    try {
      chats = await backend().TelegramLoadChats();
      await refreshStatus();
      await refreshOnboardingStatus();
    } catch (error) {
      errorText = String(error);
    } finally {
      telegramBusy = false;
    }
  }

  async function runSearch() {
    if (onboarding && !onboarding.completed) {
      errorText = "Complete onboarding first";
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
        25
      );
    } catch (error) {
      errorText = String(error);
    } finally {
      loading = false;
    }
  }

  async function runSyncNow() {
    if (onboarding && !onboarding.completed) {
      errorText = "Complete onboarding first";
      return;
    }
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

  async function exitApplication() {
    try {
      await backend().ExitApp();
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
    try {
      infoText = await backend().SetDataDir(dataDirPath);
      await refreshDataDir();
    } catch (error) {
      errorText = String(error);
    } finally {
      dataDirBusy = false;
    }
  }

  function selectedChatIds() {
    return chats.filter((chat) => chat.enabled).map((chat) => chat.chat_id);
  }

  async function updatePolicy(chat) {
    try {
      await backend().SetChatPolicy(
        chat.chat_id,
        chat.enabled,
        chat.history_mode,
        chat.allow_embeddings,
        chat.urls_mode
      );
      await refreshStatus();
      await refreshOnboardingStatus();
    } catch (error) {
      errorText = String(error);
    }
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
    try {
      const dims = Number(embDims);
      await backend().SetEmbeddingsConfig(embBaseURL, embModel, embAPIKey, dims);
      await refreshEmbeddingsConfig();
      infoText = "Embeddings config saved";
    } catch (error) {
      errorText = String(error);
    } finally {
      maintenanceBusy = false;
    }
  }

  async function rebuildSemanticIndex() {
    maintenanceBusy = true;
    errorText = "";
    infoText = "";
    try {
      infoText = await backend().RebuildSemanticIndex();
      await refreshStatus();
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
  refreshAutostart();
  refreshBackgroundPaused();
  refreshTrayStatus();
  refreshDataDir();
  refreshChats();
</script>

<main class="layout">
  <section class="hero">
    <h1>Telegram Sidecar Search</h1>
    <p>Local-first FTS + Hybrid search with read-only MCP endpoint.</p>
    <div class="row wrap navRow">
      <button class:active={currentPage === "search"} on:click={() => openPage("search")}>Search</button>
      <button class:active={currentPage === "settings"} on:click={() => openPage("settings")}>Settings</button>
      <button class:active={currentPage === "wizard"} on:click={() => openPage("wizard")}>
        Onboarding wizard
      </button>
    </div>
    {#if onboardingIncomplete}
      <p class="mutedLine">Onboarding is incomplete. Search is locked until at least one chat is enabled.</p>
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
          disabled={onboardingIncomplete}
          on:keydown={(event) => event.key === "Enter" && runSearch()}
          placeholder="Search messages..."
        />
        <select bind:value={mode} disabled={onboardingIncomplete}>
          <option value="hybrid">Hybrid</option>
          <option value="fts">FTS</option>
        </select>
        <label class="toggle">
          <input bind:checked={advanced} disabled={onboardingIncomplete} type="checkbox" />
          Advanced
        </label>
        <button on:click={runSearch} disabled={onboardingIncomplete}>
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
      {#if results.length === 0}
        <div class="empty">No results yet. Run a search.</div>
      {/if}
      {#each results as result}
        <div class="resultCard">
          <div class="resultHead">
            <strong>{result.chat_title}</strong>
            <small>{new Date(result.timestamp * 1000).toLocaleString()}</small>
          </div>
          {#if result.source_type === "url"}
            <div class="status">
              <span>Matched in URL page</span>
              <span>{result.url_title || "Untitled page"}</span>
              {#if result.url_final || result.url}
                <span>{result.url_final || result.url}</span>
              {/if}
            </div>
          {/if}
          <div class="sender">{result.sender}</div>
          <div class="snippet">{result.snippet || result.message_text}</div>
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
      <h2>Runtime</h2>
      {#if status}
        <div class="status">
          <span>MCP: {status.mcp_enabled ? "ON" : "OFF"}</span>
          <span>MCP status: {status.mcp_status || "unknown"}</span>
          <span>MCP port: {status.mcp_port || 0}</span>
          <span>Tray: {trayStatus}</span>
          <span>Endpoint: {status.mcp_endpoint || "n/a"}</span>
        </div>
      {/if}
      <div class="row">
        <button on:click={runSyncNow} disabled={syncBusy || telegramBusy || onboardingIncomplete}>
          {syncBusy ? "Syncing..." : "Sync now"}
        </button>
        <button on:click={toggleBackgroundPause} disabled={maintenanceBusy || telegramBusy || syncBusy || onboardingIncomplete}>
          {maintenanceBusy ? "Working..." : (backgroundPaused ? "Resume background" : "Pause background")}
        </button>
      </div>
      <div class="row wrap">
        <input bind:value={mcpPort} type="number" min="0" max="65535" placeholder="MCP port (0 = random free)" />
        <button on:click={saveMCPPort} disabled={mcpBusy || maintenanceBusy || telegramBusy || syncBusy}>
          {mcpBusy ? "Working..." : "Save MCP port"}
        </button>
        <button on:click={toggleMCPEnabled} disabled={mcpBusy || maintenanceBusy || telegramBusy || syncBusy}>
          {mcpBusy ? "Working..." : (status && status.mcp_enabled ? "Disable MCP" : "Enable MCP")}
        </button>
        <button on:click={copyMCPEndpoint} disabled={!status || !status.mcp_endpoint}>
          Copy endpoint
        </button>
        <button class="danger" on:click={exitApplication}>
          Exit app
        </button>
      </div>
    </section>

    <section class="panel">
      <h2>Maintenance</h2>
      <div class="status">
        <span>Embeddings: {embConfigured ? "configured" : "not configured"}</span>
        <span>Autostart: {autostartEnabled ? "enabled" : "disabled"}</span>
      </div>
      <div class="row">
        <button on:click={toggleAutostart} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : (autostartEnabled ? "Disable autostart" : "Enable autostart")}
        </button>
      </div>
      <div class="row wrap">
        <input bind:value={embBaseURL} class="pathInput" placeholder="Embeddings base URL (OpenAI-compatible)" />
        <input bind:value={embModel} placeholder="Model" />
        <input bind:value={embDims} type="number" min="1" max="8192" placeholder="Dims" />
        <input bind:value={embAPIKey} type="password" class="pathInput" placeholder="Embeddings API key (leave empty to keep current)" />
        <button on:click={saveEmbeddingsConfig} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : "Save embeddings config"}
        </button>
        <button on:click={rebuildSemanticIndex} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : "Rebuild semantic index"}
        </button>
      </div>
      <div class="row wrap">
        <input bind:value={backupPath} class="pathInput" placeholder="Backup path (.zip) or folder (optional)" />
        <button on:click={createBackup} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : "Create backup"}
        </button>
      </div>
      <div class="row wrap">
        <input bind:value={restorePath} class="pathInput" placeholder="Restore from backup .zip path" />
        <button on:click={restoreBackup} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : "Restore backup"}
        </button>
        <button class="danger" on:click={purgeAllData} disabled={maintenanceBusy || telegramBusy || syncBusy}>
          {maintenanceBusy ? "Working..." : "Purge all data"}
        </button>
      </div>
    </section>
  {/if}

  {#if currentPage === "wizard"}
    <section class="panel onboardingPanel">
      <h2>Onboarding Wizard</h2>
      <p class="mutedLine">Complete Telegram setup, discover chats, and enable at least one chat. Saved Messages stays disabled by default.</p>
      <div class="row wrap">
        <input bind:value={dataDirPath} class="pathInput" placeholder="Data directory path" />
        <button on:click={browseDataDir} disabled={dataDirBusy || onboardingBusy}>
          {dataDirBusy ? "Working..." : "Browse data dir"}
        </button>
        <button on:click={applyDataDir} disabled={dataDirBusy || onboardingBusy || !dataDirPath.trim()}>
          {dataDirBusy ? "Working..." : "Apply data dir"}
        </button>
      </div>
      <p class="mutedLine">If path changes, restart app to apply.</p>
      {#if onboarding}
        <div class="status">
          <span>Telegram configured: {onboarding.telegram_configured ? "yes" : "no"}</span>
          <span>Telegram authorized: {onboarding.telegram_authorized ? "yes" : "no"}</span>
          <span>Chats discovered: {onboarding.chats_discovered}</span>
          <span>Enabled chats: {onboarding.enabled_chats}</span>
        </div>
        <div class="row">
          <button on:click={completeOnboarding} disabled={onboardingBusy || onboarding.enabled_chats < 1}>
            {onboardingBusy ? "Finishing..." : "Complete onboarding"}
          </button>
        </div>
      {/if}
    </section>

    <section class="panel">
      <h2>Telegram Setup</h2>
      <div class="tgGrid">
        <input bind:value={tgApiID} placeholder="API ID" />
        <input bind:value={tgAPIHash} placeholder="API Hash" />
        <input bind:value={tgPhone} placeholder="Phone (+123...)" />
        <input bind:value={tgCode} placeholder="Login code" />
        <input bind:value={tgPassword} placeholder="2FA password (optional)" type="password" />
      </div>
      <div class="row">
        <button on:click={requestTelegramCode} disabled={telegramBusy}>{telegramBusy ? "Working..." : "Send code"}</button>
        <button on:click={signInTelegram} disabled={telegramBusy}>{telegramBusy ? "Working..." : "Sign in"}</button>
        <button on:click={loadTelegramChats} disabled={telegramBusy}>{telegramBusy ? "Working..." : "Load chats"}</button>
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

    <section class="panel">
      <h2>Chats & Policies</h2>
      {#each chats as chat}
        <div class="chatRow">
          <div class="chatMeta">
            <strong>{chat.title}</strong>
            <small>{chat.type}</small>
          </div>
          <label><input bind:checked={chat.enabled} on:change={() => updatePolicy(chat)} type="checkbox" /> enabled</label>
          <label><input bind:checked={chat.allow_embeddings} on:change={() => updatePolicy(chat)} type="checkbox" /> embeddings</label>
          <select bind:value={chat.history_mode} on:change={() => updatePolicy(chat)}>
            <option value="full">full</option>
            <option value="lazy">lazy</option>
          </select>
          <select bind:value={chat.urls_mode} on:change={() => updatePolicy(chat)}>
            <option value="off">off</option>
            <option value="lazy">lazy</option>
            <option value="full">full</option>
          </select>
          <button class="danger small" on:click={() => purgeChatData(chat)} disabled={maintenanceBusy || telegramBusy || syncBusy}>
            purge
          </button>
        </div>
      {/each}
    </section>
  {/if}
</main>
