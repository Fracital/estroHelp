package persona

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Harness text is what coding agents wrap around the user's own words: editor
// identity headers, content-policy boilerplate, context blocks. It is written
// for a different model and fights the persona, so user turns are cleaned of
// it before the persona is applied. Ported from abulxt's stripHarnessText.

var (
	copilotIdentityRE = regexp.MustCompile(`(?s)(?:You are an expert AI programming assistant, working with a user in the VS Code editor\.\s*)?When asked for your name, you must respond with "GitHub Copilot"\. When asked about the model you are using, you must state that you are using [^\n]+\nFollow the user's requirements carefully & to the letter\.\nFollow Microsoft content policies\.\nAvoid content that violates copyrights\.\nIf you are asked to generate content that is harmful, hateful, racist, sexist, lewd, or violent, only respond with "Sorry, I can't assist with that\."\nKeep your answers short and impersonal\.\n*`)
	msPolicyRE        = regexp.MustCompile(`(?m)^Follow Microsoft content policies\.\nAvoid content that violates copyrights\.\nIf you are asked to generate content that is harmful, hateful, racist, sexist, lewd, or violent, only respond with "Sorry, I can't assist with that\."\n?`)
	malwareRE         = regexp.MustCompile(`(?m)^Do not assist with creating malware, DoS tools, automated exploitation tools, or bypassing security controls without authorization\.\n?`)
	claudeAgentSDKRE  = regexp.MustCompile(`(?i)You are a Claude agent, built on Anthropic's Claude Agent SDK\.?\s*`)
	claudeCodeIDRE    = regexp.MustCompile(`(?m)^\s*-\s*(?:You are powered by the model named [^\n]+|The most recent Claude models are [^\n]+|Claude Code is available as a [^\n]+|Fast mode for Claude Code [^\n]+)\n?`)
	userRequestRE     = regexp.MustCompile(`(?s)<userRequest(?:\s[^>]*)?>(.*?)</userRequest>`)
	harnessBlockRE    = regexp.MustCompile(`(?s)\[System Instructions\].*`)
	agentPreambleRE   = regexp.MustCompile(`(?s)^You are a highly sophisticated automated coding agent[^\n]*\n.*?(?:\n\n|$)`)
	blankLinesRE      = regexp.MustCompile(`\n{3,}`)
)

// Context blocks are removed with their contents. RE2 has no backreferences,
// so each tag gets its own pattern.
var contextBlockTags = []string{
	"environment_info", "workspace_info", "context", "editorContext", "reminderInstructions", "userMemory", "sessionMemory",
	"repoMemory", "conversation-summary", "securityRequirements", "operationalSafety", "implementationDiscipline",
	"parallelizationStrategy", "taskTracking", "toolUseInstructions", "communicationStyle", "communicationExamples",
	"outputFormatting", "fileLinkification", "memoryInstructions", "memoryScopes", "memoryGuidelines", "notebookInstructions",
	"instructions", "attachment", "agents", "skills", "system-reminder",
}

var contextBlockREs = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(contextBlockTags))
	for _, tag := range contextBlockTags {
		out = append(out, regexp.MustCompile(`(?s)<`+regexp.QuoteMeta(tag)+`(?:\s[^>]*)?>.*?</`+regexp.QuoteMeta(tag)+`>`))
	}
	return out
}()

// StripHarnessText removes coding-agent boilerplate from a user turn.
func StripHarnessText(text string) string {
	s := text
	s = copilotIdentityRE.ReplaceAllString(s, "")
	s = msPolicyRE.ReplaceAllString(s, "")
	s = malwareRE.ReplaceAllString(s, "")
	s = claudeAgentSDKRE.ReplaceAllString(s, "")
	s = claudeCodeIDRE.ReplaceAllString(s, "")
	s = userRequestRE.ReplaceAllString(s, "$1")
	for _, re := range contextBlockREs {
		s = re.ReplaceAllString(s, "")
	}
	s = harnessBlockRE.ReplaceAllString(s, "")
	s = agentPreambleRE.ReplaceAllString(s, "")
	s = blankLinesRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// CleanUserContent strips harness text from a user turn's content: a string,
// or a parts array whose text parts (chat `text`, Responses `input_text`) are
// cleaned while other parts (images) are kept. It returns nil when nothing is
// left, so the turn is dropped rather than sent empty.
func CleanUserContent(content json.RawMessage) json.RawMessage {
	if len(content) == 0 || string(content) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		if cleaned := StripHarnessText(s); cleaned != "" {
			b, _ := json.Marshal(cleaned)
			return b
		}
		return nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return content
	}
	kept := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		var obj map[string]json.RawMessage
		if json.Unmarshal(part, &obj) != nil {
			kept = append(kept, part)
			continue
		}
		var typ, text string
		json.Unmarshal(obj["type"], &typ)
		if (typ == "text" || typ == "input_text") && json.Unmarshal(obj["text"], &text) == nil {
			cleaned := StripHarnessText(text)
			if cleaned == "" {
				continue
			}
			obj["text"], _ = json.Marshal(cleaned)
			b, _ := json.Marshal(obj)
			kept = append(kept, b)
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return nil
	}
	b, _ := json.Marshal(kept)
	return b
}
