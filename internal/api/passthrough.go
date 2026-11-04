package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/provider"
	"elichat/internal/relay"
)

// passthrough serves the OpenAI endpoints that carry no conversation to apply
// a persona to (legacy completions, embeddings): the model is swapped and the
// body relayed as is, through the same gate and metering as chat.
func (c *Chat) passthrough(dialect provider.Dialect) http.HandlerFunc {
	return auth.RequireUser(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFrom(r.Context())
		raw, ok := readBody(w, r, relay.OpenAI)
		if !ok {
			return
		}
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &req); err != nil || json.Unmarshal(raw, &body) != nil {
			openAIError(w, 400, "invalid JSON body", "bad_request")
			return
		}
		p, prov, ok := c.gate(w, r, req.Model, func(s int, m, k string) { openAIError(w, s, m, k) })
		if !ok {
			return
		}
		if _, ok := c.admit(w, r, "openai", p.ID, func(s int, m, k string) { openAIError(w, s, m, k) }); !ok {
			return
		}
		sanitizeCommon(body)
		if dialect == provider.Completions && req.Stream {
			opts := map[string]json.RawMessage{}
			if o, ok := body["stream_options"]; ok {
				json.Unmarshal(o, &opts)
			}
			opts["include_usage"] = json.RawMessage("true")
			body["stream_options"], _ = json.Marshal(opts)
		}
		body["model"], _ = json.Marshal(p.BaseModel)
		out, _ := json.Marshal(body)

		slog.Info("chat", "dialect", string(dialect), "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream)
		rw := newModelRewriter(w, p.ID, relay.OpenAI, false)
		status := prov.Serve(rw, r, dialect, out)
		rw.Close()
		c.finish(r.Context(), u, p.ID, status, rw, len(raw))
	})
}
