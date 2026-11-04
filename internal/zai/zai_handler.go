package zai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"elichat/internal/auth"
	"elichat/internal/relay"
)

// Public routes for the chat.z.ai (GLM) provider. All three protocol faces
// share one path: parse -> one prompt (zai_conversation.go) -> one completion
// -> parse tool calls -> render. Streams commit headers at once and send a
// keepalive while the browser turn runs, because a silent connection made
// Claude Code abort and resend multi-minute turns.
//
//	POST /zai/v1/messages          Anthropic Messages
//	POST /zai/v1/chat/completions  OpenAI Chat Completions
//	POST /zai/v1/responses         OpenAI Responses

const zaiDefaultModel = "glm-5.3-flash"

// zaiStreamPingInterval is how often a stream writes a keepalive during a turn.
var zaiStreamPingInterval = 10 * time.Second

// zaiReply is a parsed completion ready to render.
type zaiReply struct {
	model string
	text  string
	calls []gensparkToolCall
	res   *zaiStreamResult
	// streamedText: a text block is already open on the stream, so finish
	// closes it even when no text remains to send.
	streamedText bool
}

// zaiAfterStreamed returns r with the already-streamed prefix removed from its
// text. Streaming sends a prefix of the raw answer, which the final text starts
// with up to leading whitespace.
func zaiAfterStreamed(r *zaiReply, streamed string) *zaiReply {
	out := *r
	out.streamedText = true
	out.text = strings.TrimPrefix(strings.TrimLeft(r.text, " \t\r\n"), strings.TrimLeft(streamed, " \t\r\n"))
	return &out
}

// zaiFace renders one public protocol.
type zaiFace interface {
	// start commits the stream preamble.
	start(w http.ResponseWriter, model string)
	ping(w http.ResponseWriter)
	// delta streams answer text before the turn ends; finish then sends only
	// what was not streamed.
	delta(w http.ResponseWriter, text string)
	// reasoning streams a thinking fragment; faces without a reasoning channel ignore it.
	reasoning(w http.ResponseWriter, text string)
	// finish writes the reply into the open stream.
	finish(w http.ResponseWriter, r *zaiReply)
	// fail reports an error after the stream preamble was committed.
	fail(w http.ResponseWriter, err error)
	writeJSON(w http.ResponseWriter, r *zaiReply)
}

// zaiRequest is the protocol-independent part of a parsed request.
type zaiRequest struct {
	model    string
	prompt   string
	thinking bool
	// effort is the client's reasoning level (z.ai's ladder: low, high, max) or "" for the default.
	effort string
	stream bool
	tools  map[string]bool // declared tool names, for the parser
}

// zaiEffort keeps a client reasoning level the upstream understands.
func zaiEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "high", "max":
		return strings.ToLower(strings.TrimSpace(effort))
	}
	return ""
}

func zaiToolNames(raw json.RawMessage) map[string]bool {
	names := map[string]bool{}
	for _, t := range zaiParseTools(raw) {
		names[t.Name] = true
	}
	return names
}

func handleZaiMessages(w http.ResponseWriter, r *http.Request, apiKey string) {
	zaiServe(w, r, apiKey, &zaiAnthropicFace{}, zaiParseMessagesRequest)
}

func zaiParseMessagesRequest(body []byte) (*zaiRequest, error) {
	var req struct {
		Model    string          `json:"model"`
		Messages json.RawMessage `json:"messages"`
		System   interface{}     `json:"system"`
		Tools    json.RawMessage `json:"tools"`
		Stream   bool            `json:"stream"`
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	prompt, err := zaiFlattenAnthropic(req.Messages, req.System, req.Tools)
	if err != nil {
		return nil, err
	}
	return &zaiRequest{model: req.Model, prompt: prompt, thinking: zaiThinkingEnabled(req.Thinking.Type, ""), stream: req.Stream, tools: zaiToolNames(req.Tools)}, nil
}

func handleZaiChatCompletions(w http.ResponseWriter, r *http.Request, apiKey string) {
	zaiServe(w, r, apiKey, &zaiChatFace{}, zaiParseChatRequest)
}

func zaiParseChatRequest(body []byte) (*zaiRequest, error) {
	var req struct {
		Model           string          `json:"model"`
		Messages        json.RawMessage `json:"messages"`
		Tools           json.RawMessage `json:"tools"`
		Stream          bool            `json:"stream"`
		ReasoningEffort string          `json:"reasoning_effort"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	prompt, err := zaiFlattenOpenAI(req.Messages, req.Tools)
	if err != nil {
		return nil, err
	}
	return &zaiRequest{model: req.Model, prompt: prompt, thinking: zaiThinkingEnabled("", req.ReasoningEffort), effort: zaiEffort(req.ReasoningEffort), stream: req.Stream, tools: zaiToolNames(req.Tools)}, nil
}

func handleZaiResponses(w http.ResponseWriter, r *http.Request, apiKey string) {
	zaiServe(w, r, apiKey, &zaiResponsesFace{}, func(body []byte) (*zaiRequest, error) {
		var req struct {
			Model        string          `json:"model"`
			Input        json.RawMessage `json:"input"`
			Instructions string          `json:"instructions"`
			Tools        json.RawMessage `json:"tools"`
			Stream       bool            `json:"stream"`
			Reasoning    struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		chat, err := zaiResponsesInputToChat(req.Input)
		if err != nil {
			return nil, err
		}
		if req.Instructions != "" {
			chat = append([]map[string]interface{}{{"role": "system", "content": req.Instructions}}, chat...)
		}
		raw, _ := json.Marshal(chat)
		prompt, err := zaiFlattenOpenAI(raw, req.Tools)
		if err != nil {
			return nil, err
		}
		return &zaiRequest{model: req.Model, prompt: prompt, thinking: zaiThinkingEnabled("", req.Reasoning.Effort), effort: zaiEffort(req.Reasoning.Effort), stream: req.Stream, tools: zaiToolNames(req.Tools)}, nil
	})
}

// zaiServe reads the request body and serves it through zaiServeBody. The
// apiKey argument is unused: elichat authenticates before calling in.
func zaiServe(w http.ResponseWriter, r *http.Request, _ string, face zaiFace, parse func([]byte) (*zaiRequest, error)) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, err := readLimitedBody(w, r, 4<<20)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, APIErrorDetail{Message: err.Error(), Type: "invalid_request_error", Code: "request_too_large"})
			return
		}
		writeAPIError(w, http.StatusBadRequest, APIErrorDetail{Message: "reading request body: " + err.Error(), Type: "invalid_request_error", Code: "invalid_request"})
		return
	}
	zaiServeBody(w, r, body, face, parse)
}

// zaiServeBody is the shared request path for every face, given an already-read body.
func zaiServeBody(w http.ResponseWriter, r *http.Request, body []byte, face zaiFace, parse func([]byte) (*zaiRequest, error)) {
	req, err := parse(body)
	if err != nil {
		zaiDump(r, body, http.StatusBadRequest, err)
		writeAPIError(w, http.StatusBadRequest, APIErrorDetail{Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"})
		return
	}
	if req.model == "" {
		req.model = zaiDefaultModel
	}

	ctx, cancel := context.WithTimeout(r.Context(), zaiRequestTimeout)
	defer cancel()
	deltas := make(chan string, 64)
	thinks := make(chan string, 64)
	if req.stream {
		ctx = zaiWithDeltas(ctx, func(s string) {
			select {
			case deltas <- s:
			case <-ctx.Done():
			}
		})
	}
	// The upstream reasons regardless of enable_thinking for some models; a
	// client that switched thinking off never sees that reasoning either way.
	if req.stream && req.thinking {
		ctx = zaiWithThinking(ctx, func(s string) {
			select {
			case thinks <- s:
			case <-ctx.Done():
			}
		})
	}
	run := func() (*zaiReply, error) {
		res, err := zaiRunCompletionFunc(ctx, zaiMapModel(req.model), req.prompt, req.thinking, req.effort)
		if err != nil {
			return nil, err
		}
		text, calls := zaiParseToolCallsFor(zaiStripWrapper(res.Answer), req.tools)
		calls = zaiKnownCalls(calls, req.tools)
		if len(calls) == 0 {
			text = strings.TrimSpace(res.Answer)
		}
		if res.PromptTokens == 0 {
			res.PromptTokens = estimateTokens(len(req.prompt))
		}
		if res.CompletionTokens == 0 {
			res.CompletionTokens = estimateTokens(len(res.Answer))
		}
		return &zaiReply{model: req.model, text: text, calls: calls, res: res}, nil
	}

	if !req.stream {
		reply, err := run()
		if err != nil {
			status, _ := zaiErrorStatus(err)
			zaiDump(r, body, status, err)
			zaiWriteUpstreamError(w, err)
			return
		}
		face.writeJSON(w, reply)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	face.start(w, req.model)
	flush()

	type outcome struct {
		reply *zaiReply
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		// net/http recovers handler panics, not panics in goroutines it spawns.
		defer func() {
			if p := recover(); p != nil {
				logf("event=zai_turn_panic err=%v", p)
				done <- outcome{nil, fmt.Errorf("internal error: %v", p)}
			}
		}()
		reply, err := run()
		done <- outcome{reply, err}
	}()
	ticker := time.NewTicker(zaiStreamPingInterval)
	defer ticker.Stop()
	for {
		select {
		case s := <-thinks:
			face.reasoning(w, s)
			flush()
		case s := <-deltas:
			face.delta(w, s)
			flush()
		case out := <-done:
			// run() returns only after its last delta send, so drain before finishing.
			for len(thinks) > 0 {
				face.reasoning(w, <-thinks)
			}
			for len(deltas) > 0 {
				face.delta(w, <-deltas)
			}
			if out.err != nil {
				status, _ := zaiErrorStatus(out.err)
				zaiDump(r, body, status, out.err)
				face.fail(w, out.err)
			} else {
				face.finish(w, out.reply)
			}
			flush()
			return
		case <-ticker.C:
			face.ping(w)
			flush()
		case <-r.Context().Done():
			// The client is gone; run() sees the same cancellation and stops the browser turn.
			return
		}
	}
}

// readLimitedBody reads at most limit bytes of the request body.
func readLimitedBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
}

// zaiThinkingEnabled resolves enable_thinking. A set ZAI_ENABLE_THINKING is an
// operator override: Claude Code always asks for thinking, and GLM thinking on
// a ~100KB agent prompt ran 2-5 minutes per turn versus 10-40s without it.
// Otherwise the client decides; the default is on.
func zaiThinkingEnabled(anthropicType, effort string) bool {
	if strings.TrimSpace(os.Getenv("ZAI_ENABLE_THINKING")) != "" {
		return zaiEnvBool("ZAI_ENABLE_THINKING", true)
	}
	if strings.EqualFold(strings.TrimSpace(anthropicType), "disabled") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal":
		return false
	}
	return true
}

// zaiErrorStatus maps a provider error to the HTTP status and error type clients act on.
func zaiErrorStatus(err error) (int, string) {
	var up *zaiUpstreamError
	switch {
	case errors.As(err, &up) && up.Status == http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "rate_limit_error"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "overloaded_error"
	}
	return http.StatusBadGateway, "api_error"
}

func zaiErrorCode(err error) string {
	var up *zaiUpstreamError
	if errors.As(err, &up) && up.CaptchaRequired {
		return "zai_captcha_failed"
	}
	return "zai_upstream_error"
}

// zaiDump records a failed Epsilon request: the body the relay received (after
// the persona), the z.ai status, detail and code when the error came from
// upstream, and the client's request ids.
func zaiDump(r *http.Request, body []byte, status int, err error) {
	var user string
	var id int
	if u := auth.UserFrom(r.Context()); u != nil {
		user, id = u.Username, u.ID
	}
	upstreamStatus := status
	var upstreamBody []byte
	var up *zaiUpstreamError
	if errors.As(err, &up) {
		if up.Status != 0 {
			upstreamStatus = up.Status
		}
		upstreamBody, _ = json.Marshal(map[string]any{"status": up.Status, "code": up.Code, "detail": up.Detail, "captcha_required": up.CaptchaRequired})
	}
	dialect := strings.TrimPrefix(r.URL.Path, "/v1/")
	relay.DumpFailure(r, user, id, "zai", dialect, zaiAPIBase+zaiComplete, body, upstreamStatus, nil, upstreamBody, err)
}

func zaiWriteUpstreamError(w http.ResponseWriter, err error) {
	status, typ := zaiErrorStatus(err)
	writeAPIError(w, status, APIErrorDetail{Message: err.Error(), Type: typ, Code: zaiErrorCode(err)})
}

// ---------------------------------------------------------------------------
// Anthropic Messages
// ---------------------------------------------------------------------------

type zaiAnthropicFace struct {
	streamed string // text already sent in block 0
}

func (f *zaiAnthropicFace) delta(w http.ResponseWriter, text string) {
	if f.streamed == "" {
		writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
	}
	f.streamed += text
	writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": 0, "delta": map[string]interface{}{"type": "text_delta", "text": text}})
}

func (*zaiAnthropicFace) start(w http.ResponseWriter, model string) {
	writeSSE(w, "message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id": "msg_" + generateUUID(), "type": "message", "role": "assistant", "model": model,
			"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
}

func (*zaiAnthropicFace) ping(w http.ResponseWriter) {
	writeSSE(w, "ping", map[string]string{"type": "ping"})
}

// fail sends the Anthropic error event. Like the real API it ends the stream
// there: no message_stop follows an error event.
func (*zaiAnthropicFace) fail(w http.ResponseWriter, err error) {
	_, typ := zaiErrorStatus(err)
	writeSSE(w, "error", map[string]interface{}{
		"type": "error", "error": map[string]string{"type": typ, "message": err.Error()},
	})
}

func (*zaiAnthropicFace) blocks(r *zaiReply) []map[string]interface{} {
	var blocks []map[string]interface{}
	if r.text != "" || len(r.calls) == 0 || r.streamedText {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": r.text})
	}
	for _, tc := range r.calls {
		blocks = append(blocks, map[string]interface{}{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": tc.Input})
	}
	return blocks
}

func zaiStopReason(r *zaiReply) string {
	if len(r.calls) > 0 {
		return "tool_use"
	}
	return "end_turn"
}

func (f *zaiAnthropicFace) finish(w http.ResponseWriter, r *zaiReply) {
	if f.streamed != "" {
		r = zaiAfterStreamed(r, f.streamed)
	}
	for i, b := range f.blocks(r) {
		if b["type"] == "text" {
			if f.streamed == "" {
				writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": i, "content_block": map[string]string{"type": "text", "text": ""}})
			}
			if t, _ := b["text"].(string); t != "" || f.streamed == "" {
				writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": i, "delta": map[string]interface{}{"type": "text_delta", "text": t}})
			}
		} else {
			writeSSE(w, "content_block_start", map[string]interface{}{"type": "content_block_start", "index": i,
				"content_block": map[string]interface{}{"type": "tool_use", "id": b["id"], "name": b["name"], "input": map[string]interface{}{}}})
			writeSSE(w, "content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": i,
				"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": string(b["input"].(json.RawMessage))}})
		}
		writeSSE(w, "content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": i})
	}
	writeSSE(w, "message_delta", map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": zaiStopReason(r), "stop_sequence": nil},
		"usage": map[string]int{"input_tokens": r.res.PromptTokens, "output_tokens": r.res.CompletionTokens},
	})
	writeSSE(w, "message_stop", map[string]string{"type": "message_stop"})
}

func (f *zaiAnthropicFace) writeJSON(w http.ResponseWriter, r *zaiReply) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": "msg_" + generateUUID(), "type": "message", "role": "assistant", "model": r.model,
		"content": f.blocks(r), "stop_reason": zaiStopReason(r), "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": r.res.PromptTokens, "output_tokens": r.res.CompletionTokens},
	})
}

// ---------------------------------------------------------------------------
// OpenAI Chat Completions
// ---------------------------------------------------------------------------

type zaiChatFace struct {
	id       string
	created  int64
	model    string
	streamed string
}

func (f *zaiChatFace) delta(w http.ResponseWriter, text string) {
	f.streamed += text
	f.chunk(w, map[string]interface{}{"content": text}, nil, nil)
}

func (f *zaiChatFace) reasoning(w http.ResponseWriter, text string) {
	f.chunk(w, map[string]interface{}{"reasoning_content": text}, nil, nil)
}

func (*zaiAnthropicFace) reasoning(http.ResponseWriter, string) {}
func (*zaiResponsesFace) reasoning(http.ResponseWriter, string) {}

func (f *zaiChatFace) chunk(w http.ResponseWriter, delta map[string]interface{}, finish interface{}, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"id": f.id, "object": "chat.completion.chunk", "created": f.created, "model": f.model,
		"choices": []map[string]interface{}{{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	for k, v := range extra {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func (f *zaiChatFace) start(w http.ResponseWriter, model string) {
	f.id, f.created, f.model = "chatcmpl-"+generateUUID(), time.Now().Unix(), model
	f.chunk(w, map[string]interface{}{"role": "assistant", "content": ""}, nil, nil)
}

// ping writes an SSE comment, which OpenAI clients ignore.
func (f *zaiChatFace) ping(w http.ResponseWriter) { fmt.Fprint(w, ": keepalive\n\n") }

func (f *zaiChatFace) fail(w http.ResponseWriter, err error) {
	_, typ := zaiErrorStatus(err)
	b, _ := json.Marshal(map[string]interface{}{"error": map[string]string{"message": err.Error(), "type": typ, "code": zaiErrorCode(err)}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", b)
}

// zaiOpenAICallID gives OpenAI clients the call_ prefix they expect.
func zaiOpenAICallID(id string) string {
	return "call_" + strings.TrimPrefix(id, "toolu_")
}

func zaiChatToolCalls(calls []gensparkToolCall) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(calls))
	for i, tc := range calls {
		out = append(out, map[string]interface{}{
			"index": i, "id": zaiOpenAICallID(tc.ID), "type": "function",
			"function": map[string]string{"name": tc.Name, "arguments": string(tc.Input)},
		})
	}
	return out
}

func zaiFinishReason(r *zaiReply) string {
	if len(r.calls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func zaiChatUsage(r *zaiReply) map[string]int {
	return map[string]int{"prompt_tokens": r.res.PromptTokens, "completion_tokens": r.res.CompletionTokens,
		"total_tokens": r.res.PromptTokens + r.res.CompletionTokens}
}

func (f *zaiChatFace) finish(w http.ResponseWriter, r *zaiReply) {
	if f.streamed != "" {
		r = zaiAfterStreamed(r, f.streamed)
	}
	if r.text != "" {
		f.chunk(w, map[string]interface{}{"content": r.text}, nil, nil)
	}
	if len(r.calls) > 0 {
		f.chunk(w, map[string]interface{}{"tool_calls": zaiChatToolCalls(r.calls)}, nil, nil)
	}
	f.chunk(w, map[string]interface{}{}, zaiFinishReason(r), map[string]interface{}{"usage": zaiChatUsage(r)})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func (f *zaiChatFace) writeJSON(w http.ResponseWriter, r *zaiReply) {
	message := map[string]interface{}{"role": "assistant", "content": r.text}
	if len(r.calls) > 0 {
		message["tool_calls"] = zaiChatToolCalls(r.calls)
		if r.text == "" {
			message["content"] = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"id": "chatcmpl-" + generateUUID(), "object": "chat.completion", "created": time.Now().Unix(), "model": r.model,
		"choices": []map[string]interface{}{{"index": 0, "message": message, "finish_reason": zaiFinishReason(r)}},
		"usage":   zaiChatUsage(r),
	})
}

// ---------------------------------------------------------------------------
// OpenAI Responses
// ---------------------------------------------------------------------------

type zaiResponsesFace struct {
	id       string
	created  int64
	model    string
	seq      int
	streamed string
	msgID    string // the message item opened by the first delta
}

func (f *zaiResponsesFace) delta(w http.ResponseWriter, text string) {
	if f.msgID == "" {
		f.msgID = "msg_" + generateUUID()
		f.emit(w, "response.output_item.added", map[string]interface{}{"output_index": 0,
			"item": map[string]interface{}{"type": "message", "id": f.msgID, "role": "assistant", "status": "in_progress", "content": []interface{}{}}})
		f.emit(w, "response.content_part.added", map[string]interface{}{"item_id": f.msgID, "output_index": 0, "content_index": 0,
			"part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}}})
	}
	f.streamed += text
	f.emit(w, "response.output_text.delta", map[string]interface{}{"item_id": f.msgID, "output_index": 0, "content_index": 0, "delta": text})
}

func (f *zaiResponsesFace) emit(w http.ResponseWriter, event string, data map[string]interface{}) {
	data["type"] = event
	data["sequence_number"] = f.seq
	f.seq++
	writeSSE(w, event, data)
}

func (f *zaiResponsesFace) response(status string, output []map[string]interface{}, usage map[string]interface{}) map[string]interface{} {
	if output == nil {
		output = []map[string]interface{}{}
	}
	resp := map[string]interface{}{
		"id": f.id, "object": "response", "created_at": f.created, "model": f.model,
		"status": status, "output": output,
	}
	if usage != nil {
		resp["usage"] = usage
	}
	return resp
}

func (f *zaiResponsesFace) start(w http.ResponseWriter, model string) {
	f.id, f.created, f.model = "resp_"+generateUUID(), time.Now().Unix(), model
	f.emit(w, "response.created", map[string]interface{}{"response": f.response("in_progress", nil, nil)})
	f.emit(w, "response.in_progress", map[string]interface{}{"response": f.response("in_progress", nil, nil)})
}

func (f *zaiResponsesFace) ping(w http.ResponseWriter) { fmt.Fprint(w, ": keepalive\n\n") }

func (f *zaiResponsesFace) fail(w http.ResponseWriter, err error) {
	_, typ := zaiErrorStatus(err)
	resp := f.response("failed", nil, nil)
	resp["error"] = map[string]string{"code": typ, "message": err.Error()}
	f.emit(w, "response.failed", map[string]interface{}{"response": resp})
}

func zaiResponsesUsage(r *zaiReply) map[string]interface{} {
	return map[string]interface{}{
		"input_tokens": r.res.PromptTokens, "output_tokens": r.res.CompletionTokens,
		"total_tokens":          r.res.PromptTokens + r.res.CompletionTokens,
		"input_tokens_details":  map[string]int{"cached_tokens": 0},
		"output_tokens_details": map[string]int{"reasoning_tokens": 0},
	}
}

func zaiResponsesOutput(r *zaiReply) []map[string]interface{} {
	var out []map[string]interface{}
	if r.text != "" || len(r.calls) == 0 {
		out = append(out, map[string]interface{}{
			"type": "message", "id": "msg_" + generateUUID(), "role": "assistant", "status": "completed",
			"content": []map[string]interface{}{{"type": "output_text", "text": r.text, "annotations": []interface{}{}}},
		})
	}
	for _, tc := range r.calls {
		out = append(out, map[string]interface{}{
			"type": "function_call", "id": "fc_" + generateUUID(), "call_id": zaiOpenAICallID(tc.ID),
			"name": tc.Name, "arguments": string(tc.Input), "status": "completed",
		})
	}
	return out
}

func (f *zaiResponsesFace) finish(w http.ResponseWriter, r *zaiReply) {
	rest := r.text
	if f.streamed != "" {
		rest = zaiAfterStreamed(r, f.streamed).text
		r = &zaiReply{model: r.model, text: f.streamed + rest, calls: r.calls, res: r.res}
	}
	output := zaiResponsesOutput(r)
	for i, item := range output {
		base := map[string]interface{}{"output_index": i}
		if item["type"] == "message" {
			text := r.text
			if f.msgID != "" {
				item["id"] = f.msgID
			}
			part := map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}}
			ids := map[string]interface{}{"item_id": item["id"], "output_index": i, "content_index": 0}
			if f.msgID == "" {
				f.emit(w, "response.output_item.added", map[string]interface{}{"output_index": i,
					"item": map[string]interface{}{"type": "message", "id": item["id"], "role": "assistant", "status": "in_progress", "content": []interface{}{}}})
				f.emit(w, "response.content_part.added", zaiWith(ids, "part", part))
			}
			if rest != "" || f.msgID == "" {
				f.emit(w, "response.output_text.delta", zaiWith(ids, "delta", rest))
			}
			f.emit(w, "response.output_text.done", zaiWith(ids, "text", text))
			f.emit(w, "response.content_part.done", zaiWith(ids, "part",
				map[string]interface{}{"type": "output_text", "text": text, "annotations": []interface{}{}}))
		} else {
			pending := zaiWith(item, "status", "in_progress")
			pending["arguments"] = ""
			f.emit(w, "response.output_item.added", zaiWith(base, "item", pending))
			ids := map[string]interface{}{"item_id": item["id"], "output_index": i}
			f.emit(w, "response.function_call_arguments.delta", zaiWith(ids, "delta", item["arguments"]))
			f.emit(w, "response.function_call_arguments.done", zaiWith(ids, "arguments", item["arguments"]))
		}
		f.emit(w, "response.output_item.done", zaiWith(base, "item", item))
	}
	f.emit(w, "response.completed", map[string]interface{}{"response": f.response("completed", output, zaiResponsesUsage(r))})
}

func (f *zaiResponsesFace) writeJSON(w http.ResponseWriter, r *zaiReply) {
	f.id, f.created, f.model = "resp_"+generateUUID(), time.Now().Unix(), r.model
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.response("completed", zaiResponsesOutput(r), zaiResponsesUsage(r)))
}

// zaiWith returns a copy of m with key set to v.
func zaiWith(m map[string]interface{}, key string, v interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+1)
	for k, val := range m {
		out[k] = val
	}
	out[key] = v
	return out
}

// zaiResponsesInputToChat converts Responses `input` (string or item array) to
// OpenAI chat messages.
func zaiResponsesInputToChat(input json.RawMessage) ([]map[string]interface{}, error) {
	if len(input) == 0 {
		return nil, errors.New("responses input is empty")
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return []map[string]interface{}{{"role": "user", "content": s}}, nil
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, fmt.Errorf("parse responses input: %w", err)
	}
	var out []map[string]interface{}
	for _, item := range items {
		typ, role := stringValue(item["type"]), stringValue(item["role"])
		switch {
		case typ == "message" || (typ == "" && role != ""):
			if role == "" {
				role = "user"
			}
			out = append(out, map[string]interface{}{"role": role, "content": item["content"]})
		case typ == "function_call":
			call := map[string]interface{}{
				"id": stringValue(item["call_id"]), "type": "function",
				"function": map[string]interface{}{"name": stringValue(item["name"]), "arguments": stringValue(item["arguments"])},
			}
			// Parallel calls arrive as consecutive items; keep them on one assistant turn.
			if n := len(out); n > 0 && out[n-1]["role"] == "assistant" && out[n-1]["tool_calls"] != nil {
				out[n-1]["tool_calls"] = append(out[n-1]["tool_calls"].([]map[string]interface{}), call)
				continue
			}
			out = append(out, map[string]interface{}{"role": "assistant", "tool_calls": []map[string]interface{}{call}})
		case typ == "function_call_output":
			out = append(out, map[string]interface{}{
				"role": "tool", "tool_call_id": stringValue(item["call_id"]), "content": item["output"],
			})
		case typ == "reasoning" || typ == "item_reference":
			// Prior reasoning and server-side references carry nothing z.ai can use.
		default:
			return nil, fmt.Errorf("unsupported responses input item type %q", typ)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("responses input produced no messages")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Tool-call parsing
// ---------------------------------------------------------------------------

var zaiFunctionCallTag = regexp.MustCompile(`(</?)function_call>`)

// zaiStripWrapper trims narration GLM appends after its last closing tag.
// The prompt asks for <function_call>: <tool_call> is GLM's own template
// token, and with it the native tool loop fired about twice as often (A/B).
func zaiStripWrapper(content string) string {
	end := -1
	for _, tag := range []string{"</function_call>", "</tool_call>"} {
		if idx := strings.LastIndex(content, tag); idx >= 0 && idx+len(tag) > end {
			end = idx + len(tag)
		}
	}
	// Keep a tail that holds more calls; only narration is dropped.
	if end >= 0 && !strings.Contains(content[end:], `{"name"`) {
		content = content[:end]
	}
	return strings.TrimSpace(content)
}

// zaiParseToolCallsFor extracts tool calls from GLM output: tagged blocks
// (<function_call> or <tool_call>) first, then {"name","arguments"} objects
// anywhere. Flattened calls ({"name": "Agent", "prompt": ...}, no "arguments";
// a five-Agent fan-out came back this way, live) count only for a name in known.
func zaiParseToolCallsFor(content string, known map[string]bool) (textBefore string, calls []gensparkToolCall) {
	tagged := zaiFunctionCallTag.ReplaceAllString(content, "${1}tool_call>")
	if textBefore, calls = parseToolCalls(tagged); len(calls) > 0 {
		// GLM sometimes drops the tags partway through a fan-out.
		if i := strings.LastIndex(tagged, "</tool_call>"); i >= 0 {
			if _, more := zaiParseBareCalls(tagged[i+len("</tool_call>"):], known); len(more) > 0 {
				calls = append(calls, more...)
			}
		}
		return textBefore, calls
	}
	return zaiParseBareCalls(content, known)
}

// zaiParseBareCalls finds {"name","arguments"} objects anywhere in content.
func zaiParseBareCalls(content string, known map[string]bool) (textBefore string, calls []gensparkToolCall) {
	// Escape from the first call on only: quotes in leading prose would throw
	// off the string tracking.
	if i := strings.Index(content, `{"name"`); i >= 0 {
		content = content[:i] + zaiEscapeControlInStrings(content[i:])
	}
	firstCall := -1
	for i := 0; i < len(content); i++ {
		if content[i] != '{' {
			continue
		}
		var fc struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		dec := json.NewDecoder(strings.NewReader(content[i:]))
		consumed := 0
		if err := dec.Decode(&fc); err == nil {
			consumed = int(dec.InputOffset())
		} else if n, ok := zaiRepairCallObject(content[i:], &fc.Name, &fc.Arguments); ok {
			consumed = n
		} else {
			continue
		}
		if fc.Name != "" && len(fc.Arguments) == 0 && known[fc.Name] {
			fc.Arguments = zaiFlattenedArguments(content[i : i+consumed])
		}
		// Require "arguments": prose like `{"name": "Alice"}` is not a call.
		// Skip the whole object, so nested objects inside it are never taken for calls.
		if fc.Name == "" || len(fc.Arguments) == 0 {
			i += consumed - 1
			continue
		}
		if firstCall < 0 {
			firstCall = i
		}
		if string(fc.Arguments) == "null" {
			fc.Arguments = json.RawMessage(`{}`)
		}
		calls = append(calls, gensparkToolCall{ID: "toolu_" + generateUUID(), Name: fc.Name, Input: fc.Arguments})
		i += consumed - 1
	}
	if len(calls) == 0 {
		return content, nil
	}
	return strings.TrimSpace(content[:firstCall]), calls
}

// zaiKnownCalls drops calls to tools the request did not declare. Audits that
// quote code made GLM's reply contain JSON such as {"name": "x", "arguments":
// ...}, which became calls to "x", "Alice" and "..." (8 in 8 transcripts, live).
func zaiKnownCalls(calls []gensparkToolCall, known map[string]bool) []gensparkToolCall {
	if len(known) == 0 {
		return calls
	}
	kept := calls[:0]
	for _, c := range calls {
		if known[c.Name] {
			kept = append(kept, c)
		} else {
			logf("event=zai_unknown_tool_dropped name=%q", truncate(c.Name, 60))
		}
	}
	return kept
}

// zaiFlattenedArguments returns obj without its "name" key, as the call's
// arguments, or nil if obj does not decode.
func zaiFlattenedArguments(obj string) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(obj), &fields) != nil {
		return nil
	}
	delete(fields, "name")
	args, _ := json.Marshal(fields)
	return args
}

// zaiEscapeControlInStrings escapes raw control characters inside JSON string
// literals. GLM writes real newlines inside long "prompt" arguments (seen live
// in a five-call Agent fan-out), which strict JSON rejects.
func zaiEscapeControlInStrings(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString, escaped := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case inString && c == '\\':
			escaped = true
		case c == '"':
			inString = !inString
		case inString && c < 0x20:
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// zaiTrimCallSeparator drops whitespace between concatenated calls, including
// the literal two-character escapes `\n`/`\t` GLM sometimes writes there.
func zaiTrimCallSeparator(s string) string {
	for {
		t := strings.TrimLeft(s, " \t\r\n")
		for _, esc := range []string{`\n`, `\r`, `\t`} {
			t = strings.TrimPrefix(t, esc)
		}
		if t == s {
			return s
		}
		s = t
	}
}

// zaiRepairCallObject recovers a call GLM wrote without its outer closing
// brace, as it does when writing several calls back to back:
//
//	{"name":"Agent","arguments":{...}{"name":"Agent",...
//
// The call is accepted when "arguments" is followed by end of input, a stray
// closing brace, or another call (possibly after junk tokens, seen live in a
// four-call fan-out). It returns how many bytes of s the call used.
func zaiRepairCallObject(s string, name *string, args *json.RawMessage) (int, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, false
	}
	var gotName, gotArgs bool
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return 0, false
		}
		switch key {
		case "name":
			gotName = dec.Decode(name) == nil
		case "arguments":
			gotArgs = dec.Decode(args) == nil
		default:
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return 0, false
			}
		}
		if !gotName && key == "name" || !gotArgs && key == "arguments" {
			return 0, false
		}
		if gotName && gotArgs {
			end := int(dec.InputOffset())
			rest := zaiTrimCallSeparator(s[end:])
			switch {
			case rest == "" || strings.HasPrefix(rest, "{"):
				return end, true
			case strings.HasPrefix(rest, "}"):
				return len(s) - len(rest) + 1, true
			case strings.Contains(rest, `{"name"`):
				return end, true
			}
			return 0, false
		}
	}
	return 0, false
}
