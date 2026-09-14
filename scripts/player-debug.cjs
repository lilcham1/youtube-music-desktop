// Local development probe; not included in the packaged app.
async function connect(targetKind = 'player') {
  const targets = await (await fetch('http://127.0.0.1:9231/json/list')).json();
  const target = targets.find(t => targetKind === 'player'
    ? t.url.startsWith('https://music.youtube.com/')
    : t.url.includes(targetKind === 'settings' ? '/settings.html' : '/shell.html'));
  if (!target) throw new Error(`Missing ${targetKind} debugging target`);
  const socket = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', reject, { once: true });
  });
  let id = 0;
  const pending = new Map();
  socket.addEventListener('message', ({ data }) => {
    const message = JSON.parse(data);
    const entry = pending.get(message.id);
    if (entry) {
      pending.delete(message.id);
      if (message.error) entry.reject(new Error(JSON.stringify(message.error)));
      else entry.resolve(message.result);
    }
  });
  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const requestId = ++id;
    pending.set(requestId, { resolve, reject });
    socket.send(JSON.stringify({ id: requestId, method, params }));
  });
  return {
    send,
    async evaluate(expression) {
      const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(String(result.exceptionDetails.exception?.description || result.exceptionDetails.text).slice(0,1500));
      return result.result.value;
    },
    close: () => socket.close(),
  };
}

module.exports = { connect };
if (require.main === module) {
  (async () => {
    const client = await connect(process.argv[3] || 'player');
    try { console.log(JSON.stringify(await client.evaluate(process.argv[2]), null, 2)); }
    finally { client.close(); }
  })().catch(error => { console.error(error); process.exitCode = 1; });
}
