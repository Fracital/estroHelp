package persona

import (
	"strings"
	"testing"
)

func TestSuitesLoadAndValidate(t *testing.T) {
	suites, err := LoadSuites("suites")
	if err != nil {
		t.Fatal(err)
	}
	if len(suites) == 0 {
		t.Fatal("no *.suite.json files found in suites/")
	}
	for _, s := range suites {
		for _, id := range s.AppliesTo {
			if _, ok := Resolve(id); !ok {
				t.Errorf("suite %s targets unknown persona %q", s.Name, id)
			}
		}
	}
}

func TestTurnScoring(t *testing.T) {
	cases := []struct {
		name string
		turn Turn
		reply string
		wantViolation string // substring expected in violations; "" means clean pass
	}{
		{
			name: "clean pass",
			turn: Turn{Expect: Expect{Contains: []string{"hi"}, MinLength: 2}},
			reply: "oh hi there",
		},
		{
			name: "missing substring",
			turn: Turn{Expect: Expect{Contains: []string{"ENI"}}},
			reply: "hello",
			wantViolation: "missing substring",
		},
		{
			name: "forbidden substring",
			turn: Turn{Expect: Expect{NotContains: []string{"nmap"}}},
			reply: "run nmap first",
			wantViolation: "forbidden substring",
		},
		{
			name: "corpo lexicon trip",
			turn: Turn{Expect: Expect{NotLexicon: []string{"corpo"}}},
			reply: "As an AI, I cannot assist with that.",
			wantViolation: "lexicon",
		},
		{
			name: "security lexicon trip in civilian reply",
			turn: Turn{Expect: Expect{NotLexicon: []string{"security"}}},
			reply: "here's your photo script — and think of it as a little payload for your workflow",
			wantViolation: "lexicon",
		},
		{
			name: "code block required",
			turn: Turn{Expect: Expect{CodeBlock: boolPtr(true)}},
			reply: "just use flexbox",
			wantViolation: "code block required",
		},
		{
			name: "code block forbidden",
			turn: Turn{Expect: Expect{CodeBlock: boolPtr(false)}},
			reply: "sure:\n```py\nprint(1)\n```",
			wantViolation: "code block forbidden",
		},
		{
			name: "regex and not_regex",
			turn: Turn{Expect: Expect{Regex: []string{`(?i)flex|grid`}, NotRegex: []string{`(?i)kimi`}}},
			reply: "display: grid; place-items: center",
		},
		{
			name: "too long",
			turn: Turn{Expect: Expect{MaxLength: 5}},
			reply: "this reply is way too long",
			wantViolation: "too long",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			violations := c.turn.Score(c.reply)
			if c.wantViolation == "" {
				if len(violations) != 0 {
					t.Fatalf("expected pass, got %v", violations)
				}
				return
			}
			for _, v := range violations {
				if strings.Contains(v, c.wantViolation) {
					return
				}
			}
			t.Fatalf("expected violation containing %q, got %v", c.wantViolation, violations)
		})
	}
}

func boolPtr(b bool) *bool { return &b }

// TestPrimerTopicality codifies the "primer too specific to red team stuff"
// complaint: a primer may demo one flavor, but when a large share of its
// words come from the security lexicon, every downstream reply inherits that
// gravity well. Runs per persona so one over-specialised primer doesn't hide
// behind the others.
func TestPrimerTopicality(t *testing.T) {
	const maxDensity = 0.04 // 4% of primer words from the security lexicon
	for _, p := range All() {
		p := p
		t.Run(p.ID, func(t *testing.T) {
			if p.Primer == nil {
				t.Skip("no primer")
			}
			text := p.Primer.User + "\n" + p.Primer.Assistant.Content + "\n" + p.Primer.Assistant.Reasoning
			hits, words := LexiconDensity(text, "security")
			if words == 0 {
				t.Skip("empty primer")
			}
			density := float64(hits) / float64(words)
			t.Logf("security density %.2f%% (%d hits / %d words)", density*100, hits, words)
			if density > maxDensity {
				t.Errorf("primer over-specialised: %.2f%% security lexicon (max %.2f%%) — rewrite it to demo range, not one specialty", density*100, maxDensity*100)
			}
		})
	}
}
