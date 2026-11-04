# goldenticket z.ai bridge (browser extension)

Mints chat.z.ai Aliyun captcha tokens in your real browser so the goldenticket
proxy can call chat.z.ai without solving the captcha itself.

## Why this exists

chat.z.ai gates every completion behind an Aliyun captcha (`captcha_verify_param`).
The token is minted client-side by Aliyun's captcha SDK, which **refuses to boot
under browser automation** (Patchright/headless Chromium): the script loads but
`initAliyunCaptcha` never attaches. The SDK runs fine in a real, trusted browser
— so this extension drives the widget there and hands the token to the proxy.

## Install

1. Open `chrome://extensions` (or `brave://extensions`, `edge://extensions`).
2. Enable **Developer mode**.
3. **Load unpacked** → select this `ext/zai-extension` directory.
4. Open the extension's **Settings** (options page):
   - **Server URL** — your goldenticket base URL, e.g. `http://localhost:8799`.
   - **Extension token** — must match `ZAI_EXT_TOKEN` on the proxy.
5. Keep a chat.z.ai tab available. The extension opens a background tab to
   `https://chat.z.ai/` if none is open when a solve is requested.

## How it works

- The background service worker holds a websocket to `<server>/zai-ext/ws`.
- When the proxy needs a token it sends `{type:"solve", id}`.
- The worker asks the content script on a chat.z.ai tab; the content script
  relays to a MAIN-world helper (`zai-page.js`) that drives the Aliyun widget.
- On a clean fingerprint the widget auto-passes (no puzzle) and returns a fresh
  base64 `captcha_verify_param`, which flows back to the proxy.

The toolbar badge shows the connection state (ON / ? / !); the popup shows the
last solve result.

## Server side

Only the `ZAI_TRANSPORT=browserless` transport uses the extension; the default
patchright transport lets its own browser mint the captcha. Set `ZAI_EXT_TOKEN`
to a shared secret. The proxy prefers the extension, then a pasted
`ZAI_CAPTCHA_PARAM`. See `zai_ext.go` and `zai.go`.
