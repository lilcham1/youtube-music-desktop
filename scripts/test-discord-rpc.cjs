const RPC = require('discord-rpc');

(async () => {
  const client = new RPC.Client({ transport: 'ipc' });
  client.on('error', error => console.error('RPC error:', error.message));
  try {
    await client.login({ clientId: '1547064138604347462' });
    console.log('Connected as', client.user?.username || 'unknown');
    await client.setActivity({
      type: 2,
      details: 'Connection test',
      state: 'YouTube Music',
      instance: false,
    });
    console.log('Minimal activity accepted');
    await new Promise(resolve => setTimeout(resolve, 3000));
    await client.clearActivity();
  } catch (error) {
    console.error('RPC failure:', error.stack || error.message);
    process.exitCode = 1;
  } finally {
    client.destroy();
  }
})();
