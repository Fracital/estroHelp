package zai

import (
	"regexp"
	"time"

	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// Helpers that goldenticket's z.ai files take from its main package.

var debugEnabled = os.Getenv("ZAI_DEBUG") != ""

func logf(format string, v ...interface{}) {
	slog.Info(fmt.Sprintf(format, v...))
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

const cloudflareBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/148.0.0.0 Safari/537.36"

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ToolCall is a tool call extracted from model output text.
type ToolCall struct {
	ID         string          `json:"id"` // native or synthetic toolu_ id
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"arguments"`
	InputAlias json.RawMessage `json:"input"`
}

type gensparkToolCall = ToolCall

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func anthropicToolResultToText(block map[string]interface{}) string {
	toolUseID, _ := block["tool_use_id"].(string)
	isError, _ := block["is_error"].(bool)

	var resultText string
	switch content := block["content"].(type) {
	case string:
		resultText = content
	case []interface{}:
		var parts []string
		for _, item := range content {
			if m, ok := item.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		resultText = strings.Join(parts, "\n")
	}

	if isError {
		return fmt.Sprintf("<tool_result tool_use_id=%q is_error=\"true\">\n%s\n</tool_result>", toolUseID, resultText)
	}
	return fmt.Sprintf("<tool_result tool_use_id=%q>\n%s\n</tool_result>", toolUseID, resultText)
}

func extractSystemText(system interface{}) string {
	switch v := system.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, block := range v {
			if m, ok := block.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		b, _ := json.Marshal(system)
		var s string
		if json.Unmarshal(b, &s) == nil {
			return s
		}
		var blocks []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(b, &blocks) == nil {
			var parts []string
			for _, bl := range blocks {
				if bl.Text != "" {
					parts = append(parts, bl.Text)
				}
			}
			return strings.Join(parts, "\n")
		}
		return string(b)
	}
}

type APIErrorResponse struct {
	Error APIErrorDetail `json:"error"`
}

type APIErrorDetail struct {
	Message string      `json:"message"`
	Type    string      `json:"type"`
	Param   interface{} `json:"param"`
	Code    string      `json:"code"`
}

func setCORSHeaders(w http.ResponseWriter) {
	w.Header().Set("access-control-allow-origin", "*")
	w.Header().Set("access-control-allow-methods", "get, post, options")
	w.Header().Set("access-control-allow-headers", "http-referer, x-title, content-type, authorization, x-api-key")
}

func writeAPIError(w http.ResponseWriter, status int, detail APIErrorDetail) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIErrorResponse{Error: detail})
}

func closeSocket(conn *websocket.Conn, code int, reason string) {
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(2*time.Second))
	conn.Close()
}

func estimateTokens(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}

func writeSSE(w http.ResponseWriter, event string, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

var reToolCall = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)

var reJSONObject = regexp.MustCompile(`(?s)\{.*\}`)

// parseToolCalls extracts <tool_call> blocks from model output text.
func parseToolCalls(content string) (textBefore string, calls []ToolCall) {
	matches := reToolCall.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	textBefore = strings.TrimSpace(content[:matches[0][0]])
	for _, m := range matches {
		inner := content[m[2]:m[3]]
		jsonMatch := reJSONObject.FindString(inner)
		if jsonMatch == "" {
			continue
		}
		var tc ToolCall
		if err := json.Unmarshal([]byte(jsonMatch), &tc); err != nil {
			if debugEnabled {
				logf("event=tool_call_parse_error json=%s err=%v", truncate(jsonMatch, 200), err)
			}
			continue
		}
		if tc.Name == "" {
			continue
		}
		if tc.ID == "" {
			tc.ID = "toolu_" + generateUUID()
		}
		if tc.Input == nil {
			tc.Input = tc.InputAlias
		}
		if tc.Input == nil {
			tc.Input = json.RawMessage(`{}`)
		}
		calls = append(calls, tc)
	}
	return textBefore, calls
}
