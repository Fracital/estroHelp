// goldenticket z.ai bridge — MAIN-world page helper.
//
// Injected by content.js into chat.z.ai's page world, where the Aliyun captcha
// SDK's initAliyunCaptcha actually lives. Drives the widget and posts the result
// back to the content script over window.postMessage.
//
// The widget config mirrors the live frontend (prod-fe-1.1.123): region sgp,
// prefix no8xfe, SceneId didk33e0, mode popup, hidden element + trigger button.
// On a clean fingerprint the widget auto-passes (success fires with a fresh
// certifyId, no puzzle). If it escalates to a slider, we surface that as an
// error — the operator's real browser passes clean, so escalation means the
// browser profile itself is flagged.

(function () {
  const SCENE_ID = 'didk33e0';
  const REGION = 'sgp';
  const PREFIX = 'no8xfe';
  const SCRIPT = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js';
  const EL_ID = 'gt-zai-cap-el';
  const BTN_ID = 'gt-zai-cap-btn';

  let loadPromise = null;
  let instance = null;

  function ensureSdk() {
    if (loadPromise) return loadPromise;
    loadPromise = new Promise((resolve, reject) => {
      if (typeof window.initAliyunCaptcha === 'function') return resolve();
      window.AliyunCaptchaConfig = { region: REGION, prefix: PREFIX };
      const s = document.createElement('script');
      s.src = SCRIPT;
      s.onload = () => {
        // The SDK attaches initAliyunCaptcha asynchronously after load.
        let n = 0;
        const t = setInterval(() => {
          if (typeof window.initAliyunCaptcha === 'function') { clearInterval(t); resolve(); }
          else if (++n > 60) { clearInterval(t); reject(new Error('initAliyunCaptcha never attached')); }
        }, 100);
      };
      s.onerror = () => reject(new Error('aliyun captcha script failed to load'));
      document.head.appendChild(s);
    });
    return loadPromise;
  }

  function ensureElements() {
    if (!document.getElementById(EL_ID)) {
      const el = document.createElement('div');
      el.id = EL_ID;
      el.style.cssText = 'position:absolute;left:-99999px;top:-99999px;width:0;height:0;overflow:hidden;pointer-events:none;';
      document.body.appendChild(el);
    }
    if (!document.getElementById(BTN_ID)) {
      const btn = document.createElement('button');
      btn.id = BTN_ID;
      btn.type = 'button';
      btn.style.cssText = 'position:absolute;left:-99999px;top:-99999px;width:1px;height:1px;opacity:0;';
      document.body.appendChild(btn);
    }
  }

  function encodeParam(payload) {
    // The success callback hands back {certifyId, ...}; wrap it with the scene
    // id + security token the way the frontend does before base64-encoding.
    let obj = payload;
    if (typeof payload === 'string') {
      // Some SDK versions hand back the verify-param string directly.
      try { JSON.parse(atob(payload)); return payload; } catch (e) { /* fall through */ }
      return btoa(payload);
    }
    obj = {
      certifyId: payload.certifyId || payload.certify_id || '',
      sceneId: SCENE_ID,
      isSign: true,
      securityToken: payload.securityToken || payload.security_token || '',
    };
    if (payload.verifyToken) obj.verifyToken = payload.verifyToken;
    // base64 of UTF-8 JSON.
    return btoa(unescape(encodeURIComponent(JSON.stringify(obj))));
  }

  async function solve(id) {
    try {
      await ensureSdk();
      ensureElements();
      const result = await new Promise((resolve, reject) => {
        let settled = false;
        const finish = (fn) => (v) => { if (!settled) { settled = true; fn(v); } };
        const ok = finish(resolve);
        const bad = finish(reject);
        window.initAliyunCaptcha({
          SceneId: SCENE_ID,
          mode: 'popup',
          element: '#' + EL_ID,
          button: '#' + BTN_ID,
          language: 'en',
          timeout: 15000,
          delayBeforeSuccess: false,
          success: (p) => ok(p),
          fail: (e) => bad(new Error('captcha fail: ' + safeJson(e))),
          onError: (e) => bad(new Error('captcha error: ' + safeJson(e))),
          onClose: () => bad(new Error('captcha closed')),
          getInstance: (inst) => { instance = inst; },
        });
        // Kick the hidden trigger after the widget initializes.
        setTimeout(() => { const b = document.getElementById(BTN_ID); if (b) b.click(); }, 300);
        setTimeout(() => bad(new Error('captcha solve timed out')), 20000);
      });
      const token = encodeParam(result);
      window.postMessage({ gt: 'zai-token', id, token }, location.origin);
    } catch (err) {
      window.postMessage({ gt: 'zai-error', id, error: String(err && err.message || err) }, location.origin);
    }
  }

  function safeJson(v) {
    try { return JSON.stringify(v); } catch (e) { return String(v); }
  }

  window.addEventListener('message', (event) => {
    if (event.source !== window || !event.data) return;
    if (event.data.gt === 'zai-solve') solve(event.data.id);
  });
})();
