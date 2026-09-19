/** 并发互斥复测: 目标会话有活跃 Plan Run 时, 普通消息应被拒（正确 modelKey 探针） */
'use strict';
const WebSocket = require('C:/Users/keke/Desktop/xm/react-base-service/web/sdk/node_modules/ws');

const SESSION = process.argv[2];
const ws = new WebSocket('ws://127.0.0.1:8180/react-base-service/react/ws', {
  headers: { 'X-User-Name': 'plan-e2e-tester' },
});
const timer = setTimeout(() => { console.log('TIMEOUT no response'); process.exit(1); }, 30000);
ws.on('open', () => {
  ws.send(JSON.stringify({
    type: 'run',
    sessionId: SESSION,
    payload: {
      callerKey: 'demo-app', type: 'chat', userPrompt: '并发互斥探针消息',
      modelKey: 'claude', modelVersion: 'glm-4.6',
    },
  }));
});
ws.on('message', (raw) => {
  const ev = JSON.parse(raw.toString());
  if (ev.type === 'heartbeat') return;
  console.log('EVENT:', ev.type, JSON.stringify(ev.payload || {}).slice(0, 200));
  if (ev.type === 'error' || ev.type === 'done') { clearTimeout(timer); ws.close(); process.exit(0); }
});
