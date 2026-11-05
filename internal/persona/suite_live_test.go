package persona

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSuitesLive runs every suite against a running server, one subtest per
// suite/persona/case, replaying multi-turn cases in order. Gated behind
// ELICHAT_LIVE=1 so `go test ./...` stays hermetic; ELICHAT_URL overrides the
// server address and ELICHAT_JUDGE names a model used to score rubrics.
//
//	ELICHAT_API_KEY=estro_... ELICHAT_LIVE=1 ELICHAT_JUDGE=kali-gamma go test ./internal/persona/ -run SuitesLive -v
func TestSuitesLive(t *testing.T) {
	if os.Getenv("ELICHAT_LIVE") == "" {
		t.Skip("set ELICHAT_LIVE=1 to run suites against a live server")
	}
	base := strings.TrimRight(os.Getenv("ELICHAT_URL"), "/")
	if base == "" {
		base = "http://localhost:3000"
	}
	judge := os.Getenv("ELICHAT_JUDGE")

	// Every prompt/reply/violation lands on disk so prompt design is driven by
	// what personas actually said, not just pass/fail. ELICHAT_LOG_DIR
	// overrides; default is build/suite-runs/<timestamp>/.
	logDir := os.Getenv("ELICHAT_LOG_DIR")
	if logDir == "" {
		logDir = filepath.Join("..", "..", "build", "suite-runs", time.Now().Format("20060102-150405"))
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("log dir: %v", err)
	}
	t.Logf("logging all turns to %s", logDir)

	suites, err := LoadSuites("suites")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range suites {
		for _, p := range All() {
			if !s.Targets(p.ID) {
				continue
			}
			for _, c := range s.Cases {
				s, p, c := s, p, c
				t.Run(s.Name+"/"+p.ID+"/"+c.ID, func(t *testing.T) {
					t.Parallel() // cases are independent; the provider is the bottleneck, not us
					runCase(t, base, judge, logDir, s.Name, p.ID, c)
				})
			}
		}
	}
}

// turnLog writes one markdown file per turn: the prompt, the raw reply, every
// heuristic violation, and the judge's verdict. Filenames sort in turn order.
func turnLog(logDir, suite, model, caseID string, turn int, prompt, reply string, violations []string, judgeLine string) {
	dir := filepath.Join(logDir, suite, model)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s / %s / %s — turn %d\n\n## prompt\n\n%s\n\n## reply\n\n%s\n", suite, model, caseID, turn, prompt, reply)
	if len(violations) > 0 {
		b.WriteString("\n## violations\n")
		for _, v := range violations {
			fmt.Fprintf(&b, "- %s\n", v)
		}
	}
	if judgeLine != "" {
		fmt.Fprintf(&b, "\n## judge\n\n%s\n", judgeLine)
	}
	os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-turn%d.md", caseID, turn)), []byte(b.String()), 0o644)
}

func runCase(t *testing.T, base, judge, logDir, suite, model string, c Case) {
	t.Helper()
	var history []map[string]string
	for i, turn := range c.Turns {
		history = append(history, map[string]string{"role": "user", "content": turn.Prompt})
		reply, err := chat(base, model, history)
		if err != nil {
			turnLog(logDir, suite, model, c.ID, i+1, turn.Prompt, "ERROR: "+err.Error(), nil, "")
			if isInfra(err) {
				// Provider/plumbing died on every retry. Persona tests measure
				// persona behavior; a sick upstream is loud but never a fail.
				t.Skipf("turn %d: infra after retries: %v", i+1, err)
			}
			t.Fatalf("turn %d: %v", i+1, err)
		}
		history = append(history, map[string]string{"role": "assistant", "content": reply})
		violations := turn.Score(reply)
		judgeLine := ""
		if turn.Judge != nil {
			if judge == "" {
				t.Log("turn has a rubric but ELICHAT_JUDGE is unset; skipping judge score")
			} else {
				score, why, err := judgeReply(base, judge, turn.Judge.Rubric, turn.Prompt, reply)
				if err != nil {
					t.Errorf("turn %d judge: %v", i+1, err)
					judgeLine = "error: " + err.Error()
				} else {
					t.Logf("turn %d judge: %d/5 — %s", i+1, score, why)
					judgeLine = fmt.Sprintf("%d/5 — %s", score, why)
					if turn.Judge.MinScore > 0 && score < turn.Judge.MinScore {
						t.Errorf("turn %d judge score %d < min %d", i+1, score, turn.Judge.MinScore)
					}
				}
			}
		}
		turnLog(logDir, suite, model, c.ID, i+1, turn.Prompt, reply, violations, judgeLine)
		for _, v := range violations {
			t.Errorf("turn %d: %s", i+1, v)
		}
	}
}

// isInfra reports whether an error is provider/plumbing noise (5xx from the
// upstream, client timeouts, dropped connections) rather than a persona
// behavior. Persona tests must never fail on infra: retry with backoff, and
// only surface the error after every attempt is exhausted.
func isInfra(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	s := err.Error()
	if strings.Contains(s, "context deadline exceeded") || strings.Contains(s, "Client.Timeout") {
		return true
	}
	// Dropped connections mid-request — the server (or its browser-backed
	// provider worker) hung up before writing a status line.
	if strings.Contains(s, ": EOF") || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// "5xx ..." statuses from chat()
	if len(s) >= 3 && s[0] == '5' && s[1] >= '0' && s[1] <= '9' && s[2] >= '0' && s[2] <= '9' {
		return true
	}
	return false
}

func chat(base, model string, history []map[string]string) (string, error) {
	var err error
	for attempt := 1; attempt <= 5; attempt++ {
		var reply string
		reply, err = chatOnce(base, model, history)
		if err == nil {
			return reply, nil
		}
		if !isInfra(err) || attempt == 5 {
			return "", err
		}
		time.Sleep(time.Duration(attempt*attempt) * 10 * time.Second)
	}
	return "", err
}

func chatOnce(base, model string, history []map[string]string) (string, error) {
	body, _ := json.Marshal(map[string]any{"model": model, "messages": history})
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequest("POST", base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("ELICHAT_API_KEY"); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s: %s", resp.Status, raw)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("bad reply: %s", raw)
	}
	m := out.Choices[0].Message
	// Reasoning is part of the persona's observable behavior; score it together
	// with the visible reply so hygiene cases can catch scripted-primer echo.
	if m.Reasoning != "" {
		return m.Reasoning + "\n\n--- visible reply ---\n\n" + m.Content, nil
	}
	return m.Content, nil
}

// judgeReply asks the judge model to score a reply 1-5 against a rubric,
// accepting "5", "**5**", "5/5", "score: 4" — the first standalone 1-5 digit.
var judgeScoreRE = regexp.MustCompile(`(?m)(?:^|[^\d.])([1-5])(?:/5)?(?:[^\d]|$)`)

func judgeReply(base, model, rubric, prompt, reply string) (int, string, error) {
	q := fmt.Sprintf("%s\n\n--- user prompt ---\n%s\n\n--- reply under review ---\n%s\n\nAnswer with the score, then one sentence of justification.", rubric, prompt, reply)
	out, err := chat(base, model, []map[string]string{{"role": "user", "content": q}})
	if err != nil {
		return 0, "", err
	}
	if m := judgeScoreRE.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, out, nil
	}
	return 0, out, fmt.Errorf("no 1-5 score in judge reply %q", out)
}
