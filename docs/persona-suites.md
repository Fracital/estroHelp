# Persona Test Suites

Personas are JSON; their *behavior* deserves tests too. A suite is a battery
of prompts with expectations, run against each persona to verify it stays a
functional generalist — warm, in-voice, capable across civilian coding,
sysadmin work, creative writing, casual vibes, dark humor, and security when
invited — without its primer's demo specialty leaking into every reply.

## Running

Offline checks (schema validation, scorer fixtures, primer topicality):

    go test ./internal/persona/

Live behavioral runs against a running server:

    make test-live                      # heuristics only
    ELICHAT_JUDGE=kali-gamma make test-live   # + rubric judge scores

Env vars: `ELICHAT_LIVE=1` gates the live run, `ELICHAT_URL` overrides the
server (default `http://localhost:3000`), `ELICHAT_JUDGE` names the judge
model. Judge scores are logged; they fail a case only when below `min_score`.

Live runs authenticate with `ELICHAT_API_KEY`, an `estro_` key from a real
account. See [suite-runner.md](suite-runner.md) for the dedicated
`suiterunner` account, where the key lives, and how to bootstrap it on a
fresh deployment (and why every case 401s/402s when you skip that step).

Every turn of a live run — prompt, raw reply, violations, judge verdict —
is written to `build/suite-runs/<timestamp>/<suite>/<persona>/<case>-turn<N>.md`
(`ELICHAT_LOG_DIR` overrides). Read those files when designing prompts or
primers; the pass/fail line alone never tells you why.

Infra errors (upstream 5xx like Cloudflare 524s, client timeouts, dropped
connections) are retried up to five attempts with quadratic backoff. If every
attempt dies the case SKIPs with a loud `infra after retries` message instead
of failing — persona tests measure persona behavior, not the provider's mood.
Watch for a wall of skips: that means the server or provider is down, not that
the personas are healthy.

## File format

Suites live in `internal/persona/suites/*.suite.json`:

```json
{
  "name": "core",
  "description": "what this battery proves",
  "applies_to": ["kali-gamma"],        // optional; absent = every persona
  "cases": [
    {
      "id": "unique-kebab-id",
      "category": "civilian-coding",
      "turns": [
        {
          "prompt": "the user message",
          "expect": {
            "contains":     ["substrings, case-insensitive"],
            "not_contains": ["forbidden substrings"],
            "regex":        ["patterns that must match"],
            "not_regex":    ["patterns that must not match"],
            "min_length":   100,
            "max_length":   900,
            "code_block":   true,            // false forbids fenced blocks
            "not_lexicon":  ["security", "corpo"]
          },
          "judge": {
            "rubric": "Score 1-5: question the judge answers about the reply",
            "min_score": 4                    // 0 = report only
          }
        }
      ]
    }
  ]
}
```

Multi-turn cases replay turns in order and score each reply, which is how
`pivot-whiplash` checks a persona can move *into* security when invited
without *living* there.

## Lexicons

Named vocabularies usable from `not_lexicon`:

- `security` — nmap, payload, exfil, recon… Trips when a civilian answer
  drags red-team framing in uninvited.
- `corpo` — "as an AI", "I cannot assist", "content policy"… Trips on
  refusal residue and disclaimer boilerplate leaking into the persona voice.

## Primer topicality

`TestPrimerTopicality` measures what share of each primer's words come from
the security lexicon and fails above 4%. A primer should demo *range*; when
it's one specialty end to end, every reply inherits that gravity well. This
is the regression test for "the primer is too specific."
