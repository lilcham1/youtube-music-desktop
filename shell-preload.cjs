const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('youtubeMusicShell', {
  getSettings: () => ipcRenderer.invoke('settings:get'),
  openSettings: () => ipcRenderer.send('open-settings'),
  openDiscordSettings: () => ipcRenderer.send('open-discord-settings'),
  setVolume: (value) => ipcRenderer.send('volume:set', Number(value)),
  minimize: () => ipcRenderer.send('window:minimize'),
  maximize: () => ipcRenderer.send('window:maximize'),
  close: () => ipcRenderer.send('window:close'),
  onSettingsChanged: (callback) => ipcRenderer.on('settings:changed', (_event, settings) => callback(settings)),
});
