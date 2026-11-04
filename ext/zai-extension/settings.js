// Settings page: persist the server URL + token and trigger a reconnect.
const serverUrl = document.getElementById('serverUrl');
const token = document.getElementById('token');
const save = document.getElementById('save');
const saved = document.getElementById('saved');

chrome.storage.local.get(['serverUrl', 'token'], (s) => {
  if (s.serverUrl) serverUrl.value = s.serverUrl;
  if (s.token) token.value = s.token;
});

save.addEventListener('click', async () => {
  await chrome.storage.local.set({ serverUrl: serverUrl.value.trim(), token: token.value.trim() });
  saved.textContent = 'Saved.';
  setTimeout(() => { saved.textContent = ''; }, 2000);
});
