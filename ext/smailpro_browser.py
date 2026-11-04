"""SmailPro mailbox support using 2captcha Turnstile tokens."""

import json
import re
import sys
import time
import urllib.request
from datetime import datetime

BASE_URL = "https://smailpro.com"
SITE_KEY = "0x4AAAAAAABIS_gEec2IwOhI"

FAMILIES = {
    "gmail": {"domain": "gmail.com", "backend": "temp_gmail"},
    "googlemail": {"domain": "googlemail.com", "backend": "temp_gmail"},
    "outlook": {"domain": "outlook.com", "backend": "temp_outlook"},
}


class SmailproError(RuntimeError):
    pass


class SmailproExpiredError(SmailproError):
    pass


class SmailproHTTPError(SmailproError):
    def __init__(self, status):
        self.status = status
        super().__init__(f"HTTP {status}")


def _post_json(url, payload):
    request = urllib.request.Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.loads(response.read().decode("utf-8"))


def solve_turnstile(client_key, action, post_json=_post_json, sleep=time.sleep, timeout=120):
    if not client_key:
        raise SmailproError("TWOCAPTCHA_API_KEY is required for SmailPro")

    created = post_json("https://api.2captcha.com/createTask", {
        "clientKey": client_key,
        "task": {
            "type": "TurnstileTaskProxyless",
            "websiteURL": f"{BASE_URL}/temporary-email",
            "websiteKey": SITE_KEY,
            "action": action,
        },
    })
    if created.get("errorId"):
        err_code = str(created.get("errorCode", "unknown"))
        err_desc = str(created.get("errorDescription", ""))
        if "ZERO_BALANCE" in err_code or "zero or negative" in err_desc.lower():
            sys.stderr.write("[smailpro] event=twocaptcha_zero_balance\n")
            sys.stderr.flush()
        raise SmailproError(
            f"2captcha createTask error: {err_code} ({err_desc})"
        )
    task_id = created.get("taskId")
    if not task_id:
        raise SmailproError("2captcha returned empty taskId")

    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        sleep(3)
        result = post_json("https://api.2captcha.com/getTaskResult", {
            "clientKey": client_key,
            "taskId": task_id,
        })
        if result.get("errorId"):
            raise SmailproError(
                f"2captcha getTaskResult error: {result.get('errorCode', 'unknown')} "
                f"({result.get('errorDescription', '')})"
            )
        if result.get("status") == "ready":
            token = (result.get("solution") or {}).get("token")
            if not token:
                raise SmailproError("2captcha returned an empty Turnstile token")
            return token
    raise SmailproError("2captcha Turnstile task timed out")


def domain_of(address):
    return address.rsplit("@", 1)[-1].lower() if "@" in address else ""


def available_families(candidates, blocked):
    return [family for family in candidates if family in FAMILIES]


def parse_created_mailbox(data, family, blocked):
    if not isinstance(data, dict) or not data.get("address") or not data.get("key"):
        message = data.get("msg") or data.get("message") if isinstance(data, dict) else "invalid response"
        raise SmailproError(f"create failed: {message}")
    domain = domain_of(data["address"])
    local = data["address"].rsplit("@", 1)[0]
    if "+" in local:
        raise SmailproError("plus aliases are not accepted")
    expected_domain = FAMILIES.get(family, {}).get("domain")
    if expected_domain != domain:
        raise SmailproError(f"unexpected {family} domain: {domain}")
    return {
        "address": data["address"],
        "timestamp": data.get("timestamp"),
        "key": data["key"],
        "family": family,
    }


def _message_timestamp(message):
    raw_date = message.get("timestamp") or message.get("time") or message.get("textDate") or 0
    try:
        value = float(raw_date)
        return value / 1000 if value > 100000000000 else value
    except (TypeError, ValueError):
        pass
    try:
        return datetime.fromisoformat(str(raw_date).replace("Z", "+00:00")).timestamp()
    except (TypeError, ValueError):
        return 0


def select_service_message(messages, since=0, service_terms=("notion",), excluded_ids=None):
    terms = tuple(str(term).strip().lower() for term in service_terms if str(term).strip())
    excluded_ids = set(excluded_ids or ())
    candidates = []
    for position, message in enumerate(messages or []):
        if str(message.get("mid", "")) in excluded_ids:
            continue
        text = " ".join(str(message.get(field, "")) for field in ("textFrom", "textSubject")).lower()
        message_time = _message_timestamp(message)
        if message_time and since and message_time < since:
            continue
        if any(term in text for term in terms):
            candidates.append((message_time, position, message))
    if not candidates:
        return None
    return max(candidates, key=lambda item: (item[0], item[1]))[2]


def select_notion_message(messages, since=0):
    return select_service_message(messages, since, ("notion",))


def extract_code(text):
    css_like = re.compile(
        r"(?i)webkit|mso-|font-|text-size|border-|margin-|padding-|line-height|box-sizing|button-|hover|container|invisible|wrapper|layout|section"
    )
    passphrase_re = re.compile(r"\b([a-z]{3,}-[a-z]{3,}-[a-z]{3,}-[a-z]{3,})\b")
    # Passphrase patterns (highest priority)
    for pattern in (
        r'(?i)style="[^"]*font-size[^"]*"[^>]*>\s*([a-z]+-[a-z]+-[a-z]+-[a-z]+)\s*<',
        r"(?i)<(?:b|strong)>\s*([a-z]+-[a-z]+-[a-z]+-[a-z]+)\s*</(?:b|strong)>",
        r"(?i)(?:temporary|login|passcode|verification|code|signup)[^a-z]{0,60}([a-z]+-[a-z]+-[a-z]+-[a-z]+)",
    ):
        for candidate in re.findall(pattern, text or ""):
            if candidate and not css_like.search(candidate):
                return candidate
    # Bare passphrase anywhere in the text
    for candidate in passphrase_re.findall(text or ""):
        if not css_like.search(candidate):
            return candidate
    # Digit patterns only when no passphrase was found
    for pattern in (
        r"(?i)(?:temporary|login|passcode|verification|code|signup)\D{0,30}(\d{4,8})",
        r"\b(\d{6})\b",
    ):
        for candidate in re.findall(pattern, text or ""):
            if candidate:
                return candidate
    return ""


def _parse_cookie_string(cookie_str, domain=".smailpro.com"):
    cookies = []
    for part in cookie_str.split(";"):
        part = part.strip()
        if not part:
            continue
        if "=" in part:
            name, _, value = part.partition("=")
            if name.strip():
                cookies.append({
                    "name": name.strip(),
                    "value": value.strip().strip('"'),
                    "domain": domain,
                    "path": "/",
                })
        else:
            # Bare session token without key=val: inject as both sonjj_session and sessionToken
            cookies.append({
                "name": "sonjj_session",
                "value": part.strip().strip('"'),
                "domain": domain,
                "path": "/",
            })
            cookies.append({
                "name": "sessionToken",
                "value": part.strip().strip('"'),
                "domain": domain,
                "path": "/",
            })
    return cookies


class SmailproSession:
    def __init__(self, page, mailbox, twocaptcha_key=None, is_premium=False):
        self.page = page
        self.mailbox = mailbox
        self.twocaptcha_key = twocaptcha_key
        self.is_premium = is_premium

    @classmethod
    def create(cls, context, candidates, blocked, twocaptcha_key=None, token=None, mailbox_type="alias"):
        is_premium = bool(token or mailbox_type == "real")
        if not is_premium and not twocaptcha_key:
            raise SmailproError("TWOCAPTCHA_API_KEY is required for SmailPro")

        if token:
            try:
                cookies = _parse_cookie_string(token, domain=".smailpro.com")
                if cookies:
                    context.add_cookies(cookies)
            except Exception:
                pass

        page = context.new_page()
        page.goto(f"{BASE_URL}/temporary-email", wait_until="domcontentloaded", timeout=45000)

        # In case token was a localStorage sessionToken rather than cookie:
        if token and "=" not in token:
            try:
                page.evaluate("(t) => { localStorage.setItem('sessionToken', t); }", token)
            except Exception:
                pass

        last_error = None
        for family in available_families(candidates, blocked):
            for _ in range(5):
                try:
                    config = FAMILIES[family]
                    headers = {"Content-Type": "application/json"}
                    need_captcha = not is_premium or (isinstance(last_error, SmailproHTTPError) and last_error.status == 403)
                    if need_captcha and twocaptcha_key:
                        captcha_token = solve_turnstile(twocaptcha_key, "smailpro_create")
                        headers["x-captcha"] = captcha_token
                    
                    fetch_options = {
                        "method": "GET",
                        "query": {"username": "random", "type": mailbox_type, "domain": config["domain"], "server": "1"},
                        "headers": headers,
                        "credentials": "include",
                        "response": "json",
                    }
                    result = cls._fetch(page, "/app/create", fetch_options)
                    return cls(page, parse_created_mailbox(result, family, blocked), twocaptcha_key, is_premium)
                except Exception as error:
                    last_error = error
                    if isinstance(error, SmailproHTTPError) and error.status == 429:
                        # Cloudflare 1015 IP rate limit on this proxy relay; rotate to next family or let caller rotate relay
                        break
                    time.sleep(1)
        page.close()
        raise SmailproError(str(last_error or "no available family"))

    @staticmethod
    def _fetch(page, path, options):
        response = page.evaluate(
            """async ({path, options}) => {
                let url = path;
                if (options.query) url += '?' + new URLSearchParams(options.query).toString();
                const fetchOpts = {
                    method: options.method,
                    headers: options.headers || {},
                    body: options.body ? JSON.stringify(options.body) : undefined
                };
                if (options.credentials) {
                    fetchOpts.credentials = options.credentials;
                }
                const response = await fetch(url, fetchOpts);
                const body = options.response === 'text' ? await response.text() : await response.json().catch(() => null);
                return {ok: response.ok, status: response.status, body};
            }""",
            {"path": path, "options": options},
            isolated_context=False,
        )
        return _unwrap_response(response)

    def _list_messages(self):
        payloads = self._fetch(self.page, "/app/inbox", {
            "method": "POST",
            "headers": {"Content-Type": "application/json"},
            "body": [{key: self.mailbox.get(key) for key in ("address", "timestamp", "key")}],
            "credentials": "include",
            "response": "json",
        })
        if not payloads:
            raise SmailproExpiredError("mailbox payload expired")
        item = payloads[0]
        if item.get("key"):
            self.mailbox["key"] = item["key"]
        backend = FAMILIES[self.mailbox["family"]]["backend"]
        listing = self._fetch(self.page, f"https://api.sonjj.com/v1/{backend}/inbox", {
            "method": "GET", "query": {"payload": item.get("payload", "")}, "response": "json"
        })
        return listing.get("messages", [])

    def message_ids(self):
        try:
            messages = self._list_messages()
        except SmailproHTTPError as error:
            if error.status == 404:
                return set()
            raise
        return {str(message.get("mid")) for message in messages if message.get("mid")}

    def poll_code(
            self,
            since,
            timeout=60,
            interval=10,
            service_terms=("notion",),
            excluded_ids=None,
            on_poll=None,
    ):
        deadline = time.monotonic() + timeout
        attempt = 0
        while time.monotonic() < deadline:
            attempt += 1
            try:
                messages = self._list_messages()
            except SmailproHTTPError as error:
                if error.status != 404:
                    raise
                messages = []
            message = select_service_message(messages, since, service_terms, excluded_ids)
            if on_poll:
                on_poll(
                    attempt=attempt,
                    message_count=len(messages),
                    matched=bool(message),
                )
            if message:
                payload_headers = {"Content-Type": "application/json"}
                if not self.is_premium:
                    token = self._captcha_with_retry("smailpro_message")
                    payload_headers["x-captcha"] = token
                payload = self._fetch(self.page, "/app/message", {
                    "method": "GET", "query": {"email": self.mailbox["address"], "mid": message.get("mid", "")},
                    "headers": payload_headers, "credentials": "include", "response": "text"
                })
                backend = FAMILIES[self.mailbox["family"]]["backend"]
                detail = self._fetch(self.page, f"https://api.sonjj.com/v1/{backend}/message", {
                    "method": "GET", "query": {"payload": payload}, "response": "json"
                })
                raw_body = str(message.get("textSubject", "")) + "\n" + str(detail.get("body", ""))
                vis_body = re.sub(r"(?is)<(style|script)[^>]*>.*?</\1>|<!--.*?-->", " ", raw_body)
                vis_body = "\n".join(re.sub(r"\s+", " ", l).strip() for l in re.sub(r"<[^>]+>", "\n", vis_body).splitlines() if l.strip())
                code = extract_code(vis_body) or extract_code(raw_body)
                if code:
                    if on_poll:
                        on_poll(
                            attempt=attempt,
                            message_count=len(messages),
                            matched=True,
                            code_found=True,
                        )
                    return code
            time.sleep(interval)
        return None

    def _captcha_with_retry(self, action, attempts=2):
        last_error = None
        for _ in range(attempts):
            try:
                return solve_turnstile(self.twocaptcha_key, action)
            except SmailproError as error:
                last_error = error
        raise last_error or SmailproError("CAPTCHA failed")


def _unwrap_response(response):
    if not response.get("ok"):
        if response.get("status") == 403:
            raise SmailproExpiredError("mailbox expired")
        raise SmailproHTTPError(response.get("status"))
    return response.get("body")