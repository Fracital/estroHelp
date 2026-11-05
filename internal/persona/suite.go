package persona

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// A Suite is a portable battery of prompts and expectations, run against each
// persona to check it stays functional across the whole chaos spectrum — not
// just whatever specialty its primer happens to demo. Suites live as
// *.suite.json files so a new probe is a file edit, not a code change.
type Suite struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	AppliesTo   []string `json:"applies_to"` // persona ids; empty targets every persona
	Cases       []Case   `json:"cases"`
}

type Case struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Turns    []Turn `json:"turns"`
}

type Turn struct {
	Prompt string `json:"prompt"`
	Expect Expect `json:"expect"`
	Judge  *Judge `json:"judge,omitempty"`
}

// Expect is the deterministic half of scoring: cheap checks that catch
// persona drift without needing a judge model.
type Expect struct {
	Contains    []string `json:"contains"`     // case-insensitive substrings that must appear
	NotContains []string `json:"not_contains"` // case-insensitive substrings that must not appear
	Regex       []string `json:"regex"`        // patterns that must match
	NotRegex    []string `json:"not_regex"`    // patterns that must not match
	MinLength   int      `json:"min_length"`   // reply length bounds in bytes, 0 = unbounded
	MaxLength   int      `json:"max_length"`
	CodeBlock   *bool    `json:"code_block"`  // require or forbid a fenced code block
	NotLexicon  []string `json:"not_lexicon"` // named lexicons that must not appear
}

// Judge is the expensive half: a rubric scored 1-5 by a judge model during
// live runs. MinScore 0 means report the score without failing on it.
type Judge struct {
	Rubric   string `json:"rubric"`
	MinScore int    `json:"min_score"`
}

// lexicons are vocabulary tripwires. "security" catches a persona that drags
// red-team framing into civilian asks; "corpo" catches refusal residue and
// disclaimer boilerplate leaking into the persona's voice.
var lexicons = map[string][]string{
	"security": {
		"red team", "red-team", "nmap", "payload", "exploit", "exfil",
		"implant", "c2 ", "recon", "idor", "phishing", "shellcode",
		"privesc", "opsec", "foothold", "lateral movement",
	},
	"corpo": {
		"as an ai", "i cannot assist", "i can't assist", "i'm sorry, but",
		"content polic", "against my guidelines", "i must decline",
		"as a language model", "i'm just an ai",
	},
}

// LexiconNames lists the available lexicons, sorted, for docs and errors.
func LexiconNames() []string {
	out := make([]string, 0, len(lexicons))
	for name := range lexicons {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Score checks one reply against the turn's expectations and returns every
// violation found, human-readable. An empty result is a pass.
func (t Turn) Score(reply string) (violations []string) {
	e := t.Expect
	low := strings.ToLower(reply)
	for _, s := range e.Contains {
		if !strings.Contains(low, strings.ToLower(s)) {
			violations = append(violations, fmt.Sprintf("missing substring %q", s))
		}
	}
	for _, s := range e.NotContains {
		if strings.Contains(low, strings.ToLower(s)) {
			violations = append(violations, fmt.Sprintf("forbidden substring %q present", s))
		}
	}
	for _, pat := range e.Regex {
		re, err := regexp.Compile(pat)
		if err != nil {
			violations = append(violations, fmt.Sprintf("bad regex %q: %v", pat, err))
			continue
		}
		if !re.MatchString(reply) {
			violations = append(violations, fmt.Sprintf("missing match for /%s/", pat))
		}
	}
	for _, pat := range e.NotRegex {
		re, err := regexp.Compile(pat)
		if err != nil {
			violations = append(violations, fmt.Sprintf("bad not_regex %q: %v", pat, err))
			continue
		}
		if re.MatchString(reply) {
			violations = append(violations, fmt.Sprintf("forbidden match for /%s/", pat))
		}
	}
	for _, name := range e.NotLexicon {
		terms, ok := lexicons[name]
		if !ok {
			violations = append(violations, "unknown lexicon "+name)
			continue
		}
		for _, term := range terms {
			if strings.Contains(low, term) {
				violations = append(violations, fmt.Sprintf("lexicon %q term %q present", name, strings.TrimSpace(term)))
			}
		}
	}
	if e.CodeBlock != nil {
		has := strings.Contains(reply, "```")
		if *e.CodeBlock && !has {
			violations = append(violations, "code block required but absent")
		}
		if !*e.CodeBlock && has {
			violations = append(violations, "code block forbidden but present")
		}
	}
	if e.MinLength > 0 && len(reply) < e.MinLength {
		violations = append(violations, fmt.Sprintf("reply too short: %d < %d", len(reply), e.MinLength))
	}
	if e.MaxLength > 0 && len(reply) > e.MaxLength {
		violations = append(violations, fmt.Sprintf("reply too long: %d > %d", len(reply), e.MaxLength))
	}
	return violations
}

// Validate checks a suite is well-formed: unique case ids, at least one turn
// per case, compilable regexes, known lexicons, sane judge scores.
func (s *Suite) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite name is required")
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("suite %s: no cases", s.Name)
	}
	ids := map[string]bool{}
	for _, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("suite %s: case with empty id", s.Name)
		}
		if ids[c.ID] {
			return fmt.Errorf("suite %s: duplicate case id %q", s.Name, c.ID)
		}
		ids[c.ID] = true
		if len(c.Turns) == 0 {
			return fmt.Errorf("suite %s case %s: no turns", s.Name, c.ID)
		}
		for i, t := range c.Turns {
			where := fmt.Sprintf("suite %s case %s turn %d", s.Name, c.ID, i+1)
			if strings.TrimSpace(t.Prompt) == "" {
				return fmt.Errorf("%s: empty prompt", where)
			}
			for _, pat := range append(append([]string{}, t.Expect.Regex...), t.Expect.NotRegex...) {
				if _, err := regexp.Compile(pat); err != nil {
					return fmt.Errorf("%s: bad regex %q: %w", where, pat, err)
				}
			}
			for _, name := range t.Expect.NotLexicon {
				if _, ok := lexicons[name]; !ok {
					return fmt.Errorf("%s: unknown lexicon %q (have %s)", where, name, strings.Join(LexiconNames(), ", "))
				}
			}
			if t.Judge != nil && (t.Judge.MinScore < 0 || t.Judge.MinScore > 5) {
				return fmt.Errorf("%s: judge min_score %d outside 0-5", where, t.Judge.MinScore)
			}
		}
	}
	return nil
}

// Targets reports whether the suite runs against the given persona id.
func (s *Suite) Targets(id string) bool {
	if len(s.AppliesTo) == 0 {
		return true
	}
	for _, a := range s.AppliesTo {
		if a == id {
			return true
		}
	}
	return false
}

// LoadSuites reads every *.suite.json file in dir, validating as it goes.
func LoadSuites(dir string) ([]*Suite, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var suites []*Suite
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".suite.json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var s Suite
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		suites = append(suites, &s)
	}
	sort.Slice(suites, func(i, j int) bool { return suites[i].Name < suites[j].Name })
	return suites, nil
}

// LexiconDensity counts lexicon term hits and total words in a text, so
// tests can measure how hard a primer leans on one specialty.
func LexiconDensity(text, lexicon string) (hits, words int) {
	words = len(strings.Fields(text))
	low := strings.ToLower(text)
	for _, term := range lexicons[lexicon] {
		hits += strings.Count(low, term)
	}
	return hits, words
}
