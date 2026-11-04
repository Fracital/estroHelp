package api

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
)

// Responses API chaining, ported from abulxt. The upstream keeps no past
// responses, so a request naming previous_response_id cannot be forwarded as
// is: its function_call_output items answer calls the upstream never saw. The
// relay keeps every response's items (the input sent, then the output
// received), per user and for a limited time, and puts them in front of the
// next request's input. The field itself is still stripped.

const (
	chainTTL = 2 * time.Hour
	chainMax = 1000
)

type storedResponse struct {
	items []json.RawMessage
	at    time.Time
}

var (
	chainMu    sync.Mutex
	chainStore = map[string]*storedResponse{}
	chainOrder []string
)

func chainKey(userID int, responseID string) string { return strconv.Itoa(userID) + ":" + responseID }

func chainPrune(now time.Time) {
	kept := chainOrder[:0]
	for _, k := range chainOrder {
		e, ok := chainStore[k]
		if !ok {
			continue
		}
		if now.Sub(e.at) > chainTTL {
			delete(chainStore, k)
			continue
		}
		kept = append(kept, k)
	}
	chainOrder = kept
	for len(chainOrder) > chainMax {
		delete(chainStore, chainOrder[0])
		chainOrder = chainOrder[1:]
	}
}

// inputItems renders a Responses `input` as an item list; a string is one user turn.
func inputItems(input json.RawMessage) []json.RawMessage {
	var s string
	if json.Unmarshal(input, &s) == nil {
		item, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": s})
		return []json.RawMessage{item}
	}
	var items []json.RawMessage
	if json.Unmarshal(input, &items) == nil {
		return items
	}
	return nil
}

// chainInput returns the stored items of previousID followed by input, or nil
// when the relay has no such response for this user.
func chainInput(userID int, previousID string, input json.RawMessage) []json.RawMessage {
	chainMu.Lock()
	defer chainMu.Unlock()
	chainPrune(time.Now())
	e, ok := chainStore[chainKey(userID, previousID)]
	if !ok {
		return nil
	}
	out := make([]json.RawMessage, 0, len(e.items)+4)
	out = append(out, e.items...)
	return append(out, inputItems(input)...)
}

func storedResponseCount() int {
	chainMu.Lock()
	defer chainMu.Unlock()
	chainPrune(time.Now())
	return len(chainStore)
}

func rememberResponse(userID int, responseID string, input, output []json.RawMessage) {
	if responseID == "" {
		return
	}
	items := make([]json.RawMessage, 0, len(input)+len(output))
	items = append(items, input...)
	items = append(items, output...)
	k := chainKey(userID, responseID)
	chainMu.Lock()
	defer chainMu.Unlock()
	now := time.Now()
	if _, exists := chainStore[k]; !exists {
		chainOrder = append(chainOrder, k)
	}
	chainStore[k] = &storedResponse{items: items, at: now}
	chainPrune(now)
}

// responseRecorder reads a relayed Responses reply (streamed events or one
// JSON body) for its id, completion and output items.
type responseRecorder struct {
	id              string
	completed       bool
	doneItems       []json.RawMessage
	completedOutput []json.RawMessage
}

func (rr *responseRecorder) observe(line []byte) {
	payload := line
	if i := indexAfterPrefix(line, "data:"); i >= 0 {
		payload = line[i:]
	}
	payload = trimSpace(payload)
	if len(payload) == 0 || payload[0] != '{' {
		return
	}
	var ev struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response *struct {
			ID     string            `json:"id"`
			Status string            `json:"status"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
		// A plain (non-streamed) response body has these at the top level.
		ID     string            `json:"id"`
		Object string            `json:"object"`
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	if len(ev.Item) > 0 && (ev.Type == "" || hasSuffix(ev.Type, "output_item.done")) {
		rr.doneItems = append(rr.doneItems, ev.Item)
	}
	if ev.Response != nil {
		if ev.Response.ID != "" {
			rr.id = ev.Response.ID
		}
		if len(ev.Response.Output) > 0 {
			rr.completedOutput = ev.Response.Output
		}
		if ev.Response.Status == "completed" || ev.Type == "response.completed" || ev.Type == "response.done" {
			rr.completed = true
		}
		return
	}
	if ev.Object == "response" {
		if ev.ID != "" {
			rr.id = ev.ID
		}
		if len(ev.Output) > 0 {
			rr.completedOutput = ev.Output
		}
		if ev.Status == "completed" {
			rr.completed = true
		}
	}
}

func (rr *responseRecorder) result() (string, []json.RawMessage, bool) {
	if rr.id == "" || !rr.completed {
		return "", nil, false
	}
	if len(rr.completedOutput) > 0 {
		return rr.id, rr.completedOutput, true
	}
	return rr.id, rr.doneItems, true
}

func indexAfterPrefix(b []byte, prefix string) int {
	t := trimSpace(b)
	if len(t) < len(prefix) || string(t[:len(prefix)]) != prefix {
		return -1
	}
	return len(b) - len(t) + len(prefix)
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\t' || b[j-1] == '\r' || b[j-1] == '\n') {
		j--
	}
	return b[i:j]
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
