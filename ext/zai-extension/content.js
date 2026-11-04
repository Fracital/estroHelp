// goldenticket z.ai bridge — content script (isolated world).
//
// The Aliyun captcha SDK attaches initAliyunCaptcha to the page's MAIN-world
// window, which this isolated-world content script cannot reach directly. So we
// inject a small MAIN-world helper (zai-page.js) and talk to it over
// window.postMessage. The helper drives the widget; we relay the result back to
// the background worker.
//
// Message protocol with the page helper:
//   -> { gt: 'zai-solve', id }        ask for a fresh captcha token
//   <- { gt: 'zai-token', id, token } success
//   <- { gt: 'zai-error', id, error } failure

(function () {
  // Inject the MAIN-world helper once.
  const script = document.createElement('script');
  script.src = chrome.runtime.getURL('zai-page.js');
  script.onload = () => script.remove();
  (document.head || document.documentElement).appendChild(script);

  // Relay solve requests from the background worker to the page helper, then
  // relay the page's answer back. sendResponse is async, so return true.
  chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
    if (!msg || msg.gt !== 'zai-solve') return false;

    const onMessage = (event) => {
      if (event.source !== window || !event.data || event.data.id !== msg.id) return;
      if (event.data.gt === 'zai-token') {
        window.removeEventListener('message', onMessage);
        sendResponse({ token: event.data.token });
      } else if (event.data.gt === 'zai-error') {
        window.removeEventListener('message', onMessage);
        sendResponse({ error: event.data.error });
      }
    };
    window.addEventListener('message', onMessage);
    window.postMessage({ gt: 'zai-solve', id: msg.id }, location.origin);
    return true; // keep the message channel open for the async sendResponse
  });
})();
