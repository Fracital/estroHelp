package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"elichat/internal/auth"
	"elichat/internal/persona"
)

type Thread struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Model   string    `json:"model"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

type Message struct {
	Seq     int             `json:"seq"`
	Role    string          `json:"role"`
	Content string          `json:"content"`
	Model   *string         `json:"model"`
	Data    json.RawMessage `json:"data,omitempty"`
	Created time.Time       `json:"created"`
}

const messageDataLimit = 12 << 20

func (c *Chat) RegisterThreads(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/threads", auth.RequireUser(c.listThreads))
	mux.HandleFunc("POST /api/threads", auth.RequireUser(c.createThread))
	mux.HandleFunc("GET /api/threads/{id}", auth.RequireUser(c.getThread))
	mux.HandleFunc("PATCH /api/threads/{id}", auth.RequireUser(c.updateThread))
	mux.HandleFunc("DELETE /api/threads/{id}", auth.RequireUser(c.deleteThread))
	mux.HandleFunc("POST /api/threads/{id}/messages", auth.RequireUser(c.appendMessage))
	mux.HandleFunc("DELETE /api/threads/{id}/messages/{seq}", auth.RequireUser(c.truncateMessages))
}

func newID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (c *Chat) ownThread(r *http.Request) (*Thread, error) {
	u := auth.UserFrom(r.Context())
	var t Thread
	err := c.DB.QueryRow(r.Context(), `SELECT id, title, model, created, updated FROM thread WHERE id=$1 AND user_id=$2`, r.PathValue("id"), u.ID).
		Scan(&t.ID, &t.Title, &t.Model, &t.Created, &t.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("no such thread")
	}
	return &t, err
}

func (c *Chat) listThreads(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	rows, err := c.DB.Query(r.Context(), `SELECT id, title, model, created, updated FROM thread WHERE user_id=$1 ORDER BY updated DESC LIMIT 200`, u.ID)
	if err != nil {
		auth.WriteError(w, 500, "could not list threads")
		return
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		var t Thread
		rows.Scan(&t.ID, &t.Title, &t.Model, &t.Created, &t.Updated)
		out = append(out, t)
	}
	auth.WriteJSON(w, 200, map[string]any{"threads": out})
}

func (c *Chat) createThread(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var body struct{ Model, Title string }
	auth.ReadJSON(r, &body)
	p, ok := persona.Resolve(body.Model)
	if !ok {
		p = persona.All()[0]
	}
	t := Thread{ID: newID(), Model: p.ID, Title: strings.TrimSpace(body.Title)}
	if t.Title == "" {
		t.Title = "New chat"
	}
	err := c.DB.QueryRow(r.Context(), `INSERT INTO thread (id, user_id, title, model) VALUES ($1,$2,$3,$4) RETURNING created, updated`, t.ID, u.ID, t.Title, t.Model).Scan(&t.Created, &t.Updated)
	if err != nil {
		auth.WriteError(w, 500, "could not create thread")
		return
	}
	auth.WriteJSON(w, 201, t)
}

func (c *Chat) getThread(w http.ResponseWriter, r *http.Request) {
	t, err := c.ownThread(r)
	if err != nil {
		auth.WriteError(w, 404, err.Error())
		return
	}
	rows, err := c.DB.Query(r.Context(), `SELECT seq, role, content, model, data, created FROM thread_message WHERE thread_id=$1 ORDER BY seq`, t.ID)
	if err != nil {
		auth.WriteError(w, 500, "could not load messages")
		return
	}
	defer rows.Close()
	msgs := []Message{}
	for rows.Next() {
		var m Message
		var data []byte
		rows.Scan(&m.Seq, &m.Role, &m.Content, &m.Model, &data, &m.Created)
		if len(data) > 0 {
			m.Data = json.RawMessage(data)
		}
		msgs = append(msgs, m)
	}
	auth.WriteJSON(w, 200, map[string]any{"thread": t, "messages": msgs})
}

func (c *Chat) updateThread(w http.ResponseWriter, r *http.Request) {
	t, err := c.ownThread(r)
	if err != nil {
		auth.WriteError(w, 404, err.Error())
		return
	}
	var body struct{ Title, Model *string }
	if err := auth.ReadJSON(r, &body); err != nil {
		auth.WriteError(w, 400, "invalid json")
		return
	}
	if body.Title != nil {
		if s := strings.TrimSpace(*body.Title); s != "" {
			t.Title = truncateRunes(s, 120)
		}
	}
	if body.Model != nil {
		if p, ok := persona.Resolve(*body.Model); ok {
			t.Model = p.ID
		}
	}
	c.DB.Exec(r.Context(), `UPDATE thread SET title=$1, model=$2 WHERE id=$3`, t.Title, t.Model, t.ID)
	auth.WriteJSON(w, 200, t)
}

func (c *Chat) deleteThread(w http.ResponseWriter, r *http.Request) {
	t, err := c.ownThread(r)
	if err != nil {
		auth.WriteError(w, 404, err.Error())
		return
	}
	c.DB.Exec(r.Context(), `DELETE FROM thread WHERE id=$1`, t.ID)
	auth.WriteJSON(w, 200, map[string]any{"ok": true})
}

func (c *Chat) appendMessage(w http.ResponseWriter, r *http.Request) {
	t, err := c.ownThread(r)
	if err != nil {
		auth.WriteError(w, 404, err.Error())
		return
	}
	var body struct {
		Role, Content string
		Model         *string
		Data          json.RawMessage
	}
	if err := auth.ReadJSONLimit(r, &body, messageDataLimit); err != nil {
		auth.WriteError(w, 400, "invalid json")
		return
	}
	if body.Role != "user" && body.Role != "assistant" && body.Role != "tool" {
		auth.WriteError(w, 400, "role must be user, assistant or tool")
		return
	}
	if len(body.Content) > 1<<20 {
		auth.WriteError(w, 413, "message too long")
		return
	}
	var data any
	if len(body.Data) > 0 && string(body.Data) != "null" {
		if !json.Valid(body.Data) {
			auth.WriteError(w, 400, "data must be a JSON object")
			return
		}
		data = body.Data
	}
	var m Message
	err = c.DB.QueryRow(r.Context(), `INSERT INTO thread_message (thread_id, seq, role, content, model, data)
		VALUES ($1, (SELECT coalesce(max(seq),0)+1 FROM thread_message WHERE thread_id=$1), $2, $3, $4, $5) RETURNING seq, created`,
		t.ID, body.Role, body.Content, body.Model, data).Scan(&m.Seq, &m.Created)
	if err != nil {
		auth.WriteError(w, 500, "could not save message")
		return
	}
	m.Role, m.Content, m.Model = body.Role, body.Content, body.Model
	if data != nil {
		m.Data = body.Data
	}
	if m.Seq == 1 && body.Role == "user" && t.Title == "New chat" {
		t.Title = autoTitle(body.Content)
	}
	c.DB.Exec(r.Context(), `UPDATE thread SET updated=NOW(), title=$2 WHERE id=$1`, t.ID, t.Title)
	auth.WriteJSON(w, 201, map[string]any{"message": m, "thread": t})
}

// truncateMessages deletes a message and everything after it (edit / retry).
func (c *Chat) truncateMessages(w http.ResponseWriter, r *http.Request) {
	t, err := c.ownThread(r)
	if err != nil {
		auth.WriteError(w, 404, err.Error())
		return
	}
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil {
		auth.WriteError(w, 400, "bad seq")
		return
	}
	c.DB.Exec(r.Context(), `DELETE FROM thread_message WHERE thread_id=$1 AND seq>=$2`, t.ID, seq)
	auth.WriteJSON(w, 200, map[string]any{"ok": true})
}

func autoTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncateRunes(s, 60)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
