const { app, BrowserWindow, BrowserView, Tray, Menu, ipcMain, nativeImage, shell } = require('electron');
const RPC = require('discord-rpc');
const fs = require('node:fs');
const path = require('node:path');

const MUSIC_URL = 'https://music.youtube.com/';
const DEFAULT_SETTINGS = {
  discordEnabled: false,
  discordAppId: '',
  minimizeToTray: true,
  closeToTray: true,
  startWithWindows: false,
};
const iconPath = path.join(__dirname, 'icon.ico');

// Keep this desktop window separate from the retired Tauri helper so Windows
// does not reuse that helper's old taskbar-icon cache.
app.setAppUserModelId('com.youtube.music.personal.desktop');

// A desktop player must have one visible application instance. Launching the
// shortcut again brings the existing window forward instead of opening another
// player (and another Discord RPC connection) in the background.
const hasSingleInstanceLock = app.requestSingleInstanceLock();
if (!hasSingleInstanceLock) {
  app.quit();
}

let mainWindow;
let playerView;
let settingsWindow;
let tray;
let rpc;
let rpcClientId = '';
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

function settingsPath() {
  return path.join(app.getPath('userData'), 'settings.json');
}

function loadSettings() {
  try {
    const saved = JSON.parse(fs.readFileSync(settingsPath(), 'utf8'));
    settings = { ...DEFAULT_SETTINGS, ...saved };
  } catch {
    settings = { ...DEFAULT_SETTINGS };
  }
}

function saveSettings(next) {
  settings = { ...settings };
  if (typeof next.discordEnabled === 'boolean') settings.discordEnabled = next.discordEnabled;
  if (typeof next.discordAppId === 'string') settings.discordAppId = next.discordAppId.trim();
  if (typeof next.minimizeToTray === 'boolean') settings.minimizeToTray = next.minimizeToTray;
  if (typeof next.closeToTray === 'boolean') settings.closeToTray = next.closeToTray;
  if (typeof next.startWithWindows === 'boolean') {
    settings.startWithWindows = next.startWithWindows;
    app.setLoginItemSettings({ openAtLogin: settings.startWithWindows, openAsHidden: true });
  }
  fs.writeFileSync(settingsPath(), JSON.stringify(settings, null, 2));
  return settings;
}

function settingsSnapshot() {
  return { ...settings, startWithWindows: app.getLoginItemSettings().openAtLogin };
}

function showSettings(section = 'general') {
  if (settingsWindow && !settingsWindow.isDestroyed()) {
    settingsWindow.setTitle(section === 'discord' ? 'Discord Rich Presence' : 'YouTube Music Settings');
    settingsWindow.loadFile(path.join(__dirname, 'settings.html'), { query: { section } });
    settingsWindow.show();
    settingsWindow.focus();
    return;
  }

  settingsWindow = new BrowserWindow({
    parent: mainWindow,
    modal: true,
    width: 440,
    height: 410,
    resizable: false,
    maximizable: false,
    title: section === 'discord' ? 'Discord Rich Presence' : 'YouTube Music Settings',
    icon: iconPath,
    backgroundColor: '#171717',
    webPreferences: {
      preload: path.join(__dirname, 'settings-preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  settingsWindow.setMenuBarVisibility(false);
  settingsWindow.loadFile(path.join(__dirname, 'settings.html'), { query: { section } });
  settingsWindow.on('closed', () => { settingsWindow = undefined; });
}

function showMainWindow() {
  if (!mainWindow || mainWindow.isDestroyed()) return;
  if (mainWindow.isMinimized()) mainWindow.restore();
  mainWindow.show();
  mainWindow.focus();
}

function sizePlayerView() {
  if (!mainWindow || !playerView) return;
  const [width, height] = mainWindow.getContentSize();
  playerView.setBounds({ x: 0, y: 40, width, height: Math.max(0, height - 40) });
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
    if (rpc && lastActivityKey) {
      lastActivityKey = '';
      try { await rpc.clearActivity(); } catch {}
    }
    return;
  }

  const activityKey = [playback.title, playback.artist, playback.album, playback.startedAt].join('|');
  if (activityKey === lastActivityKey && rpc && rpcClientId === settings.discordAppId) return;

  try {
    const client = await ensureDiscord();
    if (!client || !playback.playing) return;
    // discord-rpc's setActivity helper drops activity.type and nested assets.
    // Use Discord's SET_ACTIVITY schema directly so this renders as Listening
    // with the real album image instead of a generic game activity.
    await client.request('SET_ACTIVITY', {
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
    });
    lastActivityKey = activityKey;
  } catch {
    // Discord may be closed. The next genuine track/playback event retries it.
    await disconnectDiscord();
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
    webPreferences: {
      preload: path.join(__dirname, 'shell-preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  mainWindow.setMenuBarVisibility(false);
  mainWindow.loadFile(path.join(__dirname, 'shell.html'));
  playerView = new BrowserView({
    webPreferences: {
      preload: path.join(__dirname, 'player-preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  mainWindow.setBrowserView(playerView);
  sizePlayerView();
  playerView.webContents.loadURL(MUSIC_URL);
  playerView.webContents.on('did-finish-load', () => { setTimeout(injectVolumeControl, 600); });
  playerView.webContents.on('media-started-playing', () => { void readActiveTrack(); });
  playerView.webContents.on('media-paused', () => {
    updatePlayback({ ...playback, playing: false });
  });
  mainWindow.on('resize', sizePlayerView);
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
  mainWindow.on('closed', () => { mainWindow = undefined; playerView = undefined; });
}

ipcMain.on('open-settings', () => showSettings('general'));
ipcMain.on('open-discord-settings', () => showSettings('discord'));
ipcMain.on('window:minimize', () => mainWindow?.minimize());
ipcMain.on('window:maximize', () => {
  if (!mainWindow) return;
  if (mainWindow.isMaximized()) mainWindow.unmaximize();
  else mainWindow.maximize();
});
ipcMain.on('window:close', () => mainWindow?.close());
ipcMain.on('volume:set', (_event, value) => { void setPlayerVolume(value); });
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

async function injectVolumeControl() {
  if (!playerView || playerView.webContents.isDestroyed()) return;
  try {
    await playerView.webContents.executeJavaScript(`(() => {
      if (window.__ytmNumericVolumeInstalled || document.getElementById('ytm-desktop-volume')) return;
      window.__ytmNumericVolumeInstalled = true;
      const find = (selector, root = document) => {
        const direct = root.querySelector?.(selector);
        if (direct) return direct;
        for (const element of root.querySelectorAll?.('*') || []) {
          if (element.shadowRoot) { const nested = find(selector, element.shadowRoot); if (nested) return nested; }
        }
        return null;
      };
      const clamp = (value) => Math.max(0, Math.min(100, Math.round(Number(value) || 0)));
      const widget = document.createElement('label');
      widget.id = 'ytm-desktop-volume';
      widget.title = 'Volume (0 to 100)';
      widget.innerHTML = '<span>VOL</span><input aria-label="Volume, 0 to 100" type="number" min="0" max="100" step="1">';
      const input = widget.querySelector('input');
      document.documentElement.append(widget);
      let slider;
      const bind = () => {
        slider = find('#volume-slider') || find('paper-slider#volume-slider') || slider;
        if (!slider) { input.disabled = true; input.value = '0'; return; }
        input.disabled = false;
        input.value = String(clamp(slider.value ?? slider.getAttribute('value')));
        slider.style.display = 'none';
      };
      input.addEventListener('change', () => {
        const value = clamp(input.value);
        input.value = String(value);
        const player = find('ytmusic-player-bar');
        if (typeof player?.updateVolume === 'function') player.updateVolume(value);
        else if (slider) {
          slider.value = value;
          slider.dispatchEvent(new Event('input', { bubbles: true }));
          slider.dispatchEvent(new Event('change', { bubbles: true }));
        }
      });
      new MutationObserver(bind).observe(document.documentElement, { childList: true, subtree: true });
      bind();
    })()`, true);
  } catch {}
}

async function setPlayerVolume(value) {
  if (!playerView || playerView.webContents.isDestroyed()) return;
  const volume = Math.max(0, Math.min(100, Math.round(Number(value) || 0)));
  try {
    await playerView.webContents.executeJavaScript(`(() => {
      const find = (selector, root = document) => {
        const direct = root.querySelector?.(selector);
        if (direct) return direct;
        for (const element of root.querySelectorAll?.('*') || []) {
          if (element.shadowRoot) { const nested = find(selector, element.shadowRoot); if (nested) return nested; }
        }
        return null;
      };
      const slider = find('#volume-slider') || find('paper-slider#volume-slider') || find('input[type="range"]');
      const player = find('ytmusic-player-bar');
      if (typeof player?.updateVolume === 'function') player.updateVolume(${volume});
      else if (slider) { slider.value = ${volume}; slider.dispatchEvent(new Event('input', { bubbles: true })); slider.dispatchEvent(new Event('change', { bubbles: true })); }
    })()`, true);
  } catch {}
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
ipcMain.on('settings:quit', () => app.quit());

app.whenReady().then(() => {
  if (!hasSingleInstanceLock) return;
  loadSettings();
  createMainWindow();
  createTray();
  app.on('activate', () => {
    if (!mainWindow) createMainWindow();
    else showMainWindow();
  });
});

app.on('second-instance', () => {
  if (app.isReady()) showMainWindow();
});

app.on('before-quit', () => { app.isQuitting = true; });
app.on('window-all-closed', () => {});
app.on('will-quit', () => { void disconnectDiscord(); });
