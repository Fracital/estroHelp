// Status popup: read the published status from storage and render it.
function render(status) {
  const stateEl = document.getElementById('state');
  const msgEl = document.getElementById('msg');
  const solveEl = document.getElementById('solve');
  if (!status || !status.connection) {
    stateEl.textContent = 'Not running';
    msgEl.textContent = 'Open Settings to configure the bridge.';
    return;
  }
  const c = status.connection;
  stateEl.textContent = c.state;
  stateEl.className = 'state ' + c.state;
  msgEl.textContent = c.message || '';
  if (status.lastSolve) {
    solveEl.textContent = `Last solve: ${status.lastSolve.state} — ${status.lastSolve.message} (${status.lastSolve.updated})`;
  } else {
    solveEl.textContent = 'No solves yet.';
  }
}

chrome.storage.local.get('status', (s) => render(s.status));
chrome.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.status) render(changes.status.newValue);
});
document.getElementById('options').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});
