const fs = require('node:fs');
const path = require('node:path');

function profileDirectory(homeDirectory) {
  // Packaged Windows launchers can virtualize AppData for child processes.
  // A folder directly under the user's home is shared by Explorer, startup,
  // the updater, and development tools, regardless of the launching package.
  return path.join(homeDirectory, '.youtube-music');
}

function migrateLegacyProfile(destination, legacyDirectory) {
  const marker = path.join(destination, '.profile-ready');
  if (fs.existsSync(marker)) return;
  // Never replace an existing account or settings with a launcher's legacy
  // profile. The migration runs only after acquiring the single-instance lock.
  if (!fs.existsSync(path.join(destination, 'settings.json'))
      && !fs.existsSync(path.join(destination, 'Network', 'Cookies'))
      && fs.existsSync(legacyDirectory)) {
    const excluded = new Set(['Cache', 'Code Cache', 'GPUCache', 'DawnGraphiteCache',
      'DawnWebGPUCache', 'blob_storage', 'SingletonLock', 'SingletonCookie', 'SingletonSocket']);
    for (const entry of fs.readdirSync(legacyDirectory)) {
      if (excluded.has(entry)) continue;
      fs.cpSync(path.join(legacyDirectory, entry), path.join(destination, entry), {
        recursive: true, force: false, errorOnExist: false,
      });
    }
  }
  fs.writeFileSync(marker, '1\n');
}

function writeSettings(file, settings) {
  // Finish writing before replacing the last valid settings file.
  const temporary = `${file}.tmp`;
  fs.writeFileSync(temporary, JSON.stringify(settings, null, 2));
  fs.renameSync(temporary, file);
}

module.exports = { profileDirectory, migrateLegacyProfile, writeSettings };
