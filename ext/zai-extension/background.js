// goldenticket z.ai bridge — background service worker.
//
// Keeps a websocket to the goldenticket proxy (/zai-ext/ws). On a "solve"
// request it finds an open chat.z.ai tab (or opens a hidden one) and asks the
// content script to mint an Aliyun captcha token, then returns it.
//
// Settings live in chrome.storage.local: { serverUrl, token }. The badge shows
// the connection state. Server side: zai_ext.go. Page side: content.js +
// zai-page.js.

const KEEPALIVE_ALARM = 'gt-zai-keepalive';
const HEARTBEAT_MS = 20000;
const RETRY_MS = 5000;
const CLOSE_TOKEN_REJECTED = 4401;
const ZAI_HOME = 'https://chat.z.ai/';

let socket = null;
let heartbeat = null;
let connecting = false;
let connection = { state: 'idle', message: 'Starting…' };
let lastSolve = null;

async function publish() {
  await chrome.storage.local.set({ status: { connection, lastSolve, updated: new Date().toISOString() } });
  paintBadge();
}

function setConnection(state, message) { connection = { state, message }; publish(); }
function setLastSolve(state, message) { lastSolve = { state, message, updated: new Date().toISOString() }; publish(); }

function paintBadge() {
  let text = '…';
  let color = '#6e7781';
  let title = connection.message;
  if (connection.state === 'connected') { text = 'ON'; color = '#1a7f37'; }
  else if (connection.state === 'not_configured') { text = '?'; color = '#bf8700'; }
  else if (connection.state === 'error') { text = '!'; color = '#cf222e'; }
  if (connection.state === 'connected' && lastSolve && lastSolve.state === 'failed') {
    text = '!'; color = '#cf222e'; title = `Last solve failed: ${lastSolve.message}`;
  }
  chrome.action.setBadgeText({ text });
  chrome.action.setBadgeBackgroundColor({ color });
  chrome.action.setTitle({ title: `goldenticket z.ai bridge: ${title}` });
}

function send(message) {
  if (socket && socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(message));
}

async function getSettings() {
  const s = await chrome.storage.local.get(['serverUrl', 'token']);
  return { serverUrl: (s.serverUrl || '').trim(), token: (s.token || '').trim() };
}

function wsUrl(serverUrl) {
  return serverUrl.replace(/^http/, 'ws').replace(/\/+$/, '') + '/zai-ext/ws';
}

async function connect() {
  if (connecting) return;
  const { serverUrl, token } = await getSettings();
  if (!serverUrl || !token) {
    setConnection('not_configured', 'Set the server URL and token in the extension options.');
    return;
  }
  connecting = true;
  setConnection('connecting', `Connecting to ${serverUrl}…`);
  try {
    socket = new WebSocket(wsUrl(serverUrl));
  } catch (err) {
    connecting = false;
    setConnection('error', String(err));
    scheduleRetry();
    return;
  }

  socket.onopen = () => {
    send({ type: 'hello', token, version: chrome.runtime.getManifest().version });
  };
  socket.onmessage = (event) => {
    let msg;
    try { msg = JSON.parse(event.data); } catch { return; }
    if (msg.type === 'ready') {
      connecting = false;
      setConnection('connected', `Connected to ${serverUrl}`);
      startHeartbeat();
    } else if (msg.type === 'pong') {
      // heartbeat answer
    } else if (msg.type === 'solve') {
      handleSolve(msg);
    }
  };
  socket.onclose = (event) => {
    connecting = false;
    stopHeartbeat();
    if (event.code === CLOSE_TOKEN_REJECTED) {
      setConnection('error', 'Server rejected the token. Check ZAI_EXT_TOKEN.');
    } else if (connection.state !== 'not_configured') {
      setConnection('error', `Disconnected (${event.code || 'closed'}). Retrying…`);
      scheduleRetry();
    }
  };
  socket.onerror = () => { /* onclose follows */ };
}

function scheduleRetry() { setTimeout(connect, RETRY_MS); }

function startHeartbeat() {
  stopHeartbeat();
  heartbeat = setInterval(() => send({ type: 'ping' }), HEARTBEAT_MS);
}
function stopHeartbeat() { if (heartbeat) { clearInterval(heartbeat); heartbeat = null; } }

// Find an open chat.z.ai tab, or open a background one.
async function ensureZaiTab() {
  const tabs = await chrome.tabs.query({ url: 'https://chat.z.ai/*' });
  if (tabs.length > 0) return tabs[0];
  return chrome.tabs.create({ url: ZAI_HOME, active: false });
}

async function handleSolve(msg) {
  try {
    const tab = await ensureZaiTab();
    // Give a freshly opened tab a moment to load the content script.
    await waitForContentScript(tab.id);
    const response = await chrome.tabs.sendMessage(tab.id, { gt: 'zai-solve', id: msg.id });
    if (response && response.token) {
      setLastSolve('ok', 'token minted');
      send({ type: 'token', id: msg.id, token: response.token });
    } else {
      const err = (response && response.error) || 'no token returned';
      setLastSolve('failed', err);
      send({ type: 'error', id: msg.id, error: err });
    }
  } catch (err) {
    setLastSolve('failed', String(err && err.message || err));
    send({ type: 'error', id: msg.id, error: String(err && err.message || err) });
  }
}

// Wait until the content script answers a lightweight ping, so we don't post a
// solve into a tab that hasn't loaded it yet.
async function waitForContentScript(tabId) {
  for (let i = 0; i < 40; i++) {
    try {
      // A solve with a probe id doubles as a readiness check would be wrong; use
      // a cheap scripting ping instead.
      await chrome.scripting.executeScript({ target: { tabId }, func: () => true });
      return;
    } catch (err) {
      await new Promise((r) => setTimeout(r, 250));
    }
  }
}

chrome.runtime.onInstalled.addListener(connect);
chrome.runtime.onStartup.addListener(connect);
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === KEEPALIVE_ALARM && (!socket || socket.readyState !== WebSocket.OPEN)) connect();
});
chrome.alarms.create(KEEPALIVE_ALARM, { periodInMinutes: 1 });
chrome.storage.onChanged.addListener((changes) => { if (changes.serverUrl || changes.token) connect(); });

connect();
