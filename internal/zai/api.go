// Package zai is the chat.z.ai (GLM) provider, ported from goldenticket. It
// serves Kali Epsilon. Configuration is by ZAI_* environment variables (see
// zai.go and zai_pool.go); no static upstream credential is required.
package zai

import (
	"bytes"
	"io"
	"net/http"
)

// ServeChatCompletions handles an OpenAI chat-completions request whose JSON body
// has already been read (persona applied, model already a z.ai id such as
// "glm-5.3-flash"). Writes the complete SSE or JSON response to w. No auth.
func ServeChatCompletions(w http.ResponseWriter, r *http.Request, body []byte) {
	setCORSHeaders(w)
	zaiServeBody(w, r, body, &zaiChatFace{}, zaiParseChatRequest)
}

// ServeMessages is the Anthropic Messages face for an already-read body.
func ServeMessages(w http.ResponseWriter, r *http.Request, body []byte) {
	setCORSHeaders(w)
	zaiServeBody(w, r, body, &zaiAnthropicFace{}, zaiParseMessagesRequest)
}

// ServeResponses is the OpenAI Responses face for an already-read body.
func ServeResponses(w http.ResponseWriter, r *http.Request, body []byte) {
	setCORSHeaders(w)
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	handleZaiResponses(w, r2, "")
}

// Prewarm starts the browser pool in the background (no-op for browserless transport).
func Prewarm() {
	if zaiTransport() == "patchright" {
		zaiGetBrowserPool().prewarm()
	}
}

// Models lists the public model ids this provider accepts, in display order.
// Guests get only GLM-5.3 Flash; a signed-in account (ZAI_TOKEN,
// zai_config.json, or a provisioned pool account) unlocks GLM-5.3 and GLM-5.2.
func Models() []string {
	models := []string{"glm-5.3-flash"}
	if zaiAccountToken() != "" || (zaiGetAccountPool() != nil && zaiGetAccountPool().hasAccounts()) {
		models = append(models, "glm-5.3", "glm-5.2")
	}
	return models
}

// ExtHandler returns the captcha-extension WebSocket handler (nil when ZAI_EXT_TOKEN is unset).
func ExtHandler() http.Handler {
	if !zaiExtEnabled() {
		return nil
	}
	return http.HandlerFunc(zaiExt.serveSocket)
}
