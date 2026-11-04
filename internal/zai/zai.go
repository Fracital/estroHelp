package zai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// chat.z.ai (GLM) provider. chat.z.ai is an Open WebUI fork behind Cloudflare
// and an Aliyun captcha. Two transports reach it (see zaiTransport):
//
//   - patchright (default): a pool of warm headed browsers under xvfb whose own
//     sends are rewritten in flight (ext/zai_browser.py). The page supplies the
//     session, Cloudflare clearance and the single-use captcha token.
//   - browserless: guest JWT from GET /api/v1/auths/, the frontend HMAC
//     x-signature computed here (zaiSignature, byte-verified), and curl_cffi for
//     Cloudflare's TLS check. The captcha token must come from the browser
//     extension (zai_ext.go) or ZAI_CAPTCHA_PARAM.

const (
	zaiAPIBase     = "https://chat.z.ai"
	zaiAuthPath    = "/api/v1/auths/"
	zaiComplete    = "/api/v2/chat/completions"
	zaiModelsPath  = "/api/models"
	zaiFEVersion   = "prod-fe-1.1.123"
	zaiSignKey     = "key-@@@@)))()((9))-xxxx&&&%%%%%"
	zaiSignWindow  = 5 * 60 * 1000 // 5-minute bucket, ms
	zaiGuestRegion = "global"
)

// zaiModelMap maps public model IDs accepted by the proxy to z.ai upstream ids.
// Guest sessions only get x-preview-l; signed-in sessions unlock the rest.
var zaiModelMap = map[string]string{
	// guest tier
	"x-preview-l":   "x-preview-l",
	"glm-5.3-flash": "x-preview-l",
	"glm-5-3-flash": "x-preview-l",
	"free":          "x-preview-l",
	"default":       "x-preview-l",
	// signed-in tier
	"glm-5.3": "glm-5.3",
	"glm-5-3": "glm-5.3",
	"glm-5.2": "glm-5.2",
	"glm-5-2": "glm-5.2",
}

// zaiConfigFile holds a signed-in account, like chatgpt_config.json. It is
// git-ignored: {"token": "<localStorage token JWT>", "cookie": "..."}.
const zaiConfigFile = "zai_config.json"

// zaiAccountToken returns the signed-in account's JWT: ZAI_TOKEN, else the
// config file (ZAI_CONFIG_FILE overrides its path). Empty means guest.
func zaiAccountToken() string {
	if v := strings.TrimSpace(os.Getenv("ZAI_TOKEN")); v != "" {
		return v
	}
	path := strings.TrimSpace(os.Getenv("ZAI_CONFIG_FILE"))
	if path == "" {
		path = zaiConfigFile
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		logf("event=zai_config_invalid path=%s", path)
		return ""
	}
	return strings.TrimSpace(cfg.Token)
}

// zaiSession is a live z.ai guest/account session.
type zaiSession struct {
	Token  string // Bearer JWT (also sent as ?token=)
	UserID string
}

// zaiSessionCache avoids re-minting a guest token on every request. Guest JWTs
// carry no exp claim, so a cached token is valid until the server retires it;
// a 401 invalidates the cache and forces a fresh mint.
var zaiSessionCache struct {
	mu      sync.Mutex
	session *zaiSession
	minting chan struct{} // non-nil while a mint is in flight; closed when it ends
}

// zaiUpstreamError carries a z.ai error frame back to the handler.
type zaiUpstreamError struct {
	Status int
	Detail string
	Code   string
	// CaptchaRequired marks the FRONTEND_CAPTCHA_REQUIRED frame so the caller
	// can re-solve the captcha and retry once.
	CaptchaRequired bool
}

func (e *zaiUpstreamError) Error() string { return e.Detail }

// ---------------------------------------------------------------------------
// x-signature (reverse-engineered, byte-verified against a live capture)
// ---------------------------------------------------------------------------

// zaiSignature reproduces the frontend's X-Signature header.
//
//	sortedPayload = "requestId,<uuid>,timestamp,<ts>,user_id,<uid>"  (sorted k,v pairs)
//	h             = sortedPayload + "|" + base64(lastUserText) + "|" + ts
//	derived       = hex HMAC-SHA256(key=zaiSignKey, msg=floor(ts/5min))
//	signature     = hex HMAC-SHA256(key=derived, msg=h)
func zaiSignature(lastUserText, tsMillis, requestID, userID string) string {
	pairs := []string{
		"requestId," + requestID,
		"timestamp," + tsMillis,
		"user_id," + userID,
	}
	sort.Strings(pairs)
	sortedPayload := strings.Join(pairs, ",")

	b64prompt := base64.StdEncoding.EncodeToString([]byte(lastUserText))
	h := sortedPayload + "|" + b64prompt + "|" + tsMillis

	ts, _ := strconv.ParseInt(tsMillis, 10, 64)
	window := strconv.FormatInt(ts/zaiSignWindow, 10)

	mac := hmac.New(sha256.New, []byte(zaiSignKey))
	mac.Write([]byte(window))
	derived := hex.EncodeToString(mac.Sum(nil))

	mac2 := hmac.New(sha256.New, []byte(derived))
	mac2.Write([]byte(h))
	return hex.EncodeToString(mac2.Sum(nil))
}

// ---------------------------------------------------------------------------
// Guest session minting (browserless)
// ---------------------------------------------------------------------------

// zaiMintGuestSession fetches a fresh guest JWT via curl_cffi. The endpoint is
// Cloudflare-fronted, so we cannot use net/http.
func zaiMintGuestSession(ctx context.Context) (*zaiSession, error) {
	out, err := zaiCurlCffi(ctx, "GET", zaiAPIBase+zaiAuthPath, map[string]string{
		"accept": "application/json",
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("zai guest auth transport: %w", err)
	}
	var parsed struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("zai guest auth parse: %w", err)
	}
	if parsed.Token == "" {
		return nil, errors.New("zai guest auth returned no token")
	}
	return &zaiSession{Token: parsed.Token, UserID: parsed.ID}, nil
}

var zaiMintSessionFunc = zaiMintGuestSession

// zaiGetSession returns a cached session, minting one on first use. One mint
// runs at a time, outside the lock, so a waiter's own ctx still bounds it.
func zaiGetSession(ctx context.Context) (*zaiSession, error) {
	for {
		zaiSessionCache.mu.Lock()
		if s := zaiSessionCache.session; s != nil {
			zaiSessionCache.mu.Unlock()
			return s, nil
		}
		if wait := zaiSessionCache.minting; wait != nil {
			zaiSessionCache.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		done := make(chan struct{})
		zaiSessionCache.minting = done
		zaiSessionCache.mu.Unlock()

		sess, err := zaiMintSessionFunc(ctx)
		zaiSessionCache.mu.Lock()
		if err == nil {
			zaiSessionCache.session = sess
		}
		zaiSessionCache.minting = nil
		zaiSessionCache.mu.Unlock()
		close(done)
		return sess, err
	}
}

// zaiInvalidateSession drops the cached session after a 401, unless a
// concurrent request already replaced it with a fresh one.
func zaiInvalidateSession(stale *zaiSession) {
	zaiSessionCache.mu.Lock()
	if zaiSessionCache.session == stale {
		zaiSessionCache.session = nil
	}
	zaiSessionCache.mu.Unlock()
}

// ---------------------------------------------------------------------------
// curl_cffi transport (Chrome-impersonating, Cloudflare-safe)
// ---------------------------------------------------------------------------

// zaiPythonCmd locates the Python interpreter that has curl_cffi/patchright.
func zaiPythonCmd() string {
	if v := strings.TrimSpace(os.Getenv("ZAI_PYTHON")); v != "" {
		return v
	}
	return "python3"
}

// zaiCurlCffiScript is the minimal Chrome-impersonating fetch. It reads one
// JSON line {method,url,headers,body_b64} on stdin and writes the raw response
// body to stdout, exiting non-zero with the status on stderr for HTTP >= 400.
const zaiCurlCffiScript = `
import sys, json, base64
from curl_cffi import requests
req = json.loads(sys.stdin.readline())
headers = req.get("headers") or {}
body = base64.b64decode(req["body_b64"]) if req.get("body_b64") else None
r = requests.request(req["method"], req["url"], headers=headers, data=body,
                     impersonate="chrome", timeout=req.get("timeout", 120))
if r.status_code >= 400:
    sys.stderr.write("HTTP %d\n" % r.status_code)
    sys.stderr.write(r.text[:1000])
    sys.exit(3)
sys.stdout.buffer.write(r.content)
`

// zaiCurlCffi performs one Chrome-impersonating HTTP request and returns the
// response body. Used for the non-streaming guest-auth mint.
func zaiCurlCffi(ctx context.Context, method, urlStr string, headers map[string]string, body []byte) ([]byte, error) {
	payload := map[string]interface{}{
		"method":  method,
		"url":     urlStr,
		"headers": headers,
		"timeout": 60,
	}
	if len(body) > 0 {
		payload["body_b64"] = base64.StdEncoding.EncodeToString(body)
	}
	line, _ := json.Marshal(payload)

	cmd := exec.CommandContext(ctx, zaiPythonCmd(), "-c", zaiCurlCffiScript)
	cmd.Stdin = bytes.NewReader(append(line, '\n'))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The path only: the query can carry the session token.
		path, _, _ := strings.Cut(urlStr, "?")
		return nil, fmt.Errorf("curl_cffi %s: %v: %s", path, err, truncate(strings.TrimSpace(stderr.String()), 300))
	}
	return stdout.Bytes(), nil
}

// zaiCurlCffiStream performs a streaming POST and returns a handle whose Read
// yields raw SSE bytes. The caller must Close it (which kills the child).
type zaiStream struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stdin   io.Closer
	stderr  *bytes.Buffer
	waitErr error
	waited  bool
}

// wait reaps the child once; Read and Close run on the same goroutine.
func (s *zaiStream) wait() error {
	if !s.waited {
		s.waited, s.waitErr = true, s.cmd.Wait()
	}
	return s.waitErr
}

// Read surfaces the child's failure at EOF: on HTTP >= 400 the script exits
// non-zero with no stdout, which would otherwise read as an empty completion.
func (s *zaiStream) Read(p []byte) (int, error) {
	n, err := s.stdout.Read(p)
	if err == io.EOF {
		if werr := s.wait(); werr != nil {
			detail := strings.TrimSpace(s.stderr.String())
			status := 502
			if code, ok := strings.CutPrefix(detail, "HTTP "); ok {
				if v, convErr := strconv.Atoi(strings.Fields(code)[0]); convErr == nil {
					status = v
				}
			}
			return n, &zaiUpstreamError{Status: status, Detail: "z.ai: " + truncate(detail, 300)}
		}
	}
	return n, err
}

func (s *zaiStream) Close() error {
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	if !s.waited && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.wait()
	return nil
}

// zaiCurlCffiStreamScript streams response chunks to stdout as they arrive.
const zaiCurlCffiStreamScript = `
import sys, json, base64
from curl_cffi import requests
req = json.loads(sys.stdin.readline())
headers = req.get("headers") or {}
body = base64.b64decode(req["body_b64"]) if req.get("body_b64") else None
with requests.Session().stream(req["method"], req["url"], headers=headers,
                               data=body, impersonate="chrome",
                               timeout=req.get("timeout", 180)) as r:
    if r.status_code >= 400:
        sys.stderr.write("HTTP %d\n" % r.status_code)
        sys.stderr.write(r.text[:1000])
        sys.exit(3)
    for chunk in r.iter_content(chunk_size=2048):
        if chunk:
            sys.stdout.buffer.write(chunk)
            sys.stdout.buffer.flush()
`

func zaiCurlCffiStream(ctx context.Context, urlStr string, headers map[string]string, body []byte) (*zaiStream, error) {
	payload := map[string]interface{}{
		"method":   "POST",
		"url":      urlStr,
		"headers":  headers,
		"body_b64": base64.StdEncoding.EncodeToString(body),
		"timeout":  180,
	}
	line, _ := json.Marshal(payload)

	cmd := exec.CommandContext(ctx, zaiPythonCmd(), "-c", zaiCurlCffiStreamScript)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	s := &zaiStream{cmd: cmd, stdout: stdout, stdin: stdin, stderr: &stderr}
	if _, err := stdin.Write(append(line, '\n')); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// zaiSolveCaptcha returns a captcha_verify_param for the browserless transport:
// from the browser extension when connected, else a pasted ZAI_CAPTCHA_PARAM.
// A standalone Patchright solver does not work: Aliyun's SDK refuses to boot
// under automation.
func zaiSolveCaptcha(ctx context.Context) (string, error) {
	if zaiExtEnabled() && zaiExt.isConnected() {
		return zaiExt.solve(ctx)
	}
	if v := strings.TrimSpace(os.Getenv("ZAI_CAPTCHA_PARAM")); v != "" {
		return v, nil
	}
	return "", errors.New("z.ai captcha: set ZAI_EXT_TOKEN and load the browser extension, or paste ZAI_CAPTCHA_PARAM")
}

func zaiEnvBool(name string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

// ---------------------------------------------------------------------------
// Completion request build + SSE decode
// ---------------------------------------------------------------------------

// zaiCompletionRequest is the POST body to /api/v2/chat/completions.
type zaiCompletionRequest struct {
	Stream                 bool              `json:"stream"`
	Model                  string            `json:"model"`
	Messages               []zaiMessage      `json:"messages"`
	SignaturePrompt        string            `json:"signature_prompt"`
	Params                 map[string]any    `json:"params"`
	Extra                  map[string]any    `json:"extra"`
	Features               map[string]any    `json:"features"`
	Variables              map[string]string `json:"variables,omitempty"`
	ChatID                 string            `json:"chat_id"`
	ID                     string            `json:"id"`
	CurrentUserMsgID       string            `json:"current_user_message_id,omitempty"`
	CurrentUserMsgParentID any               `json:"current_user_message_parent_id"`
	CaptchaVerifyParam     string            `json:"captcha_verify_param,omitempty"`
}

// zaiBuildQuery reproduces the telemetry-heavy query string. Only a handful of
// params are load-bearing (timestamp, requestId, user_id, token,
// signature_timestamp); the rest mirror a real browser so the request looks
// ordinary.
func zaiBuildQuery(sess *zaiSession, ts, requestID string) string {
	now := time.Now()
	q := url.Values{}
	q.Set("timestamp", ts)
	q.Set("requestId", requestID)
	q.Set("user_id", sess.UserID)
	q.Set("version", "0.0.1")
	q.Set("platform", "web")
	q.Set("token", sess.Token)
	q.Set("user_agent", cloudflareBrowserUserAgent)
	q.Set("language", "en-US")
	q.Set("languages", "en-US,en")
	q.Set("timezone", "America/New_York")
	q.Set("cookie_enabled", "true")
	q.Set("screen_width", "1920")
	q.Set("screen_height", "1080")
	q.Set("screen_resolution", "1920x1080")
	q.Set("viewport_height", "800")
	q.Set("viewport_width", "1280")
	q.Set("viewport_size", "1280x800")
	q.Set("color_depth", "24")
	q.Set("pixel_ratio", "1")
	q.Set("current_url", zaiAPIBase+"/")
	q.Set("pathname", "/")
	q.Set("search", "")
	q.Set("hash", "")
	q.Set("host", "chat.z.ai")
	q.Set("hostname", "chat.z.ai")
	q.Set("protocol", "https:")
	q.Set("referrer", "")
	q.Set("title", "Z.ai - Advanced AI Chatbot & Agent powered by GLM-5.3-Flash")
	q.Set("timezone_offset", "240")
	q.Set("local_time", now.UTC().Format("2006-01-02T15:04:05.000Z"))
	q.Set("utc_time", now.UTC().Format(time.RFC1123))
	q.Set("is_mobile", "false")
	q.Set("is_touch", "false")
	q.Set("max_touch_points", "0")
	q.Set("browser_name", "Chrome")
	q.Set("os_name", "Linux")
	q.Set("signature_timestamp", ts)
	return q.Encode()
}

// zaiSSEEvent is one parsed `data:` frame.
type zaiSSEEvent struct {
	Type string `json:"type"`
	Data struct {
		DeltaContent string `json:"delta_content"`
		Phase        string `json:"phase"`
		Done         bool   `json:"done"`
		Usage        *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
		Error *struct {
			Detail           string `json:"detail"`
			Code             string `json:"code"`
			CaptchaErrorType string `json:"captcha_error_type"`
		} `json:"error"`
		// Nested error envelope: data.data.error
		Data *struct {
			Error *struct {
				Detail           string `json:"detail"`
				Code             string `json:"code"`
				CaptchaErrorType string `json:"captcha_error_type"`
			} `json:"error"`
		} `json:"data"`
	} `json:"data"`
}

// zaiStreamResult accumulates a full completion from the SSE stream.
type zaiStreamResult struct {
	Thinking         string
	Answer           string
	PromptTokens     int
	CompletionTokens int
}

type zaiDeltaKey struct{}

// zaiWithDeltas makes the completion stream answer text to fn as it arrives.
// Only plain prose is streamed; a reply holding a tool call stays buffered.
func zaiWithDeltas(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, zaiDeltaKey{}, fn)
}

func zaiDeltaFunc(ctx context.Context) func(string) {
	fn, _ := ctx.Value(zaiDeltaKey{}).(func(string))
	return fn
}

type zaiThinkKey struct{}

// zaiWithThinking streams the model's thinking phase to fn as it arrives. It
// is never held back: thinking cannot carry a tool call the parser must see.
func zaiWithThinking(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, zaiThinkKey{}, fn)
}

func zaiThinkingFunc(ctx context.Context) func(string) {
	fn, _ := ctx.Value(zaiThinkKey{}).(func(string))
	return fn
}

// zaiRunCompletionFunc is the injectable completion entry point. Tests replace
// it to exercise handlers without a live chat.z.ai upstream.
var zaiRunCompletionFunc = zaiRunCompletion

// zaiTransport selects how a completion reaches chat.z.ai.
//   - Default "patchright": a warm headed Chromium under xvfb-run
//     (ext/zai_browser.py) whose own send is rewritten in flight, so the
//     page mints the captcha and signature itself. No extension needed.
//   - "browserless": curl_cffi + Go-computed signature. Needs the browser
//     extension (ZAI_EXT_TOKEN) to mint captcha tokens, or a pasted
//     ZAI_CAPTCHA_PARAM.
func zaiTransport() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("ZAI_TRANSPORT")), "browserless") {
		return "browserless"
	}
	return "patchright"
}

// zaiRunCompletion dispatches to the configured transport.
func zaiRunCompletion(ctx context.Context, upstreamModel, prompt string, enableThinking bool, effort string) (*zaiStreamResult, error) {
	if zaiTransport() == "patchright" {
		return zaiRunBrowserTransport(ctx, upstreamModel, prompt, enableThinking, effort)
	}
	return zaiRunBrowserless(ctx, upstreamModel, prompt, enableThinking, effort)
}

// zaiRunBrowserless executes one completion turn without a local browser: mint
// session, solve captcha, sign, POST, and collect the SSE stream. On a
// FRONTEND_CAPTCHA_REQUIRED frame it re-solves the captcha and retries once.
// On 401 it re-mints the session.
func zaiRunBrowserless(ctx context.Context, upstreamModel, prompt string, enableThinking bool, effort string) (*zaiStreamResult, error) {
	sess, err := zaiGetSession(ctx)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		captcha, err := zaiSolveCaptcha(ctx)
		if err != nil {
			return nil, err
		}

		ts := strconv.FormatInt(time.Now().UnixMilli(), 10)
		requestID := generateUUID()
		body := zaiCompletionRequest{
			Stream:          true,
			Model:           upstreamModel,
			Messages:        []zaiMessage{{Role: "user", Content: prompt}},
			SignaturePrompt: prompt,
			Params:          map[string]any{},
			Extra:           map[string]any{},
			Features: map[string]any{
				"image_generation":      false,
				"web_search":            false,
				"auto_web_search":       false,
				"preview_mode":          true,
				"flags":                 []string{},
				"vlm_tools_enable":      false,
				"vlm_web_search_enable": false,
				"vlm_website_mode":      false,
				"enable_thinking":       enableThinking,
				// Without it the model reasons at length even with thinking off (see ext/zai_browser.py).
				"reasoning_effort": zaiEffortFor(enableThinking, effort),
			},
			ChatID:                 generateUUID(),
			ID:                     generateUUID(),
			CurrentUserMsgID:       generateUUID(),
			CurrentUserMsgParentID: nil,
			CaptchaVerifyParam:     captcha,
		}
		bodyBytes, _ := json.Marshal(body)

		headers := map[string]string{
			"authorization":   "Bearer " + sess.Token,
			"content-type":    "application/json",
			"accept-language": "en-US",
			"x-fe-version":    zaiFEVersion,
			"x-signature":     zaiSignature(prompt, ts, requestID, sess.UserID),
			"origin":          zaiAPIBase,
			"referer":         zaiAPIBase + "/",
			"user-agent":      cloudflareBrowserUserAgent,
		}
		urlStr := zaiAPIBase + zaiComplete + "?" + zaiBuildQuery(sess, ts, requestID)

		stream, err := zaiCurlCffiStream(ctx, urlStr, headers, bodyBytes)
		if err != nil {
			return nil, err
		}
		res, uerr := zaiCollectStream(stream)
		stream.Close()

		if uerr == nil {
			return res, nil
		}
		lastErr = uerr
		var up *zaiUpstreamError
		if attempt > 0 || !errors.As(uerr, &up) {
			break
		}
		// A pasted ZAI_CAPTCHA_PARAM is single-use; resending it cannot help.
		if up.CaptchaRequired && zaiExtEnabled() && zaiExt.isConnected() {
			continue
		}
		if up.Status == 401 {
			zaiInvalidateSession(sess)
			if sess, err = zaiGetSession(ctx); err != nil {
				return nil, fmt.Errorf("re-minting z.ai session after 401: %w", err)
			}
			continue
		}
		break
	}
	return nil, lastErr
}

// zaiCollectStream reads an SSE stream to completion, accumulating thinking and
// answer text and the usage frame. It returns the first upstream error frame.
func zaiCollectStream(r io.Reader) (*zaiStreamResult, error) {
	res := &zaiStreamResult{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" || payload == "" {
			continue
		}
		var ev zaiSSEEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		// Error frames, both flat and nested.
		if e := ev.Data.Error; e != nil && (e.Detail != "" || e.Code != "") {
			return res, &zaiUpstreamError{
				Status:          400,
				Detail:          e.Detail,
				Code:            e.Code,
				CaptchaRequired: e.Code == "FRONTEND_CAPTCHA_REQUIRED" || e.CaptchaErrorType != "",
			}
		}
		if nested := ev.Data.Data; nested != nil && nested.Error != nil {
			e := nested.Error
			return res, &zaiUpstreamError{
				Status:          400,
				Detail:          e.Detail,
				Code:            e.Code,
				CaptchaRequired: e.Code == "FRONTEND_CAPTCHA_REQUIRED" || e.CaptchaErrorType != "",
			}
		}
		switch ev.Data.Phase {
		case "thinking":
			res.Thinking += ev.Data.DeltaContent
		case "answer":
			res.Answer += ev.Data.DeltaContent
		}
		if ev.Data.Usage != nil {
			res.PromptTokens = ev.Data.Usage.PromptTokens
			res.CompletionTokens = ev.Data.Usage.CompletionTokens
		}
	}
	if err := scanner.Err(); err != nil {
		return res, err
	}
	return res, nil
}

// zaiMapModel resolves a public model id to an upstream id, defaulting to the
// guest tier. Unknown ids pass through (z.ai will 403 if the session can't use
// them, which surfaces a clear error).
func zaiMapModel(public string) string {
	if v, ok := zaiModelMap[strings.ToLower(strings.TrimSpace(public))]; ok {
		return v
	}
	if public == "" {
		return "x-preview-l"
	}
	return public
}

// zaiEffortFor is the reasoning_effort sent upstream: the client's level when
// thinking is on and one was given, otherwise the configured defaults.
func zaiEffortFor(enableThinking bool, effort string) string {
	if enableThinking && effort != "" {
		return effort
	}
	if enableThinking {
		return zaiEnvOr("ZAI_REASONING_EFFORT", "medium")
	}
	return zaiEnvOr("ZAI_REASONING_EFFORT_OFF", "low")
}

func zaiEnvOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
