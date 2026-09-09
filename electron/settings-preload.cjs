const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('youtubeMusicSettings', {
  get: () => ipcRenderer.invoke('settings:get'),
  save: (settings) => ipcRenderer.invoke('settings:save', settings),
  openDiscordPortal: () => ipcRenderer.invoke('settings:open-discord-portal'),
  getUpdateStatus: () => ipcRenderer.invoke('updates:get-status'),
  checkForUpdates: () => ipcRenderer.invoke('updates:check'),
  installUpdate: () => ipcRenderer.invoke('updates:install'),
  close: () => ipcRenderer.send('settings:close'),
  quit: () => ipcRenderer.send('settings:quit'),
  onUpdateStatus: (callback) => ipcRenderer.on('updates:status', (_event, status) => callback(status)),
});
