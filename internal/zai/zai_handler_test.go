package zai

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// zaiTestServer serves all three faces over a real HTTP server with the
// completion stubbed out, and records the prompt and thinking flag it was given.
type zaiTestServer struct {
	url      string
	prompt   string
	thinking bool
}

func newZaiTestServer(t *testing.T, run func(ctx context.Context) (*zaiStreamResult, error)) *zaiTestServer {
	t.Helper()
	ts := &zaiTestServer{}
	oldRun, oldPing := zaiRunCompletionFunc, zaiStreamPingInterval
	zaiStreamPingInterval = 50 * time.Millisecond
	zaiRunCompletionFunc = func(ctx context.Context, _ string, prompt string, thinking bool, _ string) (*zaiStreamResult, error) {
		ts.prompt, ts.thinking = prompt, thinking
		return run(ctx)
	}
	t.Cleanup(func() { zaiRunCompletionFunc, zaiStreamPingInterval = oldRun, oldPing })
	mux := http.NewServeMux()
	mux.HandleFunc("/zai/v1/messages", func(w http.ResponseWriter, r *http.Request) { handleZaiMessages(w, r, "k") })
	mux.HandleFunc("/zai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { handleZaiChatCompletions(w, r, "k") })
	mux.HandleFunc("/zai/v1/responses", func(w http.ResponseWriter, r *http.Request) { handleZaiResponses(w, r, "k") })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ts.url = srv.URL
	return ts
}

func zaiAnswer(answer string) func(context.Context) (*zaiStreamResult, error) {
	return func(context.Context) (*zaiStreamResult, error) {
		return &zaiStreamResult{Answer: answer, PromptTokens: 9, CompletionTokens: 3}, nil
	}
}

func zaiPost(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("X-Api-Key", "k")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func zaiDecode(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var out map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("status %d body %s: %v", resp.StatusCode, b, err)
	}
	return out
}

// zaiSSE returns event names (or "data"/"comment" for unnamed frames) and all data payloads.
func zaiSSE(t *testing.T, resp *http.Response) (events []string, data []string) {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	named := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			events, named = append(events, strings.TrimPrefix(line, "event: ")), true
		case strings.HasPrefix(line, "data: "):
			if !named {
				events = append(events, "data")
			}
			data, named = append(data, strings.TrimPrefix(line, "data: ")), false
		case strings.HasPrefix(line, ":"):
			events = append(events, "comment")
		}
	}
	return events, data
}

func count(items []string, want string) int {
	n := 0
	for _, s := range items {
		if s == want {
			n++
		}
	}
	return n
}

const zaiToolCallAnswer = "Let me check.\n<tool_call>\n{\"name\":\"get_weather\",\"arguments\":{\"city\":\"NYC\"}}\n</tool_call>"

// zaiStreamedAnswer emits deltas through the context, as the browser transport
// does, and then returns the full answer.
func zaiStreamedAnswer(deltas []string, answer string) func(context.Context) (*zaiStreamResult, error) {
	return func(ctx context.Context) (*zaiStreamResult, error) {
		if fn := zaiDeltaFunc(ctx); fn != nil {
			for _, d := range deltas {
				fn(d)
			}
		}
		return &zaiStreamResult{Answer: answer, PromptTokens: 9, CompletionTokens: 3}, nil
	}
}

// Streamed text must arrive once on every face: deltas first, then only the
// rest, and the reconstructed text equals the full answer.
func TestZaiStreamDeltasAllFaces(t *testing.T) {
	ts := newZaiTestServer(t, zaiStreamedAnswer([]string{"Hello, ", "wor"}, "Hello, world."))
	msg := `"messages":[{"role":"user","content":"hi"}]`

	_, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/messages", `{"stream":true,`+msg+`}`))
	var text strings.Builder
	starts := 0
	for _, d := range data {
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
		}
		_ = json.Unmarshal([]byte(d), &ev)
		if ev.Type == "content_block_delta" {
			text.WriteString(ev.Delta.Text)
		}
		if ev.Type == "content_block_start" {
			starts++
		}
	}
	if text.String() != "Hello, world." || starts != 1 {
		t.Fatalf("anthropic text=%q starts=%d", text.String(), starts)
	}

	_, data = zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/chat/completions", `{"stream":true,`+msg+`}`))
	text.Reset()
	for _, d := range data {
		var ch struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(d), &ch) == nil && len(ch.Choices) > 0 {
			text.WriteString(ch.Choices[0].Delta.Content)
		}
	}
	if text.String() != "Hello, world." {
		t.Fatalf("chat text=%q", text.String())
	}

	events, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/responses", `{"stream":true,"input":"hi"}`))
	text.Reset()
	var final string
	for _, d := range data {
		var ev struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Response struct {
				Output []struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		_ = json.Unmarshal([]byte(d), &ev)
		if ev.Type == "response.output_text.delta" {
			text.WriteString(ev.Delta)
		}
		if ev.Type == "response.completed" && len(ev.Response.Output) > 0 {
			final = ev.Response.Output[0].Content[0].Text
		}
	}
	if text.String() != "Hello, world." || final != "Hello, world." || count(events, "response.output_item.added") != 1 {
		t.Fatalf("responses deltas=%q final=%q events=%v", text.String(), final, events)
	}
}

// A streamed prose prefix followed by a tool call keeps the text block and
// still delivers the call.
func TestZaiStreamDeltasThenToolCall(t *testing.T) {
	ts := newZaiTestServer(t, zaiStreamedAnswer([]string{"Let me "}, zaiToolCallAnswer))
	_, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/messages",
		`{"stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}]}`))
	all := strings.Join(data, "\n")
	if !strings.Contains(all, `"name":"get_weather"`) || !strings.Contains(all, `"stop_reason":"tool_use"`) || strings.Count(all, `"index":0`) < 2 {
		t.Fatalf("stream: %s", all)
	}
	if strings.Contains(all, "<tool_call>") {
		t.Fatalf("call markup leaked as text: %s", all)
	}
}

func TestZaiMessagesJSONToolCall(t *testing.T) {
	ts := newZaiTestServer(t, zaiAnswer(zaiToolCallAnswer))
	resp := zaiPost(t, ts.url+"/zai/v1/messages",
		`{"model":"glm-5.3-flash","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"weather?"}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}]}`)
	out := zaiDecode(t, resp)
	if out["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v", out["stop_reason"])
	}
	content := out["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["text"] != "Let me check." ||
		content[1].(map[string]any)["name"] != "get_weather" {
		t.Errorf("content = %v", content)
	}
	if ts.thinking {
		t.Errorf("thinking.type=disabled must turn thinking off")
	}
	if !strings.Contains(ts.prompt, "### get_weather()") {
		t.Errorf("catalog not in prompt")
	}
}

// A slow turn must commit message_start at once and ping until the answer
// lands, or Claude Code times out the silent connection and resends the turn.
func TestZaiMessagesStreamPingsWhileTurnRuns(t *testing.T) {
	ts := newZaiTestServer(t, func(context.Context) (*zaiStreamResult, error) {
		time.Sleep(300 * time.Millisecond)
		return &zaiStreamResult{Answer: `{"name":"Read","arguments":{"file_path":"/x"}}`}, nil
	})
	start := time.Now()
	resp := zaiPost(t, ts.url+"/zai/v1/messages", `{"stream":true,"messages":[{"role":"user","content":"read"}]}`)
	if time.Since(start) > 200*time.Millisecond {
		t.Fatalf("headers not committed before the turn finished: %v", time.Since(start))
	}
	events, data := zaiSSE(t, resp)
	if events[0] != "message_start" || events[len(events)-1] != "message_stop" || count(events, "ping") < 2 {
		t.Fatalf("events = %v", events)
	}
	all := strings.Join(data, "\n")
	if !strings.Contains(all, `"stop_reason":"tool_use"`) || !strings.Contains(all, `"partial_json":"{\"file_path\":\"/x\"}"`) {
		t.Fatalf("missing tool_use: %s", all)
	}
}

func TestZaiMessagesStreamLateErrorIsSSEError(t *testing.T) {
	ts := newZaiTestServer(t, func(context.Context) (*zaiStreamResult, error) {
		return nil, &zaiUpstreamError{Status: http.StatusTooManyRequests, Detail: "concurrent conversation limit"}
	})
	events, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/messages", `{"stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if events[len(events)-1] != "error" || !strings.Contains(data[len(data)-1], `"rate_limit_error"`) {
		t.Fatalf("events=%v last=%s", events, data[len(data)-1])
	}
}

// A panic inside a streamed turn runs on a spawned goroutine, which net/http
// does not recover: it must become an in-band error, not a process crash.
func TestZaiMessagesStreamPanicIsSSEError(t *testing.T) {
	ts := newZaiTestServer(t, func(context.Context) (*zaiStreamResult, error) {
		panic("boom")
	})
	events, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/messages", `{"stream":true,"messages":[{"role":"user","content":"x"}]}`))
	// The panic text stays in the log and the failure dump; the client gets a plain error event.
	if events[len(events)-1] != "error" || !strings.Contains(data[len(data)-1], `"api_error"`) || strings.Contains(data[len(data)-1], "boom") {
		t.Fatalf("events=%v last=%s", events, data[len(data)-1])
	}
}

func TestZaiMessagesStreamClientGoneCancelsTurn(t *testing.T) {
	cancelled := make(chan struct{})
	ts := newZaiTestServer(t, func(ctx context.Context) (*zaiStreamResult, error) {
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	zaiPost(t, ts.url+"/zai/v1/messages", `{"stream":true,"messages":[{"role":"user","content":"x"}]}`).Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("turn not cancelled after the client went away")
	}
}

func TestZaiOversizedBodyIs413(t *testing.T) {
	ts := newZaiTestServer(t, zaiAnswer("unused"))
	big := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 5<<20) + `"}]}`
	if resp := zaiPost(t, ts.url+"/zai/v1/messages", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestZaiErrorStatusPassesThrough429(t *testing.T) {
	ts := newZaiTestServer(t, func(context.Context) (*zaiStreamResult, error) {
		return nil, &zaiUpstreamError{Status: http.StatusTooManyRequests, Detail: "busy"}
	})
	resp := zaiPost(t, ts.url+"/zai/v1/chat/completions", `{"messages":[{"role":"user","content":"x"}]}`)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestZaiChatCompletionsJSON(t *testing.T) {
	ts := newZaiTestServer(t, zaiAnswer("PONG"))
	out := zaiDecode(t, zaiPost(t, ts.url+"/zai/v1/chat/completions",
		`{"model":"glm-5.3-flash","reasoning_effort":"none","messages":[{"role":"user","content":"say PONG"}]}`))
	choice := out["choices"].([]any)[0].(map[string]any)
	if choice["message"].(map[string]any)["content"] != "PONG" || choice["finish_reason"] != "stop" {
		t.Errorf("choice = %v", choice)
	}
	if ts.thinking {
		t.Errorf("reasoning_effort=none must turn thinking off")
	}
}

func TestZaiChatCompletionsStreamToolCalls(t *testing.T) {
	ts := newZaiTestServer(t, func(context.Context) (*zaiStreamResult, error) {
		time.Sleep(150 * time.Millisecond)
		return &zaiStreamResult{Answer: zaiToolCallAnswer}, nil
	})
	events, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/chat/completions",
		`{"stream":true,"messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]}`))
	if count(events, "comment") < 1 {
		t.Errorf("no keepalive during the turn: %v", events)
	}
	if data[len(data)-1] != "[DONE]" {
		t.Fatalf("last frame = %s", data[len(data)-1])
	}
	all := strings.Join(data, "\n")
	if !strings.Contains(all, `"finish_reason":"tool_calls"`) || !strings.Contains(all, `"arguments":"{\"city\":\"NYC\"}"`) {
		t.Fatalf("frames = %s", all)
	}
}

func TestZaiResponsesJSON(t *testing.T) {
	ts := newZaiTestServer(t, zaiAnswer("PONG"))
	out := zaiDecode(t, zaiPost(t, ts.url+"/zai/v1/responses", `{"input":"say PONG","instructions":"be terse"}`))
	if out["object"] != "response" || out["status"] != "completed" {
		t.Fatalf("out = %v", out)
	}
	item := out["output"].([]any)[0].(map[string]any)
	if item["content"].([]any)[0].(map[string]any)["text"] != "PONG" {
		t.Errorf("item = %v", item)
	}
	if !strings.HasPrefix(ts.prompt, "be terse") {
		t.Errorf("instructions must lead the prompt without tools: %q", ts.prompt)
	}
}

func TestZaiResponsesStreamFunctionCall(t *testing.T) {
	ts := newZaiTestServer(t, zaiAnswer(zaiToolCallAnswer))
	events, data := zaiSSE(t, zaiPost(t, ts.url+"/zai/v1/responses",
		`{"stream":true,"input":"weather?","tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}]}`))
	want := []string{"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done",
		"response.output_item.done", "response.completed"}
	var got []string
	for _, e := range events {
		if e != "comment" {
			got = append(got, e)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events:\n got %v\nwant %v", got, want)
	}
	for i, d := range data {
		var ev map[string]any
		if json.Unmarshal([]byte(d), &ev) != nil || int(ev["sequence_number"].(float64)) != i {
			t.Fatalf("frame %d sequence_number: %s", i, d)
		}
	}
	if !strings.Contains(data[len(data)-1], `"call_id":"call_`) {
		t.Errorf("completed response lacks the function call: %s", data[len(data)-1])
	}
}
