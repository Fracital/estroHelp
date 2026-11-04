"""chat.z.ai browser transport (Patchright): the page's own send, rewritten in flight.

The page supplies what cannot be reproduced outside it: Cloudflare clearance,
the guest session and the single-use Aliyun captcha token. Each turn types a
placeholder and presses Enter; two routes then rewrite the page's requests:

  POST /api/v1/chats/new         stored history -> our prompt. The backend
                                 generates from this, not from the completions
                                 body (rewriting only that was ignored, live).
  POST /api/v2/chat/completions  messages, signature_prompt and x-signature
                                 (frontend HMAC) replaced; agent mode removed.

The reply is read from the raw SSE via CDP; the DOM drops tool-call markup.

Contract (one process serves many turns):
  stdin  : {"headless": false, "proxy": "socks5://...", "token"?} then one turn per line
           {"model", "prompt", "enable_thinking", "timeout", "stream"?}
  stdout : {"ready": true} once the page is loaded, then per turn zero or more
           {"delta": text} lines (stream turns only) and one result
           {"ok", "answer", "thinking", "usage"} or {"ok": false, "error", "status"?}
  stderr : "[zai_browser] event=..." lines
"""

import base64
import hashlib
import hmac
import json
import os
import sys
import time
from urllib.parse import urlparse, parse_qs

try:
    from patchright.sync_api import sync_playwright
except ImportError:
    sys.stderr.write('[zai_browser] event=error err="patchright is required"\n')
    raise SystemExit(2)

ZAI_HOME = "https://chat.z.ai/"
NEW_CHAT_GLOB = "**/api/v1/chats/new*"
COMPLETIONS_GLOB = "**/api/v2/chat/completions*"
COMPLETIONS_PATH = "/api/v2/chat/completions"
SIGN_KEY = b"key-@@@@)))()((9))-xxxx&&&%%%%%"
_COMPOSER = "textarea:visible, [contenteditable='true']:visible"
_NEW_CHAT_BUTTON = "#sidebar-new-chat-button"
_BLOCK_RESOURCE_TYPES = {"image", "media", "font"}
_BLOCK_URL_SUBSTRINGS = (
    "google-analytics", "googletagmanager", "doubleclick", "googlesyndication",
    "sdata.chatglm.cn", "rum.aliyuncs.com", "segment.io", "facebook",
    "reddit.com", "clarity.ms",
    # Render-blocking font CSS proxy; hung ~20s on some relays, every guest switch.
    "fonts.googleapis.com",
)
_NATIVE_TOOL_MARK = b'"phase":"tool_call"'


def _log(event, **fields):
    parts = " ".join(f"{k}={v}" for k, v in fields.items())
    sys.stderr.write(f"[zai_browser] pid={os.getpid()} event={event} {parts}\n")
    sys.stderr.flush()


def _has_complete_call(answer):
    """True when answer holds a whole {"name","arguments"} object. The native
    loop can cut a call off mid-arguments, and that must not pass as a reply."""
    dec = json.JSONDecoder(strict=False)  # GLM writes raw newlines inside long arguments
    i = answer.find('{"name"')
    while i >= 0:
        try:
            obj, _ = dec.raw_decode(answer, i)
            if isinstance(obj, dict) and obj.get("name") and "arguments" in obj:
                return True
        except ValueError:
            pass
        i = answer.find('{"name"', i + 1)
    return False


def _native_tool_names(sse):
    names = []
    for line in sse.splitlines():
        if '"delta_name"' not in line:
            continue
        try:
            n = (json.loads(line.strip()[5:]).get("data") or {}).get("delta_name")
        except (json.JSONDecodeError, AttributeError):
            continue
        if n and n not in names:
            names.append(n)
    return names


def retry_nudge(native_tools):
    """Appended to the prompt when the previous attempt fell into z.ai's own tools."""
    if os.environ.get("ZAI_RETRY_NUDGE", "1") == "0":
        return ""
    used = ", ".join(native_tools) or "a built-in tool"
    return (f"\n\nNOTE: your previous reply called {used}, which does not exist here and failed. "
            "Reply again using only the functions listed above.")


_CALL_MARKS = ('{"name"', "<function_call", "<tool_call")


class _AnswerStream:
    """Incrementally parses SSE answer text and decides what may be emitted live.

    Text is held until `hold` characters of prose exist, so a turn that goes on
    to a native loop (prose first, then a native call, ~1KB seen live) is still
    retried unseen. Once emitted, the turn is committed. The last `tail`
    characters are always held so a call marker split across chunks is caught.
    """

    def __init__(self, hold, tail=32):
        self.hold, self.tail = hold, tail
        self._pos, self.answer, self.sent, self.blocked = 0, "", 0, False
        self.thinking, self.thinking_sent = "", 0

    def feed(self, live):
        """Consume the complete lines of `live` (the whole raw stream so far)."""
        end = live.rfind(b"\n")
        if end < self._pos:
            return
        lines, self._pos = bytes(live[self._pos:end]).decode("utf-8", "replace").split("\n"), end + 1
        for line in lines:
            line = line.strip()
            if not line.startswith("data:"):
                continue
            try:
                d = (json.loads(line[5:].strip()).get("data") or {})
            except (json.JSONDecodeError, AttributeError):
                continue
            if d.get("phase") == "answer":
                self.answer += d.get("delta_content") or ""
            elif d.get("phase") == "thinking":
                self.thinking += d.get("delta_content") or ""

    def thinking_ready(self):
        """New thinking text since the last call. Thinking is never held back."""
        out, self.thinking_sent = self.thinking[self.thinking_sent:], len(self.thinking)
        return out

    def ready(self):
        """Text that may be emitted now, or ""."""
        if self.blocked:
            return ""
        if any(m in self.answer for m in _CALL_MARKS):
            self.blocked = True
            return ""
        text = self.answer.lstrip()
        if not self.sent and len(text) < self.hold:
            return ""
        end = max(self.sent, len(text) - self.tail)
        out, self.sent = text[self.sent:end], end
        return out


def _jwt_user(token):
    """The account id inside a z.ai JWT, or "" if it does not decode."""
    try:
        part = token.split(".")[1]
        return json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4))).get("id") or ""
    except (IndexError, ValueError, AttributeError):
        return ""


def _env_num(name, default):
    try:
        return float(os.environ.get(name, default))
    except ValueError:
        return default


def _redact(text, secret):
    return text.replace(secret, "<proxy>") if secret else text


def _sign(prompt, ts, request_id, user_id):
    pairs = sorted([f"requestId,{request_id}", f"timestamp,{ts}", f"user_id,{user_id}"])
    h = ",".join(pairs) + "|" + base64.b64encode(prompt.encode()).decode() + "|" + ts
    derived = hmac.new(SIGN_KEY, str(int(ts) // 300000).encode(), hashlib.sha256).hexdigest()
    return hmac.new(derived.encode(), h.encode(), hashlib.sha256).hexdigest()


def _parse_sse(text):
    answer, thinking, usage, err = [], [], {}, None
    for line in text.splitlines():
        line = line.strip()
        if not line.startswith("data:"):
            continue
        payload = line[5:].strip()
        if not payload or payload == "[DONE]":
            continue
        try:
            ev = json.loads(payload)
        except json.JSONDecodeError:
            continue
        d = ev.get("data") or {}
        e = d.get("error") or (d.get("data") or {}).get("error")
        if e:
            err = e
            break
        phase = d.get("phase")
        if phase == "answer":
            answer.append(d.get("delta_content") or "")
        elif phase == "thinking":
            thinking.append(d.get("delta_content") or "")
        elif phase == "other" and d.get("usage"):
            usage = d["usage"]
    return "".join(answer), "".join(thinking), usage, err


class ZaiBrowser:
    def __init__(self, headless=False, proxy=None, token=None):
        # Headed under xvfb: headless-shell fails the Aliyun/Cloudflare fingerprint.
        self.headless = headless
        self.proxy = proxy
        # A signed-in account's JWT (localStorage "token"). Unlocks glm-5.3/5.2;
        # without it the page mints a guest that only serves GLM-5.3-Flash.
        self.token = token or None
        self._pw = self._browser = self._page = self._cdp = None
        self._dump_path = None
        self._abandoned = 0
        self._native_tools = []
        self._emit = None  # set per turn to stream answer deltas
        self._reasoning_effort = None  # set per turn; the client's level when thinking is on
        self._committed = False
        self._reset_turn(None)

    def _reset_turn(self, pending):
        self._pending = pending
        self._rewritten = False
        self._rewrite_failed = None
        self._request = None
        self._response = None
        self._done = []
        self._stream_id = None
        self._live = bytearray()
        self._last_data = time.time()
        self._steps = []  # pre-send milestones, logged if the send never fires

    def __enter__(self):
        self._pw = sync_playwright().start()
        launch = {"headless": self.headless, "args": ["--disable-blink-features=AutomationControlled"]}
        if self.proxy:
            launch["proxy"] = {"server": self.proxy}
        self._browser = self._pw.chromium.launch(**launch)
        ctx = self._browser.new_context(
            user_agent="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36",
            locale="en-US", viewport={"width": 1280, "height": 800},
        )

        def _block(route):
            req = route.request
            if req.resource_type in _BLOCK_RESOURCE_TYPES or any(s in req.url for s in _BLOCK_URL_SUBSTRINGS):
                return route.abort()
            return route.fallback()
        ctx.route("**/*", _block)

        if self.token:
            # Runs before the page's own scripts, so its first auth call uses the account.
            # nux_tour_shown: signed-in users get an onboarding tour whose overlay blocks the composer.
            ctx.add_init_script("(() => { const t = %s; try { if (location.hostname !== 'chat.z.ai') return;"
                                " if (localStorage.getItem('token') !== t) localStorage.setItem('token', t);"
                                " localStorage.setItem('nux_tour_shown', '1'); localStorage.setItem('show_coding_plan_guide', 'false');"
                                " } catch (e) {} })()" % json.dumps(self.token))
        self._page = ctx.new_page()
        self._page.route(NEW_CHAT_GLOB, self._rewrite_new_chat)
        self._page.route(COMPLETIONS_GLOB, self._rewrite_completions)
        # Keyed on the request object we rewrote, so a late response from an
        # aborted attempt can never be read as this attempt's answer.
        self._page.on("response", lambda r: setattr(self, "_response", r) if r.request == self._request else None)
        self._page.on("request", self._note_step)
        # Response.text() only resolves at the end of the stream and has no
        # timeout; CDP gives live bytes, liveness and a request-keyed finish.
        self._cdp = ctx.new_cdp_session(self._page)
        self._cdp.send("Network.enable")
        self._cdp.on("Network.responseReceived", self._on_cdp_response)
        self._cdp.on("Network.dataReceived", self._on_cdp_data)
        self._cdp.on("Network.loadingFinished", lambda ev: self._on_cdp_end(ev, "finished"))
        self._cdp.on("Network.loadingFailed", lambda ev: self._on_cdp_end(ev, "failed: " + str(ev.get("errorText"))))
        self._goto_home()
        _log("ready")
        return self

    def __exit__(self, *exc):
        try:
            if self._browser:
                self._browser.close()
        finally:
            if self._pw:
                self._pw.stop()

    def _note_step(self, req):
        url = req.url
        for key, step in (("/api/v1/chats/new", "chats_new"), ("aliyunCaptcha", "captcha_js"),
                          ("-verify.captcha", "captcha_verify"), ("captcha_verify_success", "captcha_ok"),
                          ("captcha_verify_fail", "captcha_fail"), ("global_network_error", "net_error")):
            if key in url and step not in self._steps:
                self._steps.append(step)

    # -- request rewrites -------------------------------------------------

    def _rewrite_new_chat(self, route):
        req = route.request
        if req.method != "POST" or self._pending is None:
            return route.continue_()
        try:
            body = json.loads(req.post_data or "{}")
            chat = body["chat"]
            for msg in chat["history"]["messages"].values():
                if msg.get("role") == "user":
                    msg["content"] = self._pending["prompt"]
                    if self._pending["model"]:
                        msg["models"] = [self._pending["model"]]
            if self._pending["model"]:
                chat["models"] = [self._pending["model"]]
            chat["auto_web_search"] = False
        except Exception as err:
            # Never let the page's placeholder chat through unrewritten.
            self._rewrite_failed = f"chats/new rewrite: {err}"[:200]
            return route.abort()
        route.continue_(post_data=json.dumps(body))

    def _rewrite_completions(self, route):
        req = route.request
        if req.method != "POST" or self._pending is None:
            return route.continue_()
        try:
            body = json.loads(req.post_data or "{}")
            q = parse_qs(urlparse(req.url).query)
            ts, rid, uid = q["timestamp"][0], q["requestId"][0], q["user_id"][0]
            prompt, model = self._pending["prompt"], self._pending["model"]
            thinking = bool(self._pending["enable_thinking"])
            body["messages"] = [{"role": "user", "content": prompt}]
            body["signature_prompt"] = prompt
            if model:
                body["model"] = model
            feats = body.get("features") or {}
            feats.update(web_search=False, auto_web_search=False, enable_thinking=thinking)
            # The page asks for server-side agent mode at max effort. That mode
            # loops native WebSearch calls (8+ rounds live) and reasons at max
            # regardless of enable_thinking.
            feats["flags"] = [f for f in feats.get("flags") or [] if f != "general_agent"]
            # Without an explicit effort the model still reasons 20-37KB per
            # turn and enters its native tool loop (3/3, live); "low" answered
            # 11/12 in ~10s. "none" and "minimal" were no better than absent.
            # The client's level wins when thinking is on; otherwise the configured defaults.
            effort = self._pending.get("reasoning_effort")
            if thinking:
                feats["reasoning_effort"] = effort or os.environ.get("ZAI_REASONING_EFFORT", "medium")
            else:
                feats["reasoning_effort"] = os.environ.get("ZAI_REASONING_EFFORT_OFF", "low")
            body["features"] = feats
            headers = dict(req.headers)
            headers["x-signature"] = _sign(prompt, ts, rid, uid)
            post = json.dumps(body)
        except Exception as err:
            # The unrewritten request carries the placeholder prompt; abort it.
            self._rewrite_failed = f"completions rewrite: {err}"[:200]
            self._pending = None
            return route.abort()
        self._rewritten = True
        self._pending = None
        self._request = req
        self._dump(".body.json", post.encode())
        _log("rewrite", prompt_chars=len(prompt), model=body.get("model"))
        route.continue_(post_data=post, headers=headers)

    # -- CDP stream tracking ---------------------------------------------

    def _on_cdp_response(self, ev):
        if not self._rewritten or COMPLETIONS_PATH not in ev.get("response", {}).get("url", ""):
            return
        if self._stream_id is not None:
            # Only the rewritten request belongs to this turn; a later page
            # retry goes out unrewritten and must not replace its stream.
            return
        self._stream_id = ev.get("requestId")
        self._last_data = time.time()
        try:
            r = self._cdp.send("Network.streamResourceContent", {"requestId": self._stream_id})
            self._live += base64.b64decode(r.get("bufferedData") or "")
        except Exception as err:
            _log("stream_capture_unavailable", err=str(err)[:120])

    def _on_cdp_data(self, ev):
        if self._stream_id and ev.get("requestId") == self._stream_id:
            self._last_data = time.time()
            if ev.get("data"):
                self._live += base64.b64decode(ev["data"])

    def _on_cdp_end(self, ev, outcome):
        # Keyed on the request id, so a late failure from an aborted attempt
        # never settles the retry.
        if self._stream_id and ev.get("requestId") == self._stream_id:
            self._done.append(outcome)

    def _dump(self, suffix, data):
        if not (self._dump_path and data):
            return
        try:
            with open(self._dump_path + suffix, "wb") as f:
                f.write(data)
        except OSError as err:
            _log("dump_error", err=str(err)[:120])

    # -- turn ---------------------------------------------------------------

    def _new_guest(self):
        """Switch to a fresh guest identity.

        A guest may run 3 generations at once, and an abandoned one keeps its
        slot until it finishes server-side (minutes for a native tool loop).
        Deleting the chat or POSTing /api/tasks/stop does not free the slot
        (both measured live); a new guest starts with all 3 free.
        """
        self._abandoned = 0
        if self.token:
            # A signed-in account cannot be swapped; clearing storage would log it out.
            return
        self._page.context.clear_cookies()
        self._page.evaluate("() => { localStorage.clear(); sessionStorage.clear(); }")
        self._goto_home()
        _log("new_guest")

    def _goto_home(self):
        """Load the home page and wait until it can send. The composer renders
        before the page has minted its guest token; an Enter pressed in that
        window is silently dropped (every send right after a guest switch
        failed this way, live)."""
        # The send handler needs the model list; Enter before it loads does nothing.
        try:
            with self._page.expect_response(lambda r: "/api/models" in r.url, timeout=30000):
                self._page.goto(ZAI_HOME, wait_until="domcontentloaded", timeout=45000)
        except Exception as err:
            if "Timeout" not in type(err).__name__:
                raise
            _log("models_not_loaded")
        self._page.wait_for_selector(_COMPOSER, timeout=30000)
        try:
            self._page.wait_for_function("() => !!localStorage.getItem('token')", timeout=15000)
        except Exception:
            _log("guest_token_missing")

    def _new_chat(self):
        # The sidebar button starts a fresh chat without a reload: ~0.1s
        # versus ~1.5s (direct) to 15s (cold relay) for a navigation.
        try:
            if "/c/" in self._page.url:
                self._page.locator(_NEW_CHAT_BUTTON).first.click(timeout=5000)
                self._page.wait_for_url(lambda u: "/c/" not in u, timeout=5000)
            self._page.wait_for_selector(_COMPOSER, timeout=5000)
        except Exception:
            self._goto_home()

    def turn(self, prompt, model="", enable_thinking=True, timeout=600, emit=None, reasoning_effort=None):
        if not prompt.strip():
            return {"ok": False, "error": "empty prompt"}
        self._emit, self._committed = emit, False
        self._reasoning_effort = reasoning_effort
        deadline = time.time() + timeout
        retries = max(0, int(_env_num("ZAI_STALL_RETRIES", 5)))
        attempt = guests = 0
        sent = prompt
        while True:
            # Each abandoned generation still holds one of the guest's 3 slots;
            # move on before the next send would hit the limit.
            if self._abandoned >= 2:
                self._new_guest()
            res = self._attempt(sent, model, enable_thinking, deadline)
            stalled, busy = res.pop("stalled", False), res.pop("busy", False)
            native = res.pop("native", False)
            partial = res.pop("partial", None)
            if self._committed:
                # Text already reached the client; a retry would duplicate it.
                if native and partial is not None:
                    return {"ok": True, "answer": partial, "thinking": "", "usage": {}}
                return res
            sent = prompt + retry_nudge(self._native_tools) if native else prompt
            if time.time() >= deadline:
                return res
            if busy and guests < 3 and not self.token:
                guests += 1
                self._new_guest()
                continue
            if not stalled or attempt >= retries:
                return res
            attempt += 1
            _log("stall_retry", attempt=attempt)

    def _attempt(self, prompt, model, enable_thinking, deadline):
        dump_dir = os.environ.get("ZAI_DUMP_DIR")
        self._dump_path = os.path.join(dump_dir, f"turn-{int(time.time() * 1000)}") if dump_dir else None
        if self._dump_path:
            self._dump(".prompt.txt", prompt.encode())
        # Reset first: a chats/new fired while switching chats must not see the last turn's prompt.
        self._reset_turn(None)
        self._new_chat()
        self._reset_turn({"prompt": prompt, "model": model, "enable_thinking": enable_thinking, "reasoning_effort": self._reasoning_effort})
        box = self._page.locator(_COMPOSER).first
        # A product tour (driver.js) can still cover the page; Escape closes it.
        if self._page.locator(".driver-overlay").count():
            self._page.keyboard.press("Escape")
            self._page.evaluate("() => document.querySelectorAll('.driver-overlay, .driver-popover').forEach(e => e.remove())")
        box.click()
        box.fill("placeholder")
        box.press("Enter")

        # Poll through Playwright so route and CDP callbacks run on this thread.
        # The page sends completions once its captcha SDK mints a token (1-10s);
        # on a cold page it sometimes never does, and only a reload recovers.
        send_deadline = min(deadline, time.time() + _env_num("ZAI_SEND_TIMEOUT", 25))
        repress_at = time.time() + 5
        while time.time() < send_deadline and not self._rewritten and not self._rewrite_failed:
            self._page.wait_for_timeout(100)
            # No chats/new at all means the Enter was dropped, not a slow captcha.
            if repress_at and time.time() >= repress_at and not self._steps:
                repress_at = 0
                _log("send_repress")
                box.click()
                box.press("Enter")
        if self._rewrite_failed:
            self._pending = None
            return {"ok": False, "error": self._rewrite_failed}
        if not self._rewritten:
            self._pending = None
            _log("send_not_fired", seen=",".join(self._steps) or "none", url=self._page.url[-40:])
            if "net_error" in self._steps and "captcha_ok" not in self._steps:
                # The captcha hosts fail from this exit (18 in a row on one relay,
                # live); retrying here cannot help, so let Go move the relay.
                return {"ok": False, "relay_blocked": True, "error": "captcha network error from this exit IP"}
            self._goto_home()
            return {"ok": False, "stalled": True, "error": "completion request never fired (captcha or send failed)"}
        while time.time() < deadline and self._response is None:
            self._page.wait_for_timeout(100)
        self._pending = None
        if self._response is None:
            self._abandoned += 1  # the request went out and may still hold a slot
            return {"ok": False, "error": "timed out waiting for completion response"}
        resp = self._response

        stall = _env_num("ZAI_STALL_SECONDS", 75)
        scanned, next_progress = 0, time.time() + 15
        stream = _AnswerStream(int(_env_num("ZAI_STREAM_HOLD", 2000))) if self._emit else None
        while time.time() < deadline and not self._done:
            self._page.wait_for_timeout(100)
            if stream:
                stream.feed(self._live)
                thought = stream.thinking_ready()
                if thought:
                    self._emit(thought, "think")
            if len(self._live) > scanned:
                # Rescan only the new bytes, plus enough overlap for a mark split across chunks.
                found = self._live.find(_NATIVE_TOOL_MARK, max(0, scanned - len(_NATIVE_TOOL_MARK))) >= 0
                scanned = len(self._live)
                # GLM sometimes enters z.ai's own WebSearch/WebFetch loop
                # (~1 in 3 large agent turns, live) and never finishes.
                if found:
                    live = bytes(self._live)
                    self._dump(".native.sse.txt", live)
                    sse = live.decode("utf-8", "replace")
                    answer, thinking, usage, _ = _parse_sse(sse)
                    self._native_tools = _native_tool_names(sse)
                    _log("native_tool_loop", answer_chars=len(answer), tools=",".join(self._native_tools))
                    # Either way the loop keeps running server-side and holds a slot.
                    self._abandoned += 1
                    # Keep the answer only if it already holds a call of ours. Prose
                    # alone ("let me verify...") means the model meant to call a tool
                    # and reached for its own; returning it would end an agent's turn.
                    if _has_complete_call(answer):
                        return {"ok": True, "answer": answer, "thinking": thinking, "usage": usage}
                    return {"ok": False, "stalled": True, "native": True, "partial": answer,
                            "error": "upstream entered its native tool loop"}
                if stream:
                    delta = stream.ready()
                    if delta:
                        self._committed = True
                        self._emit(delta, "delta")
            silent = time.time() - self._last_data
            if silent > stall:
                self._dump(".partial.sse.txt", bytes(self._live))
                _log("stalled", silent_s=int(silent))
                self._abandoned += 1
                return {"ok": False, "stalled": True, "error": f"completion stream silent for {int(silent)}s"}
            if time.time() >= next_progress:
                next_progress += 15
                _log("waiting", stream_bytes=len(self._live), silent_s=int(silent))
        if not self._done:
            self._dump(".partial.sse.txt", bytes(self._live))
            self._abandoned += 1  # the generation keeps running and holds a guest slot
            return {"ok": False, "error": "completion stream did not finish within the turn deadline"}
        if self._done[0] != "finished":
            return {"ok": False, "error": f"completion request {self._done[0]}"[:300]}
        try:
            text = resp.text()
        except Exception as err:
            return {"ok": False, "error": f"reading completion stream: {err}"[:300]}
        self._dump(".sse.txt", text.encode())
        # A flagged client gets an HTML block page from Aliyun's WAF (HTTP 405, live).
        if resp.status != 200 or "data:" not in text:
            kind = "blocked by upstream WAF" if "<html" in text[:200].lower() else "non-SSE reply"
            return {"ok": False, "status": resp.status, "error": f"upstream HTTP {resp.status}: {kind}"}
        answer, thinking, usage, err = _parse_sse(text)
        if err:
            busy = isinstance(err, dict) and err.get("code") == 429
            return {"ok": False, "busy": busy, "status": 429 if busy else None,
                    "error": "upstream: " + json.dumps(err)[:300]}
        _log("turn_done", answer_chars=len(answer), thinking_chars=len(thinking))
        return {"ok": True, "answer": answer, "thinking": thinking, "usage": usage}


def main():
    line = sys.stdin.readline()
    try:
        session = json.loads(line) if line.strip() else {}
    except json.JSONDecodeError:
        session = {}
    proxy = session.get("proxy") or ""
    try:
        with ZaiBrowser(headless=session.get("headless") is True, proxy=proxy or None, token=session.get("token")) as zb:
            # The page reissues the JWT for the same account; a guest one means the token was rejected.
            if zb.token and _jwt_user(zb._page.evaluate("() => localStorage.getItem('token')") or "") != _jwt_user(zb.token):
                raise RuntimeError("z.ai rejected the signed-in token (expired or logged out); refresh zai_config.json")
            sys.stdout.write(json.dumps({"ready": True}) + "\n")
            sys.stdout.flush()
            for line in sys.stdin:
                if not line.strip():
                    continue
                try:
                    t = json.loads(line)

                    def emit(text, kind="delta"):
                        sys.stdout.write(json.dumps({kind: text}) + "\n")
                        sys.stdout.flush()
                    res = zb.turn(t.get("prompt", ""), t.get("model", ""), t.get("enable_thinking", True),
                                  int(t.get("timeout", 600)), emit if t.get("stream") else None,
                                  reasoning_effort=t.get("reasoning_effort"))
                except Exception as err:
                    res = {"ok": False, "error": _redact(str(err), proxy)[:300]}
                sys.stdout.write(json.dumps(res) + "\n")
                sys.stdout.flush()
                # A closed page or crashed browser would fail every later turn;
                # exit so the Go pool respawns a fresh process.
                if zb._page.is_closed() or not zb._browser.is_connected():
                    _log("browser_lost")
                    return 1
        return 0
    except Exception as err:
        _log("fatal", err=_redact(str(err), proxy)[:200])
        sys.stdout.write(json.dumps({"ok": False, "error": _redact(str(err), proxy)[:300]}) + "\n")
        sys.stdout.flush()
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
