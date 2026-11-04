package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"elichat/internal/db"
)

const (
	CookieName      = "kali_session"
	sessionLifetime = 30 * 24 * time.Hour
)

type User struct {
	ID            int        `json:"id"`
	Username      string     `json:"username"`
	Email         *string    `json:"email"`
	EmailVerified bool       `json:"emailVerified"`
	Admin         bool       `json:"admin"`
	Sandbox       bool       `json:"sandbox"`
	Created       time.Time  `json:"created"`
	LastLogin     *time.Time `json:"lastLogin"`
	TotpEnabled   bool       `json:"totpEnabled"`
	// PasskeyCount is filled by /api/auth/me only.
	PasskeyCount int `json:"passkeyCount"`
}

type Ban struct {
	Reason  string     `json:"reason"`
	Expires *time.Time `json:"expires"`
}

type Session struct {
	TokenHash string    `json:"-"`
	ID        string    `json:"id"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	LastSeen  time.Time `json:"lastSeen"`
	IP        *string   `json:"ip"`
	UserAgent *string   `json:"userAgent"`
	Current   bool      `json:"current"`
}

type Store struct {
	DB *db.DB
}

const userCols = `id, username, email, email_verified, admin, sandbox, created, last_login, totp_enabled`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.EmailVerified, &u.Admin, &u.Sandbox, &u.Created, &u.LastLogin, &u.TotpEnabled)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) UserByID(ctx context.Context, id int) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id=$1`, id))
}

func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return scanUser(s.DB.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE lower(username)=lower($1)`, username))
}

func (s *Store) ActiveBan(ctx context.Context, userID int) (*Ban, error) {
	var b Ban
	err := s.DB.QueryRow(ctx, `SELECT reason, expires FROM ban WHERE user_id=$1 AND lifted IS NULL AND (expires IS NULL OR expires > NOW()) ORDER BY created DESC LIMIT 1`, userID).Scan(&b.Reason, &b.Expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Store) CreateSession(ctx context.Context, userID int, r *http.Request) (string, error) {
	token := RandomToken()
	_, err := s.DB.Exec(ctx, `INSERT INTO session (token_hash, user_id, expires, ip, user_agent) VALUES ($1,$2,$3,$4,$5)`,
		HashToken(token), userID, time.Now().Add(sessionLifetime), RequestIP(r), truncate(r.UserAgent(), 256))
	if err != nil {
		return "", err
	}
	s.DB.Exec(ctx, `UPDATE users SET last_login=NOW() WHERE id=$1`, userID)
	return token, nil
}

// UserForToken resolves a session token to its user, touching last_seen.
func (s *Store) UserForToken(ctx context.Context, token string) (*User, string, error) {
	h := HashToken(token)
	u, err := scanUser(s.DB.QueryRow(ctx, `SELECT u.`+strings.ReplaceAll(userCols, ", ", ", u.")+` FROM session s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires > NOW()`, h))
	if err != nil {
		return nil, "", err
	}
	go s.DB.Exec(context.Background(), `UPDATE session SET last_seen=NOW() WHERE token_hash=$1 AND last_seen < NOW() - interval '1 minute'`, h)
	return u, h, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM session WHERE token_hash=$1`, tokenHash)
	return err
}

func (s *Store) RevokeOtherSessions(ctx context.Context, userID int, keepHash string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM session WHERE user_id=$1 AND token_hash<>$2`, userID, keepHash)
	return err
}

func (s *Store) RevokeAllSessions(ctx context.Context, userID int) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM session WHERE user_id=$1`, userID)
	return err
}

func (s *Store) ListSessions(ctx context.Context, userID int, currentHash string) ([]Session, error) {
	rows, err := s.DB.Query(ctx, `SELECT token_hash, created, expires, last_seen, ip, user_agent FROM session WHERE user_id=$1 AND expires > NOW() ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var ss Session
		if err := rows.Scan(&ss.TokenHash, &ss.Created, &ss.Expires, &ss.LastSeen, &ss.IP, &ss.UserAgent); err != nil {
			return nil, err
		}
		ss.ID = ss.TokenHash[:16]
		ss.Current = ss.TokenHash == currentHash
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Store) RevokeSessionByPrefix(ctx context.Context, userID int, prefix string) error {
	if len(prefix) != 16 {
		return errors.New("bad session id")
	}
	_, err := s.DB.Exec(ctx, `DELETE FROM session WHERE user_id=$1 AND token_hash LIKE $2`, userID, prefix+"%")
	return err
}

func (s *Store) StartCleanup(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			if _, err := s.DB.Exec(ctx, `DELETE FROM session WHERE expires < NOW()`); err != nil {
				slog.Warn("session cleanup", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

func (s *Store) Audit(ctx context.Context, actor, action, target, detail string) {
	if _, err := s.DB.Exec(ctx, `INSERT INTO audit_log (actor, action, target, detail) VALUES ($1,$2,$3,$4)`, actor, action, target, detail); err != nil {
		slog.Warn("audit", "err", err)
	}
}

func RequestIP(r *http.Request) string {
	for _, h := range []string{"CF-Connecting-IP", "X-Real-IP"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
