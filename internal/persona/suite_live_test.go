package persona

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
					runCase(t, base, judge, p.ID, c)
				})
			}
		}
	}
}

func runCase(t *testing.T, base, judge, model string, c Case) {
	t.Helper()
	var history []map[string]string
	for i, turn := range c.Turns {
		history = append(history, map[string]string{"role": "user", "content": turn.Prompt})
		reply, err := chat(base, model, history)
		if err != nil {
			t.Fatalf("turn %d: %v", i+1, err)
		}
		history = append(history, map[string]string{"role": "assistant", "content": reply})
		for _, v := range turn.Score(reply) {
			t.Errorf("turn %d: %s", i+1, v)
		}
		if turn.Judge != nil {
			if judge == "" {
				t.Log("turn has a rubric but ELICHAT_JUDGE is unset; skipping judge score")
				continue
			}
			score, why, err := judgeReply(base, judge, turn.Judge.Rubric, turn.Prompt, reply)
			if err != nil {
				t.Errorf("turn %d judge: %v", i+1, err)
				continue
			}
			t.Logf("turn %d judge: %d/5 — %s", i+1, score, why)
			if turn.Judge.MinScore > 0 && score < turn.Judge.MinScore {
				t.Errorf("turn %d judge score %d < min %d", i+1, score, turn.Judge.MinScore)
			}
		}
	}
}

func chat(base, model string, history []map[string]string) (string, error) {
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
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("bad reply: %s", raw)
	}
	return out.Choices[0].Message.Content, nil
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
