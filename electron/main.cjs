const { app, BrowserWindow, BrowserView, Tray, Menu, ipcMain, nativeImage, shell, dialog } = require('electron');
const RPC = require('discord-rpc');
const { autoUpdater } = require('electron-updater');
const fs = require('node:fs');
const path = require('node:path');
const { profileDirectory, migrateLegacyProfile, writeSettings } = require('./profile.cjs');
const {
  YOUTUBE_COOKIE_DOMAIN, YOUTUBE_COOKIE_URL, withPrefVolume, engineToSliderVolume, applyPlayerVolume,
} = require('./player-volume.cjs');

const MUSIC_URL = 'https://music.youtube.com/';
// Public Discord Application ID used by every installation. It is an
// identifier, not a secret: end users only choose whether to enable it.
const DISCORD_APPLICATION_ID = '1547064138604347462';
const DEFAULT_SETTINGS = {
  discordEnabled: false,
  discordAppId: DISCORD_APPLICATION_ID,
  minimizeToTray: false,
  closeToTray: false,
  startWithWindows: false,
  volume: 50,
};
const iconPath = path.join(__dirname, 'icon.ico');

// Keep YouTube's media pipeline alive when the player is minimized, covered by
// another window, or restored from the tray. Chromium's background timer
// throttling can otherwise interrupt long-running playback.
app.commandLine.appendSwitch('disable-background-timer-throttling');
app.commandLine.appendSwitch('disable-renderer-backgrounding');
app.commandLine.appendSwitch('disable-backgrounding-occluded-windows');

// Keep this desktop window separate from the retired Tauri helper so Windows
// does not reuse that helper's old taskbar-icon cache.
app.setAppUserModelId('com.youtube.music.personal.desktop');

const legacyProfile = path.join(app.getPath('appData'), 'youtube-music-personal');
const sharedProfile = profileDirectory(app.getPath('home'));
fs.mkdirSync(sharedProfile, { recursive: true });
app.setPath('userData', sharedProfile);
app.setPath('sessionData', sharedProfile);

// A desktop player must have one visible application instance. Launching the
// shortcut again brings the existing window forward instead of opening another
// player (and another Discord RPC connection) in the background.
const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
  app.quit();
}

let mainWindow;
let playerView;
let settingsView;
let tray;
let rpc;
let rpcClientId = '';
let discordRetryTimer;
let discordRetryAttempt = 0;
let discordSyncInFlight = false;
let discordStatus = 'Off';
let settings = { ...DEFAULT_SETTINGS };
let playback = {
  playing: false,
  title: '',
  artist: '',
  album: '',
  artwork: '',
  positionSeconds: 0,
  durationSeconds: 0,
  startedAt: 0,
};
let lastActivityKey = '';
let updateStatus = { state: 'idle', message: 'Ready to check for updates.' };

function settingsPath() {
  return path.join(app.getPath('userData'), 'settings.json');
}

function loadSettings() {
  try {
    const saved = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
    settings = { ...DEFAULT_SETTINGS, ...saved };
    // Migrate installs made before Rich Presence was bundled.
    if (!settings.discordAppId) settings.discordAppId = DISCORD_APPLICATION_ID;
    // v0.1.16 changed the standard window behavior: minimize keeps the app on
    // the taskbar and close exits it. Preserve any choices made afterwards.
    let changed = false;
    if (saved.windowBehaviorVersion !== 1) {
      settings.minimizeToTray = false;
      settings.closeToTray = false;
      settings.windowBehaviorVersion = 1;
      changed = true;
    }
    // 0.1.32 stores the volume on YouTube Music's slider scale instead of the
    // player-engine gain. Convert once so the upgrade keeps the same loudness.
    if (saved.volumeScale !== 'slider') {
      settings.volume = engineToSliderVolume(settings.volume) ?? DEFAULT_SETTINGS.volume;
      settings.volumeScale = 'slider';
      changed = true;
    }
    if (changed) writeSettings(settingsPath(), settings);
  } catch (error) {
    // A missing or unreadable settings file (first run, or a corrupt/truncated
    // JSON left by an OS crash) must not stop the app from starting: fall back
    // to defaults and rewrite a valid file. Only genuine access failures
    // (EACCES/EPERM/EISDIR) are surfaced to the user as a profile problem.
    if (error.code && error.code !== 'ENOENT') throw error;
    settings = { ...DEFAULT_SETTINGS, windowBehaviorVersion: 1, volumeScale: 'slider' };
    try {
      writeSettings(settingsPath(), settings);
    } catch (writeError) {
      if (writeError.code === 'EACCES' || writeError.code === 'EPERM') throw writeError;
    }
  }
}

function saveSettings(next) {
  settings = { ...settings };
  if (typeof next.discordEnabled === 'boolean') settings.discordEnabled = next.discordEnabled;
  // Keep the bundled application identity stable for every user. This is not
  // configurable in the UI, but accepting a non-empty legacy value preserves
  // settings written by earlier releases.
  if (typeof next.discordAppId === 'string' && next.discordAppId.trim()) settings.discordAppId = next.discordAppId.trim();
  if (typeof next.minimizeToTray === 'boolean') settings.minimizeToTray = next.minimizeToTray;
  if (typeof next.closeToTray === 'boolean') settings.closeToTray = next.closeToTray;
  if (typeof next.startWithWindows === 'boolean') {
    settings.startWithWindows = next.startWithWindows;
    app.setLoginItemSettings({ openAtLogin: settings.startWithWindows, openAsHidden: true });
  }
  if (next.volume !== undefined && Number.isFinite(Number(next.volume))) {
    settings.volume = Math.max(0, Math.min(100, Math.round(Number(next.volume))));
  }
  writeSettings(settingsPath(), settings);
  return settings;
}

function settingsSnapshot() {
  return {
    ...settings,
    version: app.getVersion(),
    startWithWindows: app.getLoginItemSettings().openAtLogin,
    discordStatus,
  };
}

function setDiscordStatus(message) {
  discordStatus = message;
  settingsView?.webContents.send('discord:status', message);
}

function stopDiscordRetry() {
  if (discordRetryTimer) clearTimeout(discordRetryTimer);
  discordRetryTimer = undefined;
  discordRetryAttempt = 0;
}

function scheduleDiscordRetry() {
  if (discordRetryTimer || !settings.discordEnabled || !playback.playing || !playback.title) return;
  const delay = Math.min(30_000, 5_000 * (2 ** discordRetryAttempt));
  discordRetryAttempt = Math.min(discordRetryAttempt + 1, 3);
  setDiscordStatus(`Discord is busy. Retrying in ${Math.ceil(delay / 1000)} seconds…`);
  discordRetryTimer = setTimeout(() => {
    discordRetryTimer = undefined;
    void syncDiscord();
  }, delay);
}

function syncShellSettings() {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  const volume = JSON.stringify(String(settings.volume));
  const discordEnabled = settings.discordEnabled ? 'true' : 'false';
  void mainWindow.webContents.executeJavaScript(`(() => {
    const volumeInput = document.getElementById('volume');
    if (volumeInput) volumeInput.value = ${volume};
    document.getElementById('discord')?.classList.toggle('enabled', ${discordEnabled});
  })()`, true).catch(() => {});
}

function showSettings(section = 'general') {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  showMainWindow();
  if (!settingsView) {
    settingsView = new BrowserView({
      webPreferences: {
        preload: path.join(__dirname, 'settings-preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
      },
    });
  }
  mainWindow.setBrowserView(settingsView);
  sizeContentView();
  settingsView.webContents.loadFile(path.join(__dirname, 'settings.html'), { query: { section } });
}

function closeSettings() {
  if (!mainWindow || mainWindow.isDestroyed() || !settingsView) return;
  mainWindow.setBrowserView(playerView);
  settingsView.webContents.close();
  settingsView = undefined;
  sizeContentView();
}

function showMainWindow() {
  // A legacy tray-only process may have no window left. Recreate it instead
  // of leaving the tray icon unable to bring the app back.
  if (!mainWindow || mainWindow.isDestroyed()) {
    createMainWindow();
    return;
  }
  if (mainWindow.isMinimized()) mainWindow.restore();
  mainWindow.show();
  mainWindow.focus();
}

function sizeContentView() {
  if (!mainWindow) return;
  const [width, height] = mainWindow.getContentSize();
  const activeView = settingsView || playerView;
  activeView?.setBounds({ x: 0, y: 40, width, height: Math.max(0, height - 40) });
}

function setUpdateStatus(state, message) {
  updateStatus = { state, message };
  settingsView?.webContents.send('updates:status', updateStatus);
}

function configureUpdater() {
  autoUpdater.autoDownload = true;
  autoUpdater.autoInstallOnAppQuit = false;
  autoUpdater.on('checking-for-update', () => setUpdateStatus('checking', 'Checking for updates…'));
  autoUpdater.on('update-available', (info) => setUpdateStatus('downloading', `Downloading YouTube Music ${info.version}…`));
  autoUpdater.on('download-progress', (progress) => setUpdateStatus('downloading', `Downloading update: ${Math.round(progress.percent)}%`));
  autoUpdater.on('update-not-available', () => setUpdateStatus('current', `You’re up to date (v${app.getVersion()}).`));
  autoUpdater.on('update-downloaded', (info) => setUpdateStatus('downloaded', `YouTube Music ${info.version} is ready to install.`));
  autoUpdater.on('error', () => setUpdateStatus('error', 'Updates are unavailable right now.'));
}

async function checkForUpdates() {
  if (!app.isPackaged) {
    setUpdateStatus('unavailable', 'Updates are available from the installed app.');
    return updateStatus;
  }
  try {
    await autoUpdater.checkForUpdates();
  } catch {
    setUpdateStatus('error', 'Updates are unavailable right now.');
  }
  return updateStatus;
}

function createTray() {
  const icon = nativeImage.createFromPath(iconPath);
  tray = new Tray(icon);
  tray.setToolTip('YouTube Music');
  tray.setContextMenu(Menu.buildFromTemplate([
    { label: 'Open YouTube Music', click: showMainWindow },
    { label: 'Settings…', click: () => showSettings('general') },
    { label: 'Discord Rich Presence…', click: () => showSettings('discord') },
    { type: 'separator' },
    { label: 'Quit YouTube Music', click: () => app.quit() },
  ]));
  tray.on('click', showMainWindow);
  tray.on('double-click', showMainWindow);
  tray.on('mouse-up', (event) => {
    if (event.button === 0 || event.button === undefined) showMainWindow();
  });
}

async function disconnectDiscord() {
  lastActivityKey = '';
  if (!rpc) return;
  const client = rpc;
  rpc = undefined;
  rpcClientId = '';
  try { await client.clearActivity(); } catch {}
  try { client.destroy(); } catch {}
}

async function ensureDiscord() {
  if (!settings.discordEnabled || !settings.discordAppId) return undefined;
  if (rpc && rpcClientId === settings.discordAppId) return rpc;
  await disconnectDiscord();
  const client = new RPC.Client({ transport: 'ipc' });
  await client.login({ clientId: settings.discordAppId });
  rpc = client;
  rpcClientId = settings.discordAppId;
  return client;
}

async function syncDiscord() {
  if (!settings.discordEnabled || !playback.playing || !playback.title) {
    if (!settings.discordEnabled) {
      stopDiscordRetry();
      setDiscordStatus('Off');
    } else {
      setDiscordStatus('Waiting for a song to play.');
    }
    if (rpc && lastActivityKey) {
      lastActivityKey = '';
      try { await rpc.clearActivity(); } catch {}
    }
    return;
  }

  if (discordSyncInFlight) return;

  const activityKey = [playback.title, playback.artist, playback.album, playback.startedAt].join('|');
  if (activityKey === lastActivityKey && rpc && rpcClientId === settings.discordAppId) return;

  discordSyncInFlight = true;
  try {
    const client = await ensureDiscord();
    if (!client || !playback.playing) return;
    // discord-rpc's setActivity helper drops activity.type and nested assets.
    // Use Discord's SET_ACTIVITY schema directly so this renders as Listening
    // with the real album image instead of a generic game activity.
    const activity = {
      pid: process.pid,
      activity: {
        type: 2,
        // Discord's compact member-list row can use state instead of the app name.
        // For music, state is the artist, while details stays the song title.
        status_display_type: 1,
        name: playback.artist || 'YouTube Music',
        details: playback.title,
        state: playback.artist || 'YouTube Music',
        timestamps: playback.startedAt ? {
          start: playback.startedAt,
          ...(playback.durationSeconds > 0 ? { end: playback.startedAt + playback.durationSeconds * 1000 } : {}),
        } : undefined,
        assets: {
          large_image: playback.artwork || 'https://music.youtube.com/img/favicon_144.png',
          // Single releases frequently use the song title as the album title.
          // Omitting it in that case avoids showing the same line twice.
          ...(
            playback.album
            && playback.album.localeCompare(playback.title, undefined, { sensitivity: 'accent' }) !== 0
              ? { large_text: playback.album }
              : {}
          ),
          small_image: 'https://music.youtube.com/img/favicon_144.png',
          small_text: 'YouTube Music',
        },
        instance: false,
      },
    };
    try {
      await client.request('SET_ACTIVITY', activity);
    } catch (error) {
      // Artwork URLs require Discord-side asset support. Never let an artwork
      // issue hide the entire status: retry with a standards-only activity.
      const fallback = structuredClone(activity);
      delete fallback.activity.assets;
      await client.request('SET_ACTIVITY', fallback);
    }
    lastActivityKey = activityKey;
    stopDiscordRetry();
    setDiscordStatus('Connected — showing the current song.');
  } catch (error) {
    await disconnectDiscord();
    scheduleDiscordRetry();
  } finally {
    discordSyncInFlight = false;
  }
}

function createMainWindow() {
  mainWindow = new BrowserWindow({
    width: 1280,
    height: 800,
    minWidth: 900,
    minHeight: 600,
    title: 'YouTube Music',
    icon: iconPath,
    backgroundColor: '#030303',
    frame: false,
    // Windows otherwise adds an accent-coloured resize frame around frameless
    // windows. The custom title bar already provides the window controls.
    thickFrame: false,
    webPreferences: {
      preload: path.join(__dirname, 'shell-preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  mainWindow.setMenuBarVisibility(false);
  // Pass the current values with the first title-bar load as well as through
  // IPC below. This avoids a race where a freshly created shell renders its
  // hard-coded placeholders before the asynchronous settings request returns.
  mainWindow.loadFile(path.join(__dirname, 'shell.html'), {
    query: {
      volume: String(settings.volume),
      discordEnabled: settings.discordEnabled ? '1' : '0',
    },
  });
  // A direct post-load sync is intentionally kept alongside the initial URL
  // values and IPC bridge. It covers renderer restarts that can otherwise
  // leave title-bar controls at their HTML fallback values.
  mainWindow.webContents.on('did-finish-load', syncShellSettings);
  playerView = new BrowserView({
      webPreferences: {
        preload: path.join(__dirname, 'player-preload.cjs'),
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        backgroundThrottling: false,
      },
  });
  mainWindow.setBrowserView(playerView);
  sizeContentView();
  // YouTube Music reads its player volume from the PREF cookie while the page
  // is rendered, so seeding it first makes the player start at the saved
  // level from its first frame. No mute/un-mute dance is needed, which is
  // what previously left the stream silent when the un-mute never arrived.
  const view = playerView;
  void seedVolumeCookie(settings.volume).finally(() => {
    if (view === playerView && !view.webContents.isDestroyed()) view.webContents.loadURL(MUSIC_URL);
  });
  // Belt and braces for the cookie: if the page somehow rendered with a stale
  // level, correct it through the player bar once the app is up.
  playerView.webContents.on('did-finish-load', () => {
    void setPlayerVolume(settings.volume);
  });
  playerView.webContents.on('media-started-playing', () => { void readActiveTrack(); });
  playerView.webContents.on('media-paused', () => {
    updatePlayback({ ...playback, playing: false });
  });
  mainWindow.on('resize', sizeContentView);
  mainWindow.on('minimize', (event) => {
    if (settings.minimizeToTray) {
      event.preventDefault();
      mainWindow.hide();
    }
  });
  mainWindow.on('close', (event) => {
    if (!app.isQuitting && settings.closeToTray) {
      event.preventDefault();
      mainWindow.hide();
    }
  });
  mainWindow.on('closed', () => { mainWindow = undefined; playerView = undefined; settingsView = undefined; });
}

ipcMain.on('open-settings', () => showSettings('general'));
ipcMain.on('open-discord-settings', () => showSettings('discord'));
ipcMain.on('settings:close', closeSettings);
ipcMain.on('window:minimize', () => mainWindow?.minimize());
ipcMain.on('window:maximize', () => {
  if (!mainWindow) return;
  if (mainWindow.isMaximized()) mainWindow.unmaximize();
  else mainWindow.maximize();
});
ipcMain.on('window:close', () => mainWindow?.close());
ipcMain.on('volume:set', (_event, value) => {
  const saved = saveSettings({ volume: value });
  void setPlayerVolume(saved.volume);
  syncShellSettings();
  mainWindow?.webContents.send('settings:changed', settingsSnapshot());
});

// The user moved YouTube Music's own slider. Persist it so the title bar and
// the next launch agree with the page, without re-applying it to the player
// (the page already did that, and echoing it back would loop).
ipcMain.on('player:volume-changed', (event, value) => {
  if (event.sender !== playerView?.webContents) return;
  if (Number(value) === settings.volume) return;
  saveSettings({ volume: value });
  syncShellSettings();
  mainWindow?.webContents.send('settings:changed', settingsSnapshot());
});
function updatePlayback(next) {
  const wasPlaying = playback.playing;
  const changedTrack = playback.title !== next.title || playback.artist !== next.artist;
  const positionSeconds = Number.isFinite(Number(next.positionSeconds))
    ? Math.max(0, Number(next.positionSeconds))
    : playback.positionSeconds;
  const durationSeconds = Number.isFinite(Number(next.durationSeconds))
    ? Math.max(0, Number(next.durationSeconds))
    : playback.durationSeconds;
  const startedAt = next.playing
    ? Date.now() - positionSeconds * 1000
    : playback.startedAt;
  playback = {
    playing: Boolean(next.playing),
    title: String(next.title || '').trim(),
    artist: String(next.artist || '').trim(),
    album: String(next.album || '').trim(),
    artwork: String(next.artwork || '').trim(),
    positionSeconds,
    durationSeconds,
    startedAt: changedTrack || !wasPlaying || next.positionSeconds !== undefined ? startedAt : playback.startedAt,
  };
  void syncDiscord();
}

async function readActiveTrack() {
  if (!playerView || playerView.webContents.isDestroyed()) return;
  try {
    const next = await playerView.webContents.executeJavaScript(`(() => {
      const text = (selector, root = document) => root.querySelector?.(selector)?.textContent?.trim() || '';
      const find = (selector, root = document) => {
        const direct = root.querySelector?.(selector);
        if (direct) return direct;
        for (const element of root.querySelectorAll?.('*') || []) {
          if (element.shadowRoot) {
            const nested = find(selector, element.shadowRoot);
            if (nested) return nested;
          }
        }
        return null;
      };
      const metadata = navigator.mediaSession?.metadata;
      const video = find('video');
      const playerTitle = find('#song-title') || find('.title.ytmusic-player-bar');
      const playerByline = find('#byline') || find('.byline.ytmusic-player-bar');
      const pageTitle = document.title.replace(/\s*[-–|]\s*YouTube Music.*$/i, '').trim();
      return {
        playing: Boolean(video && !video.paused && !video.ended),
        title: metadata?.title || playerTitle?.textContent?.trim() || pageTitle,
        artist: metadata?.artist || playerByline?.textContent?.trim() || '',
        album: metadata?.album || '',
        artwork: metadata?.artwork?.[metadata.artwork.length - 1]?.src || '',
        positionSeconds: video?.currentTime || 0,
        durationSeconds: Number.isFinite(video?.duration) ? video.duration : 0,
      };
    })()`, true);
    updatePlayback(next);
  } catch {
    // Navigation can briefly invalidate the player during a track change.
  }
}

// Store the saved level in the cookie YouTube Music itself reads, preserving
// every other PREF field (locale, autoplay flags, ...) the site may have set.
async function seedVolumeCookie(value) {
  if (!playerView || playerView.webContents.isDestroyed()) return;
  const cookies = playerView.webContents.session.cookies;
  try {
    // YouTube sets PREF on .youtube.com; prefer that over any host-only copy.
    const found = await cookies.get({ name: 'PREF', domain: 'youtube.com' });
    const existing = found.find((cookie) => cookie.domain === YOUTUBE_COOKIE_DOMAIN) ?? found[0];
    const pref = withPrefVolume(existing?.value, value);
    if (existing?.value === pref) return;
    await cookies.set({
      url: YOUTUBE_COOKIE_URL,
      name: 'PREF',
      value: pref,
      domain: YOUTUBE_COOKIE_DOMAIN,
      path: '/',
      secure: true,
      sameSite: 'no_restriction',
      expirationDate: Math.floor(Date.now() / 1000) + 2 * 365 * 24 * 60 * 60,
    });
  } catch (error) {
    console.warn('Unable to seed YouTube Music volume cookie:', error.message);
  }
}

async function setPlayerVolume(value) {
  if (!playerView || playerView.webContents.isDestroyed()) return false;
  const volume = Math.max(0, Math.min(100, Math.round(Number(value) || 0)));
  void seedVolumeCookie(volume);
  try {
    return await playerView.webContents.executeJavaScript(
      `(${applyPlayerVolume.toString()})(${volume})`, true,
    );
  } catch (error) {
    console.warn('Unable to apply player volume:', error.message);
    return false;
  }
}

ipcMain.on('playback', (_event, next) => {
  updatePlayback(next);
});

ipcMain.handle('settings:get', () => settingsSnapshot());
ipcMain.handle('settings:save', async (_event, next) => {
  const saved = saveSettings(next || {});
  if (!saved.discordEnabled) await disconnectDiscord();
  await syncDiscord();
  const snapshot = settingsSnapshot();
  mainWindow?.webContents.send('settings:changed', snapshot);
  return snapshot;
});
ipcMain.handle('settings:open-discord-portal', () => shell.openExternal('https://discord.com/developers/applications'));
ipcMain.handle('updates:get-status', () => updateStatus);
ipcMain.handle('updates:check', checkForUpdates);
ipcMain.handle('updates:install', () => {
  if (updateStatus.state === 'downloaded') autoUpdater.quitAndInstall();
});
ipcMain.on('settings:quit', () => app.quit());

app.whenReady().then(() => {
  if (!hasSingleInstanceLock) return;
  try {
    migrateLegacyProfile(sharedProfile, legacyProfile);
    loadSettings();
  } catch (error) {
    dialog.showErrorBox('YouTube Music could not load your profile',
      `Your saved profile could not be opened. Please check access to ${sharedProfile}.\n\n${error.message}`);
    app.quit();
    return;
  }
  createMainWindow();
  createTray();
  configureUpdater();
  setTimeout(() => { void checkForUpdates(); }, 12000);
  setInterval(() => { void checkForUpdates(); }, 4 * 60 * 60 * 1000).unref();
  app.on('activate', () => {
    if (!mainWindow) createMainWindow();
    else showMainWindow();
  });
});

app.on('second-instance', () => {
  if (app.isReady()) showMainWindow();
});

app.on('before-quit', () => { app.isQuitting = true; });
app.on('window-all-closed', () => {
  // When Close to tray is disabled, closing the last window really exits the
  // app and removes the notification-area icon.
  if (!app.isQuitting) app.quit();
});
app.on('will-quit', () => { void disconnectDiscord(); });
