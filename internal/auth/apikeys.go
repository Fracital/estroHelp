package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	APIKeyPrefix   = "estro_"
	maxAPIKeysEach = 20
)

type APIKey struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Prefix   string     `json:"prefix"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed"`
}

func IsAPIKey(token string) bool { return strings.HasPrefix(token, APIKeyPrefix) }

// UserForAPIKey resolves a kali_ token to its owner and marks it used.
func (s *Store) UserForAPIKey(ctx context.Context, key string) (*User, error) {
	h := HashToken(key)
	u, err := scanUser(s.DB.QueryRow(ctx, `SELECT u.`+strings.ReplaceAll(userCols, ", ", ", u.")+` FROM api_key k JOIN users u ON u.id=k.user_id WHERE k.key_hash=$1`, h))
	if err != nil {
		return nil, err
	}
	go s.DB.Exec(context.Background(), `UPDATE api_key SET last_used=NOW() WHERE key_hash=$1 AND (last_used IS NULL OR last_used < NOW() - interval '1 minute')`, h)
	return u, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, userID int) ([]APIKey, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, name, prefix, created, last_used FROM api_key WHERE user_id=$1 ORDER BY created DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Created, &k.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// CreateAPIKey returns the key row and the plaintext secret, shown once.
func (s *Store) CreateAPIKey(ctx context.Context, userID int, name string) (*APIKey, string, error) {
	secret := APIKeyPrefix + RandomToken()
	k := &APIKey{Name: name, Prefix: secret[:len(APIKeyPrefix)+8]}
	err := s.DB.QueryRow(ctx, `INSERT INTO api_key (user_id, name, prefix, key_hash) VALUES ($1,$2,$3,$4) RETURNING id, created`,
		userID, name, k.Prefix, HashToken(secret)).Scan(&k.ID, &k.Created)
	if err != nil {
		return nil, "", err
	}
	return k, secret, nil
}

func (h *Handlers) RegisterAPIKeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/api-keys", RequireUser(h.listAPIKeys))
	mux.HandleFunc("POST /api/account/api-keys", RequireUser(h.createAPIKey))
	mux.HandleFunc("DELETE /api/account/api-keys/{id}", RequireUser(h.revokeAPIKey))
}

func (h *Handlers) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Store.ListAPIKeys(r.Context(), UserFrom(r.Context()).ID)
	if err != nil {
		WriteError(w, 500, "could not list keys")
		return
	}
	WriteJSON(w, 200, map[string]any{"keys": keys})
}

func (h *Handlers) createAPIKey(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "API key"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	var count int
	h.Store.DB.QueryRow(r.Context(), `SELECT count(*) FROM api_key WHERE user_id=$1`, u.ID).Scan(&count)
	if count >= maxAPIKeysEach {
		WriteError(w, 400, "too many keys; revoke one first")
		return
	}
	key, secret, err := h.Store.CreateAPIKey(r.Context(), u.ID, name)
	if err != nil {
		WriteError(w, 500, "could not create key")
		return
	}
	h.Store.Audit(r.Context(), u.Username, "api_key_create", key.Prefix, name)
	WriteJSON(w, 201, map[string]any{"key": key, "secret": secret})
}

func (h *Handlers) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		WriteError(w, 400, "bad id")
		return
	}
	tag, err := h.Store.DB.Exec(r.Context(), `DELETE FROM api_key WHERE id=$1 AND user_id=$2`, id, u.ID)
	if err != nil || tag.RowsAffected() == 0 {
		WriteError(w, 404, "no such key")
		return
	}
	h.Store.Audit(r.Context(), u.Username, "api_key_revoke", strconv.Itoa(id), "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}
