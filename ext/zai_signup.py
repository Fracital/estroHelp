"""Automated chat.z.ai account signup (Patchright, headed under xvfb).

Creates a disposable mailbox, drives the real signup form, solves the Aliyun
slider puzzle locally (the gap is the piece's own content drawn in place, so a
masked normalized cross-correlation of the piece against the background finds
it), follows the verification link, completes finish_signup and reports the
account's session JWT. Contract mirrors ext/zai_browser.py:

  stdin  : {"proxy"?: "socks5://...", "timeout"?: 300, "headless"?: false, "mail"?: "mailtm"}
  stdout : {"ok": true, "email", "password", "token", "user_id", "name", "mail_provider"}
           {"ok": false, "stage": "...", "error": "..."}
  stderr : "[zai_signup] event=k=v" diagnostics (never the token)
  exit   : 0 ok, 1 retryable failure, 2 dependency missing
"""
import base64, hashlib, html, io, json, os, random, re, secrets, sys, time, urllib.error, urllib.request
from urllib.parse import quote

try:
    from patchright.sync_api import sync_playwright
    import numpy as np
    from PIL import Image
except ImportError as err:
    sys.stderr.write('[zai_signup] event=error err="missing dependency: %s (pip install patchright numpy pillow)"\n' % err)
    raise SystemExit(2)

SIGNUP_URL = "https://chat.z.ai/auth?action=signup"
RUN_ID = "%d" % int(time.time())
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
MAILTM = "https://api.mail.tm"
_BLOCK = ("google-analytics", "googletagmanager", "doubleclick", "googlesyndication", "sdata.chatglm.cn",
          "rum.aliyuncs.com", "segment.io", "facebook", "clarity.ms", "fonts.googleapis.com")


def _log(event, **fields):
    line = "[zai_signup] event=" + event
    if fields:
        line += " " + " ".join("%s=%s" % (k, v) for k, v in fields.items())
    sys.stderr.write(line + "\n"); sys.stderr.flush()


class Fail(Exception):
    def __init__(self, stage, msg):
        super().__init__(msg); self.stage = stage


# ---------------------------------------------------------------- mailbox

try:
    from curl_cffi import requests as cffi_requests
except ImportError:
    cffi_requests = None


class HTTPStatus(Exception):
    def __init__(self, status, body):
        super().__init__("HTTP %d: %s" % (status, body[:120])); self.status = status


def _http(method, url, body=None, headers=None, timeout=30, raw=False):
    data = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json", "User-Agent": UA}
    if data is not None:
        h["Content-Type"] = "application/json"
    h.update(headers or {})
    if cffi_requests is not None:
        r = cffi_requests.request(method, url, data=data, headers=h, timeout=timeout, impersonate="chrome")
        text = r.text
        if r.status_code >= 400:
            raise HTTPStatus(r.status_code, text)
    else:
        req = urllib.request.Request(url, data=data, headers=h, method=method)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                text = r.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as err:
            raise HTTPStatus(err.code, err.read().decode("utf-8", "replace")[:200])
    if raw:
        return text
    return json.loads(text) if text.strip() else {}


def _members(v):
    return v if isinstance(v, list) else (v.get("hydra:member") or v.get("members") or [])


class MailTM:
    name = "mailtm"

    def __init__(self):
        doms = [d["domain"] for d in _members(_http("GET", MAILTM + "/domains")) if d.get("isActive", True)]
        if not doms:
            raise Fail("mailbox", "mail.tm has no active domains")
        self.address = "estro" + secrets.token_hex(5) + "@" + random.choice(doms)
        self.password = "Mb!" + secrets.token_hex(8)
        _http("POST", MAILTM + "/accounts", {"address": self.address, "password": self.password})
        self.token = _http("POST", MAILTM + "/token", {"address": self.address, "password": self.password})["token"]

    def wait_link(self, deadline):
        auth = {"Authorization": "Bearer " + self.token}
        seen = set()
        while time.time() < deadline:
            try:
                for m in _members(_http("GET", MAILTM + "/messages", headers=auth)):
                    if m["id"] in seen:
                        continue
                    seen.add(m["id"])
                    d = _http("GET", MAILTM + "/messages/" + m["id"], headers=auth)
                    body = (d.get("text") or "") + "\n" + " ".join(d.get("html") or [])
                    _log("mail", subject=json.dumps((m.get("subject") or "")[:60]))
                    link = _find_link(body)
                    if link:
                        return link
            except Exception as err:
                _log("mail_poll_error", err=str(err)[:120])
            time.sleep(4)
        return None


LINK_RE = r"https?://[^\s\"'<>]*z\.ai[^\s\"'<>]*verify_email[^\s\"'<>]*"
LINK_FALLBACK_RE = r"https?://[^\s\"'<>]*verify[^\s\"'<>]*"


def _find_link(text):
    text = html.unescape(text or "")
    for pat in (LINK_RE, LINK_FALLBACK_RE):
        m = re.findall(pat, text)
        if m:
            return m[0].replace("&amp;", "&")
    return None


class EmailMux:
    """emailmux.com: real-looking @gmail.com / @outlook.com aliases, signed polling, no token."""
    name = "emailmux"
    BASE = "https://emailmux.com"
    SECRET = "yjd683c@47"

    def __init__(self):
        last = None
        for fam in ("gmail", "googlemail", "outlook", "hotmail"):
            try:
                d = _http("POST", self.BASE + "/generate-email", {"domains": [fam]})
            except Exception as err:
                last = err; continue
            addr = (d.get("email") or "").strip().lower()
            if d.get("status") == "success" and addr and "+" not in addr:
                self.address = addr
                break
            last = Exception("generate-email %s: %s" % (fam, json.dumps(d)[:120]))
        else:
            raise Fail("mailbox", "emailmux: %s" % last)
        _http("GET", self.BASE + "/use-email?email=" + quote(self.address), headers=self._signed())

    def _signed(self):
        ts = str(int(time.time() * 1000))
        return {"Accept": "application/json, text/plain, */*", "X-API-Timestamp": ts,
                "X-API-Signature": hashlib.md5((self.SECRET + self.address + ts).encode()).hexdigest()}

    def wait_link(self, deadline):
        seen = set()
        interval = 10
        while time.time() < deadline:
            try:
                data = _http("GET", self.BASE + "/emails?email=" + quote(self.address), headers=self._signed())
                interval = 10
                if isinstance(data, list):
                    for m in data:
                        uid = m.get("uuid")
                        if not uid or uid in seen:
                            continue
                        seen.add(uid)
                        _log("mail", provider=self.name, frm=json.dumps(str(m.get("sender"))[:40]), subject=json.dumps(str(m.get("subject"))[:60]))
                        page = _http("GET", self.BASE + "/en/email/" + uid, headers={"Accept": "text/html"}, raw=True)
                        body = page
                        mm = re.search(r'<script[^>]*id="email-html-data"[^>]*>(.*?)</script>', page, re.S)
                        if mm:
                            try:
                                body = json.loads(mm.group(1).strip()) + "\n" + page
                            except Exception:
                                pass
                        link = _find_link(body)
                        if link:
                            return link
                else:
                    _log("mail_poll_auth", provider=self.name, msg=json.dumps(str(data))[:80])
            except HTTPStatus as err:
                interval = min(60, interval * 2) if err.status == 429 else interval
                _log("mail_poll_error", provider=self.name, err=str(err)[:120], next_in=interval)
            except Exception as err:
                _log("mail_poll_error", provider=self.name, err=str(err)[:120])
            time.sleep(interval)
        return None


class TempTF:
    """temp.tf: @gmail.com / @outlook.com aliases, no credentials, bodies inline."""
    name = "temptf"
    BASE = "https://temp.tf"

    def __init__(self):
        d = _http("GET", self.BASE + "/api/account?providers=gmail,outlook,hotmail&dot=1&plus=0")
        addr = (d.get("email") or "").strip().lower()
        if not addr or "@" not in addr:
            raise Fail("mailbox", "temp.tf: %s" % json.dumps(d)[:120])
        self.address = addr

    def wait_link(self, deadline):
        seen = set()
        while time.time() < deadline:
            try:
                d = _http("POST", self.BASE + "/api/check", {"email": self.address, "wait": False})
                msgs = d if isinstance(d, list) else (d.get("data") or [])
                for m in msgs if isinstance(msgs, list) else []:
                    mid = str(m.get("id") or m.get("subject"))
                    if mid in seen:
                        continue
                    seen.add(mid)
                    _log("mail", provider=self.name, frm=json.dumps(str(m.get("from"))[:40]), subject=json.dumps(str(m.get("subject"))[:60]))
                    link = _find_link(str(m.get("content") or "") + "\n" + str(m.get("subject") or ""))
                    if link:
                        return link
            except Exception as err:
                _log("mail_poll_error", provider=self.name, err=str(err)[:120])
            time.sleep(4)
        return None


class SmailPro:
    """smailpro.com premium (SMAILPRO_TOKEN): real Gmail/Googlemail/Outlook mailboxes, read
    through the site's own session in the browser context. Ported from goldenticket."""
    name = "smailpro"
    needs_context = True

    def __init__(self, context):
        sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
        from smailpro_browser import SmailproSession
        token = _smailpro_token()
        if not token:
            raise Fail("mailbox", "SMAILPRO_TOKEN is not set")
        families = [f.strip() for f in (os.environ.get("SMAILPRO_FAMILIES") or "gmail,googlemail,outlook").split(",") if f.strip()]
        self.session = SmailproSession.create(context, families, [], token=token, mailbox_type=os.environ.get("SMAILPRO_TYPE") or "alias")
        self.address = self.session.mailbox["address"]
        # The site rotates its XSRF cookie; keep the fresh one for the next signup.
        try:
            fresh = self.session.page.evaluate("() => { const c = document.cookie.split(';').find(c => c.trim().startsWith('XSRF-TOKEN=')); return c ? c.trim() : ''; }")
            if fresh and fresh != token:
                with open(os.environ.get("SMAILPRO_SESSION_FILE", "smailpro_session.json"), "w") as f:
                    json.dump({"token": fresh}, f)
        except Exception:
            pass

    def wait_link(self, deadline):
        from smailpro_browser import FAMILIES, SmailproHTTPError
        seen = set()
        while time.time() < deadline:
            try:
                try:
                    messages = self.session._list_messages()
                except SmailproHTTPError as err:
                    if err.status != 404:
                        raise
                    messages = []
                for m in messages:
                    mid = str(m.get("mid") or "")
                    if not mid or mid in seen:
                        continue
                    seen.add(mid)
                    _log("mail", provider=self.name, frm=json.dumps(str(m.get("textFrom") or m.get("from"))[:40]), subject=json.dumps(str(m.get("textSubject") or m.get("subject"))[:60]))
                    payload = self.session._fetch(self.session.page, "/app/message", {
                        "method": "GET", "query": {"email": self.address, "mid": mid},
                        "headers": {"Content-Type": "application/json"}, "credentials": "include", "response": "text"})
                    backend = FAMILIES[self.session.mailbox["family"]]["backend"]
                    detail = self.session._fetch(self.session.page, "https://api.sonjj.com/v1/%s/message" % backend, {
                        "method": "GET", "query": {"payload": payload}, "response": "json"})
                    link = _find_link(str(m.get("textSubject", "")) + "\n" + str((detail or {}).get("body", "")))
                    if link:
                        return link
            except Exception as err:
                _log("mail_poll_error", provider=self.name, err=str(err)[:120])
            time.sleep(8)
        return None


def _smailpro_token():
    path = os.environ.get("SMAILPRO_SESSION_FILE", "smailpro_session.json")
    try:
        with open(path) as f:
            tok = (json.load(f).get("token") or "").strip()
            if tok:
                return tok
    except Exception:
        pass
    return (os.environ.get("SMAILPRO_TOKEN") or "").strip()


class Emailnator:
    """premium.emailnator.com (EMAILNATOR_TOKEN): private Gmail dot/plus and Googlemail
    aliases through the dashboard's REST API. Ported from goldenticket's mailbox_emailnator.go."""
    name = "emailnator"
    BASE = "https://api.premium.emailnator.com/api"

    def __init__(self):
        self.token = (os.environ.get("EMAILNATOR_TOKEN") or "").strip()
        if not self.token:
            raise Fail("mailbox", "EMAILNATOR_TOKEN is not set")
        types = [t.strip() for t in (os.environ.get("EMAILNATOR_TYPES") or "private_gmail_dot,private_googlemail,public_gmail_dot,public_googlemail").split(",") if t.strip()]
        last = None
        for t in types:
            try:
                d = _http("POST", self.BASE + "/emails/generate", {"type": t}, headers=self._headers())
            except Exception as err:
                last = err; continue
            addr = (d.get("email") or "").strip().lower()
            if addr and "@" in addr:
                self.address = addr; self.type = t
                return
            last = Exception("generate %s: %s" % (t, json.dumps(d)[:120]))
        raise Fail("mailbox", "emailnator: %s" % last)

    def _headers(self):
        return {"Authorization": "Bearer " + self.token, "Origin": "https://premium.emailnator.com",
                "Referer": "https://premium.emailnator.com/", "Accept": "application/json, text/plain, */*"}

    def wait_link(self, deadline):
        seen = set()
        while time.time() < deadline:
            try:
                d = _http("POST", self.BASE + "/inbox", {"email": self.address, "limit": 20}, headers=self._headers())
                for m in d.get("messages") or []:
                    mid = str(m.get("id") or "")
                    key = mid or (str(m.get("from")) + "|" + str(m.get("subject")))
                    if key in seen:
                        continue
                    seen.add(key)
                    _log("mail", provider=self.name, frm=json.dumps(str(m.get("from"))[:40]), subject=json.dumps(str(m.get("subject"))[:60]))
                    if not mid:
                        continue
                    raw = _http("GET", self.BASE + "/inbox/" + mid, headers=self._headers(), raw=True)
                    body = raw
                    try:
                        j = json.loads(raw)
                        body = json.dumps(j.get("content", j)) if isinstance(j, dict) else raw
                        body = json.loads(body) if body.startswith('"') else body
                    except Exception:
                        pass
                    link = _find_link(str(body)) or _find_link(raw)
                    if link:
                        return link
            except HTTPStatus as err:
                if err.status == 404:
                    pass  # inbox not materialised yet
                else:
                    _log("mail_poll_error", provider=self.name, err=str(err)[:120])
            except Exception as err:
                _log("mail_poll_error", provider=self.name, err=str(err)[:120])
            time.sleep(6)
        return None


MAIL_PROVIDERS = {"emailnator": Emailnator, "smailpro": SmailPro, "emailmux": EmailMux, "temptf": TempTF, "mailtm": MailTM}
MAIL_ORDER = [p.strip() for p in (os.environ.get("ZAI_SIGNUP_MAIL") or "emailnator,smailpro,emailmux,temptf,mailtm").split(",") if p.strip()]


# ---------------------------------------------------------------- captcha

def _dilate(m, r):
    from numpy.lib.stride_tricks import sliding_window_view as sw
    return sw(np.pad(m, r), (2 * r + 1, 2 * r + 1)).any(axis=(2, 3))


def solve_gap(bg_png, piece_png):
    """x offset (image px) where the piece belongs, plus diagnostics.

    Aliyun serves the background with the gap AI-inpainted (inpainted_with_mask)
    and the piece as the original pixels (bitwise_and_result), so there is no
    visible notch. Two signals, each wrong on its own for some puzzles, agree
    when combined: where the inpainter reproduced the content, the piece
    correlates with the background (NCC); where it smeared it, the region is
    smoother than its surrounding ring. Scores are z-scored and summed.
    """
    bg = np.asarray(Image.open(io.BytesIO(bg_png)).convert("RGB"), dtype=np.float32)
    pc = np.asarray(Image.open(io.BytesIO(piece_png)).convert("RGBA"), dtype=np.float32)
    a = pc[:, :, 3]
    ys, xs = np.nonzero(a > 128)
    if len(ys) == 0:
        raise Fail("captcha", "puzzle piece has no opaque pixels")
    y0, y1, x0, x1 = ys.min(), ys.max() + 1, xs.min(), xs.max() + 1
    m = a[y0:y1, x0:x1] > 128
    h, w = m.shape
    gray = lambda v: 0.299 * v[..., 0] + 0.587 * v[..., 1] + 0.114 * v[..., 2]
    pg = gray(pc[y0:y1, x0:x1])
    pv = pg[m] - pg[m].mean(); pn = np.linalg.norm(pv) + 1e-6
    bgg = gray(bg)
    gy, gx = np.gradient(bgg); energy = np.hypot(gx, gy)
    pad = 4
    # The ring lives outside the piece's bounding box, so both the mask and the
    # energy band are padded by `pad` on every side (edge values at the image border).
    mp = np.pad(m, pad)
    ring = _dilate(mp, 3) & ~_dilate(mp, 1)
    top = y0 - pad
    eband = np.pad(energy, ((pad, pad), (pad, pad)), mode="edge")[top + pad:y1 + pad + pad]  # rows y0-pad .. y1+pad
    band = bgg[y0:y1]
    n = bg.shape[1] - w + 1
    ncc = np.zeros(n); ratio = np.zeros(n)
    for x in range(n):
        wv = band[:, x:x + w][m]; wv = wv - wv.mean()
        ncc[x] = float(wv @ pv / ((np.linalg.norm(wv) + 1e-6) * pn))
        win = eband[:, x:x + w + 2 * pad]  # eband is shifted by pad horizontally
        ratio[x] = win[mp].mean() / (win[ring].mean() + 0.5)
    z = lambda v: (v - v.mean()) / (v.std() + 1e-6)
    comb = z(ncc) - z(ratio)
    best = int(np.argmax(comb))
    diag = {"ncc_x": int(np.argmax(ncc)), "ncc": round(float(ncc.max()), 3), "ring_x": int(np.argmin(ratio)),
            "ring": round(float(ratio.min()), 3), "comb": round(float(comb[best]), 2), "piece_w": int(w), "piece_h": int(h), "piece_y": int(y0)}
    return best - int(x0), diag


def _img_bytes(page, sel):
    """The SDK inlines the first puzzle as data: URLs and loads refreshed ones by URL."""
    src = page.locator(sel).first.get_attribute("src") or ""
    if src.startswith("data:image"):
        return base64.b64decode(src.split(",", 1)[1])
    if src.startswith("http"):
        for _ in range(20):
            if src in IMG_CACHE:
                return IMG_CACHE[src]
            page.wait_for_timeout(250)
        try:
            b64 = page.evaluate("""async (u) => { const r = await fetch(u, {cache: 'force-cache'}); const b = await r.blob();
                return await new Promise(res => { const fr = new FileReader(); fr.onload = () => res(fr.result.split(',')[1]); fr.readAsDataURL(b); }); }""", src)
            if b64:
                return base64.b64decode(b64)
        except Exception as err:
            _log("captcha_image_fetch_error", err=str(err)[:120])
        raise Fail("captcha", "captcha image %s not captured (%s)" % (sel, src[:60]))
    raise Fail("captcha", "captcha image %s has no source (%s)" % (sel, src[:40]))


def _piece_left(page):
    v = page.evaluate("() => { const p = document.querySelector('#aliyunCaptcha-puzzle'); return p ? parseFloat(p.style.left || '0') : null; }")
    return float(v) if v is not None else None


def drag_slider(page, target):
    """Drag the knob until the piece's own left offset reaches target (CSS px),
    with a human-looking trajectory. Closed loop: the knob-to-piece mapping is
    read from the widget instead of assumed."""
    knob = page.locator("#aliyunCaptcha-sliding-slider").first
    box = knob.bounding_box()
    if not box:
        raise Fail("captcha", "slider knob not visible")
    sx, sy = box["x"] + box["width"] / 2, box["y"] + box["height"] / 2
    page.mouse.move(sx, sy); page.mouse.down()
    x = sx
    # Fast approach to ~90% of the guess, then settle on the measured piece position.
    steps = random.randint(18, 28)
    approach = target * random.uniform(0.86, 0.94)
    for i in range(1, steps + 1):
        t = i / steps; ease = 1 - (1 - t) ** 3
        x = sx + approach * ease
        page.mouse.move(x + random.uniform(-0.5, 0.5), sy + random.uniform(-1.5, 1.5))
        page.wait_for_timeout(random.randint(8, 24))
    page.wait_for_timeout(260)  # the piece follows the knob through a 0.2s transition
    left = _piece_left(page)
    ratio = (left / (x - sx)) if left and (x - sx) > 1 else 1.0
    _log("drag_probe", knob_dx=round(x - sx, 1), piece_left=left, ratio=round(ratio, 3))
    # piece_left grows roughly quadratically with knob travel, so the local
    # slope is re-estimated from each move and the last steps are single pixels.
    prev_x, prev_left = x, left
    for _ in range(60):
        left = _piece_left(page)
        if left is None:
            break
        err = target - left
        if abs(err) < 0.5:
            break
        if prev_left is not None and abs(x - prev_x) >= 1 and left != prev_left:
            ratio = max(0.15, min(3.0, (left - prev_left) / (x - prev_x)))
        step = err / ratio
        step = max(-8.0, min(8.0, step))
        if abs(step) < 1:
            step = 1.0 if step > 0 else -1.0
        prev_x, prev_left = x, left
        x += step
        page.mouse.move(x + random.uniform(-0.2, 0.2), sy + random.uniform(-0.8, 0.8))
        page.wait_for_timeout(random.randint(230, 300))
    _log("drag_settled", knob_dx=round(x - sx, 1), piece_left=_piece_left(page), target=round(target, 1))
    page.wait_for_timeout(random.randint(90, 220))
    page.mouse.up()


def captcha_state(page):
    try:
        return page.evaluate("""() => {
          const q = s => document.querySelector(s);
          const t = s => (q(s) ? (q(s).innerText || '').trim().slice(0, 60) : null);
          const img = q('#aliyunCaptcha-img'), pz = q('#aliyunCaptcha-puzzle'), knob = q('#aliyunCaptcha-sliding-slider'), fl = q('#aliyunCaptcha-window-float');
          return {text: t('#aliyunCaptcha-captcha-text'), slide_text: t('#aliyunCaptcha-sliding-text'), float_cls: fl ? fl.className : null,
                  float_display: fl ? getComputedStyle(fl).display : null, img_src: img ? (img.src || '').slice(0, 22) : null,
                  puzzle_src: pz ? (pz.src || '').slice(0, 22) : null, puzzle_left: pz ? pz.style.left : null, knob_left: knob ? knob.style.left : null,
                  knob_cls: knob ? knob.className : null, certify: t('#aliyunCaptcha-certifyId')};
        }""")
    except Exception as err:
        return {"err": str(err)[:80]}


VERIFY = {"n": 0, "last": None}
# Puzzle images as they pass through the browser, keyed by URL: the SDK loads
# refreshed puzzles by URL and the static host is slow to answer a second fetch.
IMG_CACHE = {}


def captcha_passed(page):
    txt = (page.locator("#aliyunCaptcha-captcha-text").first.inner_text() or "").strip() if page.locator("#aliyunCaptcha-captcha-text").count() else ""
    float_shown = page.locator("#aliyunCaptcha-window-float.window-show").count() > 0
    return (not float_shown) and txt and "Click to start" not in txt, txt


def solve_captcha(page, attempts, deadline):
    cap = page.locator("#captcha-element").first
    if not cap.count():
        _log("captcha_absent")
        return True
    cap.click(timeout=10000)
    for attempt in range(attempts):
        if time.time() > deadline:
            raise Fail("captcha", "deadline while solving captcha")
        try:
            page.wait_for_function("() => { const i = document.querySelector('#aliyunCaptcha-img'), p = document.querySelector('#aliyunCaptcha-puzzle');"
                                   " return i && p && i.src && p.src && i.complete && p.complete && i.naturalWidth > 0 && p.naturalWidth > 0; }", timeout=15000)
        except Exception:
            ok, txt = captcha_passed(page)
            if ok:
                _log("captcha_passed", attempt=attempt, how="no_puzzle", text=json.dumps(txt))
                return True
            raise Fail("captcha", "puzzle never rendered")
        page.wait_for_timeout(600)
        bg = _img_bytes(page, "#aliyunCaptcha-img"); pc = _img_bytes(page, "#aliyunCaptcha-puzzle")
        img_box = page.locator("#aliyunCaptcha-img").first.bounding_box()
        nat_w = page.evaluate("() => document.querySelector('#aliyunCaptcha-img').naturalWidth") or 300
        import hashlib
        _log("captcha_images", bg_bytes=len(bg), bg_sha=hashlib.sha1(bg).hexdigest()[:10], piece_bytes=len(pc), piece_sha=hashlib.sha1(pc).hexdigest()[:10])
        gap_x, diag = solve_gap(bg, pc)
        scale = (img_box["width"] / nat_w) if img_box and nat_w else 1.0
        dx = gap_x * scale
        _log("captcha_solve", attempt=attempt, gap_x=gap_x, dx=round(dx, 1), **diag)
        dump = os.environ.get("ZAI_SIGNUP_DUMP_DIR")
        if dump:
            open(os.path.join(dump, "puzzle-%s-%d-bg.png" % (RUN_ID, attempt)), "wb").write(bg); open(os.path.join(dump, "puzzle-%s-%d-piece.png" % (RUN_ID, attempt)), "wb").write(pc)
        _log("captcha_before", **captcha_state(page))
        drag_slider(page, dx)
        _log("captcha_after_drag", **captcha_state(page))
        if dump:
            page.screenshot(path=os.path.join(dump, "drag-%d.png" % attempt))
        page.wait_for_timeout(2500)
        _log("captcha_settled", **captcha_state(page))
        before = VERIFY["n"]
        ok, txt = False, ""
        for _ in range(16):
            page.wait_for_timeout(350)
            res = VERIFY["last"] if VERIFY["n"] > before else None
            st = captcha_state(page)
            txt = st.get("text") or ""
            float_shown = st.get("float_cls") == "window-show" and st.get("float_display") != "none"
            if res is not None and (res.get("VerifyResult") is True or str(res.get("VerifyCode", "")).upper().startswith("T")):
                ok = True; break
            if not float_shown and "Click to start" not in txt:
                ok = True; break
            if res is not None and VERIFY["n"] > before and str(res.get("VerifyCode", "")).upper().startswith("F"):
                break
        if dump:
            with open(os.path.join(dump, "attempts.jsonl"), "a") as f:
                f.write(json.dumps({"run": RUN_ID, "attempt": attempt, "gap_x": gap_x, "passed": bool(ok), "verify": VERIFY["last"], **diag}) + "\n")
        if ok:
            _log("captcha_passed", attempt=attempt, text=json.dumps(txt), verify=json.dumps(VERIFY["last"])[:120])
            return True
        _log("captcha_retry", attempt=attempt, text=json.dumps(txt))
        page.wait_for_timeout(1500)
        # The widget refreshes the puzzle itself after a miss; if the float closed, reopen it.
        if not page.locator("#aliyunCaptcha-window-float.window-show").count():
            page.locator("#captcha-element").first.click(timeout=10000)
            page.wait_for_timeout(2000)
            _log("captcha_reopened", **captcha_state(page))
            if dump:
                page.screenshot(path=os.path.join(dump, "reopen-%d.png" % attempt))
    raise Fail("captcha", "slider puzzle not solved in %d attempts" % attempts)


# ---------------------------------------------------------------- signup

def _launch(pw, req):
    launch = {"headless": bool(req.get("headless")), "args": ["--disable-blink-features=AutomationControlled"]}
    if req.get("proxy"):
        launch["proxy"] = {"server": req["proxy"]}
    browser = pw.chromium.launch(**launch)
    ctx = browser.new_context(user_agent=UA, locale="en-US", viewport={"width": 1280, "height": 800})
    ctx.route("**/*", lambda route: route.abort() if (route.request.resource_type in ("media", "font") or any(s in route.request.url for s in _BLOCK)) else route.fallback())
    return browser, ctx


def run(req):
    timeout = int(req.get("timeout") or 300)
    deadline = time.time() + timeout
    order = [req["mail"]] if req.get("mail") else MAIL_ORDER
    last = None
    with sync_playwright() as pw:
        browser, ctx = _launch(pw, req)
        try:
            for prov_name in order:
                provider = MAIL_PROVIDERS.get(prov_name)
                if provider is None:
                    _log("mail_provider_unknown", name=prov_name)
                    continue
                if time.time() > deadline - 60:
                    break
                try:
                    mailbox = provider(ctx) if getattr(provider, "needs_context", False) else provider()
                except Exception as err:
                    _log("mailbox_error", provider=prov_name, err=str(err)[:160])
                    last = err
                    continue
                _log("mailbox", provider=mailbox.name, domain=mailbox.address.split("@")[-1])
                try:
                    return run_with_mailbox(req, ctx, mailbox, deadline)
                except Fail as err:
                    err.email = mailbox.address
                    last = err
                    if err.stage == "signup" and "EMAIL_DOMAIN_BLOCKED" in str(err):
                        _log("mail_domain_blocked", provider=mailbox.name, domain=mailbox.address.split("@")[-1])
                        continue
                    raise
        finally:
            browser.close()
    if isinstance(last, Fail):
        raise last
    raise Fail("mailbox", "no mail provider worked: %s" % last)


def run_with_mailbox(req, ctx, mailbox, deadline):
    dump = os.environ.get("ZAI_SIGNUP_DUMP_DIR")
    name = random.choice(["Alex", "Sam", "Jordan", "Taylor", "Casey", "Riley", "Morgan", "Jamie", "Avery", "Quinn"]) + " " + random.choice(["Reed", "Hart", "Lane", "Cole", "Park", "Wells", "Gray", "Shaw", "Moss", "Dale"])
    password = "Es" + secrets.token_urlsafe(12) + "!9"
    calls = {}
    page = ctx.new_page()

    def shot(tag):
        if dump:
            try:
                os.makedirs(dump, exist_ok=True)
                page.screenshot(path=os.path.join(dump, "%d-%s.png" % (int(time.time()), tag)))
            except Exception:
                pass

    try:
        def on_resp(r):
            u = r.url
            if "captcha" in u and u.split("?")[0].endswith(".png"):
                try:
                    IMG_CACHE[u] = r.body()
                except Exception as err:
                    _log("captcha_image_capture_error", err=str(err)[:80])
            if "-verify.captcha" in u:
                try:
                    d = r.json(); res = d.get("Result") or {}
                except Exception:
                    d, res = {}, {}
                VERIFY["n"] += 1; VERIFY["last"] = res
                _log("captcha_verify", code=res.get("VerifyCode"), result=res.get("VerifyResult"), msg=json.dumps(str(res.get("Message") or d.get("Message"))[:80]))
            if "/api/v1/auths/" in r.url and r.request.method == "POST":
                try:
                    body = r.text()[:300]
                except Exception:
                    body = ""
                key = r.url.split("/api/v1/auths/")[-1].split("?")[0]
                calls[key] = (r.status, body)
                _log("api", path=key, status=r.status, body=json.dumps(body[:160]))
        page.on("response", on_resp)

        page.goto(SIGNUP_URL, wait_until="domcontentloaded", timeout=60000)
        page.wait_for_selector("input[type=email]", timeout=30000)
        page.wait_for_timeout(1500)
        page.locator("input[autocomplete=name], input[placeholder*='Name' i]").first.fill(name)
        page.locator("input[type=email]").first.fill(mailbox.address)
        for pw_in in page.locator("input[type=password]:visible").all():
            pw_in.fill(password)
        shot("filled")
        # The Aliyun SDK loads after the form; the widget body appears when it is ready.
        try:
            page.wait_for_selector("#aliyunCaptcha-captcha-body", timeout=30000)
        except Exception:
            _log("captcha_widget_missing")
        solve_captcha(page, attempts=int(os.environ.get("ZAI_SIGNUP_CAPTCHA_ATTEMPTS", "8")), deadline=deadline)
        shot("captcha")
        btn = page.locator("button.ButtonCreateAccount, button:has-text('Create Account')").first
        btn.click(timeout=10000)
        for _ in range(60):
            if "signup" in calls:
                break
            page.wait_for_timeout(500)
        shot("submitted")
        status, body = calls.get("signup", (0, ""))
        if status != 200:
            raise Fail("signup", "signup answered %s: %s" % (status or "nothing", body[:200]))
        _log("signup_sent")

        link = mailbox.wait_link(min(deadline, time.time() + 180))
        if not link:
            raise Fail("mail", "no verification mail within the wait")
        _log("mail_link", host=link.split("/")[2])
        page.goto(link, wait_until="domcontentloaded", timeout=60000)
        for _ in range(60):
            if "verify_email" in calls:
                break
            page.wait_for_timeout(500)
        status, body = calls.get("verify_email", (0, ""))
        if status != 200:
            shot("verify")
            raise Fail("verify", "verify_email answered %s: %s" % (status or "nothing", body[:200]))
        page.wait_for_selector("input[type=password]:visible", timeout=30000)
        for pw_in in page.locator("input[type=password]:visible").all():
            pw_in.fill(password)
        shot("finish-filled")
        sub = page.locator("button[type=submit]:visible, button:has-text('Complete'):visible, button:has-text('Create'):visible, button:has-text('Sign'):visible").first
        if sub.count():
            sub.click(timeout=10000)
        else:
            page.keyboard.press("Enter")
        for _ in range(60):
            if "finish_signup" in calls:
                break
            page.wait_for_timeout(500)
        status, body = calls.get("finish_signup", (0, ""))
        if status != 200:
            shot("finish")
            raise Fail("finish", "finish_signup answered %s: %s" % (status or "nothing", body[:200]))
        page.wait_for_timeout(2500)
        token = page.evaluate("() => localStorage.getItem('token')") or ""
        if not token:
            raise Fail("token", "no token in localStorage after finish_signup")
        me = _http("GET", "https://chat.z.ai/api/v1/auths/", headers={"Authorization": "Bearer " + token})
        if me.get("role") == "guest" or not me.get("id"):
            raise Fail("token", "token resolves to %s" % (me.get("role") or "nothing"))
        _log("account_ready", user_id=me.get("id"), role=me.get("role"))
        return {"ok": True, "email": mailbox.address, "password": password, "token": token,
                "user_id": me.get("id"), "name": name, "mail_provider": mailbox.name,
                "mail_password": getattr(mailbox, "password", "")}
    finally:
        try:
            page.close()
        except Exception:
            pass


def main():
    try:
        req = json.loads(sys.stdin.readline() or "{}")
    except json.JSONDecodeError:
        req = {}
    try:
        out = run(req)
    except Fail as err:
        _log("failed", stage=err.stage, err=str(err)[:200])
        out = {"ok": False, "stage": err.stage, "error": str(err)[:300], "email": getattr(err, "email", None)}
    except Exception as err:
        _log("failed", stage="internal", err=str(err)[:200])
        out = {"ok": False, "stage": "internal", "error": str(err)[:300]}
    sys.stdout.write(json.dumps(out) + "\n"); sys.stdout.flush()
    return 0 if out.get("ok") else 1


if __name__ == "__main__":
    raise SystemExit(main())
