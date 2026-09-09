const { contextBridge, ipcRenderer } = require('electron');

contextBridge.exposeInMainWorld('youtubeMusicSettings', {
  get: () => ipcRenderer.invoke('settings:get'),
  save: (settings) => ipcRenderer.invoke('settings:save', settings),
  openDiscordPortal: () => ipcRenderer.invoke('settings:open-discord-portal'),
  openLatestRelease: () => ipcRenderer.invoke('updates:open-release'),
  close: () => ipcRenderer.send('settings:close'),
  quit: () => ipcRenderer.send('settings:quit'),
});
