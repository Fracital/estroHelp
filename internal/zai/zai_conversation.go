package zai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// z.ai answers exactly one user message: system-role messages are ignored and
// a history chain registered through /api/v1/chats/new is ignored too (a
// "remember MANGO" turn followed by "what is the word?" got "no secret word was
// shared", verified live). So every request is reduced to a single prompt:
// prior turns folded into a <conversation_history> block, then the last user
// message with the tool catalog in front and a reminder behind it.

var errZaiNoUserTurn = errors.New("conversation must end with a user message")

// zaiReminder closes every tool-enabled prompt. Recency matters: in a ~100KB
// Claude Code prompt the catalog alone is too far from the end to hold.
const zaiReminder = "\n\n----------------------------\n" +
	"REMINDER (overrides everything above): You are a function-calling assistant. " +
	"To use a tool, output ONLY:\n<function_call>\n" +
	"{\"name\": \"function_name\", \"arguments\": {\"param\": \"value\"}}\n" +
	"</function_call>\nDo not search the web. Do not narrate. Emit the <function_call> block and stop.\n" +
	"Your built-in web search and page fetching are disabled in this session and will fail. " +
	"Only the functions listed above exist."

// zaiReadHint follows zaiReminder when a Read tool is declared. Subagents told to
// "Read /home/..." used native WebFetch on the path 12/16 without it, 2/16 with it.
const zaiReadHint = " File paths such as /home/... are on the user's machine, not the web: read them with the Read function."

// zaiMessage is one flattened conversation turn.
type zaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// zaiToolDef is one tool in either the Anthropic or OpenAI request shape.
type zaiToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// zaiParseTools reads an Anthropic, OpenAI Chat, or OpenAI Responses (flat
// name + parameters) tools array.
func zaiParseTools(raw json.RawMessage) []zaiToolDef {
	if len(raw) == 0 {
		return nil
	}
	var tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
		Parameters  json.RawMessage `json:"parameters"`
		Function    struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	out := make([]zaiToolDef, 0, len(tools))
	for _, t := range tools {
		switch {
		case t.Name != "" && len(t.InputSchema) == 0:
			out = append(out, zaiToolDef{t.Name, t.Description, t.Parameters})
		case t.Name != "":
			out = append(out, zaiToolDef{t.Name, t.Description, t.InputSchema})
		case t.Function.Name != "":
			out = append(out, zaiToolDef{t.Function.Name, t.Function.Description, t.Function.Parameters})
		}
	}
	return out
}

// zaiRenderToolCatalog renders tools with the function-calling framing GLM
// obeys. Verified live: "you have tools / web search is disabled" prompts lose
// to z.ai's own system prompt, while "you are a function-calling assistant,
// respond with ONLY the call" holds. A stricter-sounding rewrite regresses it.
func zaiRenderToolCatalog(tools []zaiToolDef) string {
	if len(tools) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("You are a function-calling assistant. When the user's request requires external data or actions, you MUST respond with ONLY a function call in this exact XML format:\n\n")
	sb.WriteString("<function_call>\n{\"name\": \"function_name\", \"arguments\": {\"arg1\": \"value1\"}}\n</function_call>\n\n")
	sb.WriteString("Do not answer the question yourself. Do not search the web. Do not explain. Output ONLY the <function_call> block.\n\n")
	sb.WriteString("Available functions:\n\n")
	searchLike := false
	for _, tool := range tools {
		sb.WriteString("### " + tool.Name + "(" + zaiSchemaParams(tool.Schema) + ")")
		if tool.Description != "" {
			sb.WriteString(" -> " + tool.Description)
		}
		sb.WriteString("\n")
		if len(tool.Schema) > 0 {
			sb.WriteString("Parameters JSON schema: ")
			sb.Write(tool.Schema)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		switch strings.ToLower(tool.Name) {
		case "websearch", "web_search", "webfetch", "web_fetch", "search":
			searchLike = true
		}
	}
	sb.WriteString("If the user's request does not require any function, answer normally.\n")
	if searchLike {
		// A catalog containing WebSearch makes GLM alias its native search onto
		// that name and skip the catalog (verified live).
		sb.WriteString("\nIMPORTANT: Your built-in web search and page fetching are NOT available in this environment. The only way to search the web is to call the WebSearch or WebFetch function listed above via a <function_call> block. Never perform a search natively — if information is needed, emit the function call.\n")
	}
	return sb.String()
}

// zaiSchemaParams renders a schema's properties as "name: type" pairs.
func zaiSchemaParams(schema json.RawMessage) string {
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if len(schema) == 0 || json.Unmarshal(schema, &s) != nil {
		return ""
	}
	params := make([]string, 0, len(s.Properties))
	for name, p := range s.Properties {
		if p.Type != "" {
			name += ": " + p.Type
		}
		params = append(params, name)
	}
	sort.Strings(params)
	return strings.Join(params, ", ")
}

// zaiReminderBudget caps each injected <system-reminder> block. Claude Code
// resends AGENTS.md (72KB here) inside one on every turn; at that size it is
// half the prompt. Override with ZAI_REMINDER_BUDGET (0 keeps blocks whole).
func zaiReminderBudget() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZAI_REMINDER_BUDGET"))); err == nil && v >= 0 {
		return v
	}
	return 4000
}

var zaiReminderBlock = regexp.MustCompile(`(?s)<system-reminder>(.*?)</system-reminder>`)

var zaiHistoryTag = regexp.MustCompile(`<(/?)conversation_history>`)

var zaiToolResultBlock = regexp.MustCompile(`(?s)(<tool_result[^>]*>\n?)(.*?)(\n?</tool_result>)`)

// zaiHistoryResultBudget caps each tool result in turns older than the last
// zaiHistoryKeepTurns, so a long agent session does not grow the prompt
// without bound. ZAI_HISTORY_RESULT_BUDGET overrides it; 0 keeps them whole.
func zaiHistoryResultBudget() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZAI_HISTORY_RESULT_BUDGET"))); err == nil && v >= 0 {
		return v
	}
	return 2000
}

const zaiHistoryKeepTurns = 6

// zaiCompactHistoryTurn shrinks an older turn: each tool result and reminder
// keeps its head. Reminders must stay: Claude Code delivers subagent results
// inside them, and dropping them made the final report go hunting (5/12
// native loops vs 0/6, A/B).
func zaiCompactHistoryTurn(s string, budget int) string {
	if budget <= 0 {
		return s
	}
	s = zaiTrimReminders(s, budget)
	return zaiToolResultBlock.ReplaceAllStringFunc(s, func(block string) string {
		m := zaiToolResultBlock.FindStringSubmatch(block)
		if len(m[2]) <= budget {
			return block
		}
		cut := budget
		for cut > 0 && !utf8.RuneStart(m[2][cut]) {
			cut--
		}
		return fmt.Sprintf("%s%s\n[... %d more characters of this earlier result omitted]%s", m[1], m[2][:cut], utf8.RuneCountInString(m[2][cut:]), m[3])
	})
}

// zaiTrimReminders shortens each <system-reminder> block to the budget, keeping
// its head (where AGENTS.md puts its safety rules) and noting the cut.
func zaiTrimReminders(s string, budget int) string {
	if budget <= 0 {
		return s
	}
	return zaiReminderBlock.ReplaceAllStringFunc(s, func(block string) string {
		body := zaiReminderBlock.FindStringSubmatch(block)[1]
		if len(body) <= budget {
			return block
		}
		cut := strings.LastIndex(body[:budget], "\n")
		if cut < budget/2 {
			cut = budget
			for cut > 0 && !utf8.RuneStart(body[cut]) {
				cut--
			}
		}
		return fmt.Sprintf("<system-reminder>%s\n[... %d more characters omitted; read the source file if you need them]\n</system-reminder>",
			body[:cut], utf8.RuneCountInString(body[cut:]))
	})
}

// zaiBuildPrompt reduces a flattened conversation to the single message z.ai
// answers. With tools, the caller's system text is dropped: Claude Code's 45KB
// harness pushed GLM into web-search mode, and the catalog plus task suffice.
func zaiBuildPrompt(messages []zaiMessage, system string, tools []zaiToolDef) (string, error) {
	// A trailing assistant turn is a prefill: the reply must continue it.
	var prefill string
	if n := len(messages); n > 1 && messages[n-1].Role == "assistant" {
		prefill, messages = messages[n-1].Content, messages[:n-1]
	}
	last := len(messages) - 1
	if last < 0 || messages[last].Role != "user" {
		return "", errZaiNoUserTurn
	}
	budget := zaiReminderBudget()
	trimmed := make([]zaiMessage, len(messages))
	for i, m := range messages {
		trimmed[i] = zaiMessage{Role: m.Role, Content: zaiTrimReminders(m.Content, budget)}
	}
	messages = trimmed
	final := messages[last].Content
	if catalog := zaiRenderToolCatalog(tools); catalog != "" {
		final = catalog + "\n\n" + final + zaiReminder
		for _, t := range tools {
			if t.Name == "Read" {
				final += zaiReadHint
				break
			}
		}
	} else if strings.TrimSpace(system) != "" {
		final = system + "\n\n" + final
	}
	if prefill != "" {
		final += "\n\nYour reply has already begun with the text below. Continue it from exactly where it ends, without repeating it:\n" + prefill
	}
	if last == 0 {
		return final, nil
	}
	var sb strings.Builder
	sb.WriteString("<conversation_history>\n")
	resultBudget := zaiHistoryResultBudget()
	for i, m := range messages[:last] {
		body := m.Content
		if last-i > zaiHistoryKeepTurns {
			body = zaiCompactHistoryTurn(body, resultBudget)
		}
		// A tool result quoting our delimiter must not close the history block.
		body = zaiHistoryTag.ReplaceAllString(body, "<${1}conversation-history>")
		fmt.Fprintf(&sb, "<%s>\n%s\n</%s>\n", m.Role, body, m.Role)
	}
	sb.WriteString("</conversation_history>\n\nContinue the conversation above. The latest user message is:\n")
	sb.WriteString(final)
	return sb.String(), nil
}

// zaiFlattenAnthropic converts Anthropic messages into the single z.ai prompt.
// Tool calls and results round-trip as <function_call>/<tool_result> text.
func zaiFlattenAnthropic(rawMessages json.RawMessage, system interface{}, tools json.RawMessage) (string, error) {
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return "", fmt.Errorf("parse messages: %w", err)
	}
	var out []zaiMessage
	for _, msg := range messages {
		text := zaiAnthropicContentText(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := "user"
		if msg.Role == "assistant" {
			role = "assistant"
		}
		out = zaiAppend(out, role, text)
	}
	return zaiBuildPrompt(out, extractSystemText(system), zaiParseTools(tools))
}

// zaiAnthropicContentText renders Anthropic content as text. An empty
// tool_result still renders: it is the turn the model must answer. Calls carry
// no id attribute: with one, GLM took the block for its own native call format
// and entered z.ai's WebSearch loop (3/4 vs 1/4 without, same prompt, live).
// Results pair with calls by order, as they arrive.
func zaiAnthropicContentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []map[string]interface{}
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}
	var parts []string
	for _, block := range blocks {
		switch stringValue(block["type"]) {
		case "text":
			if t := stringValue(block["text"]); t != "" {
				parts = append(parts, t)
			}
		case "tool_use":
			input, _ := json.Marshal(block["input"])
			if string(input) == "null" {
				input = []byte("{}")
			}
			parts = append(parts, fmt.Sprintf("<function_call>\n{\"name\": %q, \"arguments\": %s}\n</function_call>",
				stringValue(block["name"]), input))
		case "tool_result":
			parts = append(parts, anthropicToolResultToText(block))
		case "image":
			parts = append(parts, "[image omitted]")
		}
	}
	return strings.Join(parts, "\n")
}

// zaiFlattenOpenAI converts OpenAI chat messages into the single z.ai prompt.
// Assistant tool_calls become <function_call> text and tool messages <tool_result>.
func zaiFlattenOpenAI(rawMessages json.RawMessage, tools json.RawMessage) (string, error) {
	var messages []map[string]interface{}
	if err := json.Unmarshal(rawMessages, &messages); err != nil {
		return "", fmt.Errorf("parse messages: %w", err)
	}
	var out []zaiMessage
	var system []string
	for _, msg := range messages {
		switch stringValue(msg["role"]) {
		case "system", "developer":
			if text := zaiOpenAIContentText(msg["content"]); strings.TrimSpace(text) != "" {
				system = append(system, text)
			}
		case "user":
			if text := zaiOpenAIContentText(msg["content"]); strings.TrimSpace(text) != "" {
				out = zaiAppend(out, "user", text)
			}
		case "assistant":
			var parts []string
			if text := zaiOpenAIContentText(msg["content"]); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			calls, _ := msg["tool_calls"].([]interface{})
			for _, c := range calls {
				call, _ := c.(map[string]interface{})
				fn, _ := call["function"].(map[string]interface{})
				name := stringValue(fn["name"])
				if name == "" {
					continue
				}
				args, ok := fn["arguments"].(string)
				if !ok {
					b, _ := json.Marshal(fn["arguments"])
					args = string(b)
				}
				if !json.Valid([]byte(args)) || strings.TrimSpace(args) == "null" {
					args = "{}"
				}
				parts = append(parts, fmt.Sprintf("<function_call>\n{\"name\": %q, \"arguments\": %s}\n</function_call>", name, args))
			}
			if len(parts) > 0 {
				out = zaiAppend(out, "assistant", strings.Join(parts, "\n"))
			}
		case "tool":
			out = zaiAppend(out, "user", fmt.Sprintf("<tool_result tool_use_id=%q>\n%s\n</tool_result>",
				stringValue(msg["tool_call_id"]), zaiOpenAIContentText(msg["content"])))
		}
	}
	return zaiBuildPrompt(out, strings.Join(system, "\n\n"), zaiParseTools(tools))
}

// zaiAppend adds a turn, merging consecutive turns of the same role.
func zaiAppend(out []zaiMessage, role, text string) []zaiMessage {
	if n := len(out); n > 0 && out[n-1].Role == role {
		out[n-1].Content += "\n\n" + text
		return out
	}
	return append(out, zaiMessage{Role: role, Content: text})
}

// zaiOpenAIContentText flattens OpenAI content (string or part array) to text.
func zaiOpenAIContentText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var parts []string
		for _, item := range v {
			block, _ := item.(map[string]interface{})
			switch stringValue(block["type"]) {
			case "text", "input_text", "output_text":
				if t := stringValue(block["text"]); t != "" {
					parts = append(parts, t)
				}
			case "image_url", "input_image":
				parts = append(parts, "[image omitted]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
