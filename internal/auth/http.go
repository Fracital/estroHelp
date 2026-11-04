package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type ctxKey int

const (
	userKey ctxKey = iota
	sessionKey
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]any{"error": msg})
}

func ReadJSON(r *http.Request, v any) error {
	return ReadJSONLimit(r, v, 1<<20)
}

func ReadJSONLimit(r *http.Request, v any, limit int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("empty body")
	}
	return json.Unmarshal(body, v)
}

func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(userKey).(*User)
	return u
}

func SessionHashFrom(ctx context.Context) string {
	h, _ := ctx.Value(sessionKey).(string)
	return h
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return k
	}
	return ""
}

// Authenticate resolves the session from a bearer token or the session cookie and
// stores the user in the request context. Requests without a valid session pass
// through unauthenticated; RequireUser rejects them.
func (s *Store) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		viaCookie := false
		if token == "" {
			if c, err := r.Cookie(CookieName); err == nil {
				token = c.Value
				viaCookie = true
			}
		}
		if token != "" && !viaCookie && IsAPIKey(token) {
			u, err := s.UserForAPIKey(r.Context(), token)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "invalid API key")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
			return
		}
		if token != "" {
			if u, hash, err := s.UserForToken(r.Context(), token); err == nil {
				// Cookie sessions get a cheap CSRF guard: cross-site form posts
				// cannot set a JSON content type without a CORS preflight.
				if viaCookie && r.Method != http.MethodGet && r.Method != http.MethodHead &&
					!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
					WriteError(w, http.StatusForbidden, "json content type required")
					return
				}
				ctx := context.WithValue(r.Context(), userKey, u)
				ctx = context.WithValue(ctx, sessionKey, hash)
				r = r.WithContext(ctx)
			} else if !viaCookie {
				WriteError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func RequireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if UserFrom(r.Context()) == nil {
			WriteError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		next(w, r)
	}
}

func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return RequireUser(func(w http.ResponseWriter, r *http.Request) {
		if !UserFrom(r.Context()).Admin {
			WriteError(w, http.StatusForbidden, "admin only")
			return
		}
		next(w, r)
	})
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r),
		SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(sessionLifetime),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: "", Path: "/", HttpOnly: true, Secure: secureRequest(r),
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}
