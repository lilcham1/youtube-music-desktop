const net = require('node:net');

function frame(op, data) {
  const body = Buffer.from(JSON.stringify(data));
  const packet = Buffer.alloc(8 + body.length);
  packet.writeInt32LE(op, 0); packet.writeInt32LE(body.length, 4); body.copy(packet, 8);
  return packet;
}

const socket = net.createConnection('\\\\?\\pipe\\discord-ipc-0');
let buffer = Buffer.alloc(0);
socket.on('connect', () => socket.write(frame(0, { v: 1, client_id: '1547064138604347462' })));
socket.on('data', data => {
  buffer = Buffer.concat([buffer, data]);
  while (buffer.length >= 8 && buffer.length >= 8 + buffer.readInt32LE(4)) {
    const length = buffer.readInt32LE(4);
    console.log(JSON.stringify({ op: buffer.readInt32LE(0), data: JSON.parse(buffer.subarray(8, 8 + length).toString()) }));
    buffer = buffer.subarray(8 + length);
  }
});
socket.on('close', () => console.log('IPC closed'));
socket.on('error', error => console.error('IPC error:', error.message));
setTimeout(() => socket.destroy(), 4000).unref();
