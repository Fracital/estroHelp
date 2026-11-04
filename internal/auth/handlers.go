package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Handlers struct {
	Store  *Store
	Mailer *Mailer
	// RequireVerifiedEmail gates chat behind email verification; /api/auth/me reports it.
	RequireVerifiedEmail bool
}

func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/register", h.register)
	mux.HandleFunc("POST /api/auth/login", h.login)
	mux.HandleFunc("POST /api/auth/logout", h.logout)
	mux.HandleFunc("GET /api/auth/me", h.me)
	mux.HandleFunc("POST /api/auth/verify-email", RequireUser(h.verifyEmail))
	mux.HandleFunc("POST /api/auth/resend-verification", RequireUser(h.resendVerification))
	mux.HandleFunc("POST /api/auth/forgot-password", h.forgotPassword)
	mux.HandleFunc("POST /api/auth/reset-password", h.resetPassword)
	mux.HandleFunc("POST /api/account/password", RequireUser(h.changePassword))
	mux.HandleFunc("POST /api/account/email", RequireUser(h.changeEmail))
	mux.HandleFunc("GET /api/account/sessions", RequireUser(h.listSessions))
	mux.HandleFunc("DELETE /api/account/sessions/{id}", RequireUser(h.revokeSession))
	mux.HandleFunc("POST /api/account/sessions/revoke-others", RequireUser(h.revokeOthers))
	mux.HandleFunc("DELETE /api/account", RequireUser(h.deleteAccount))
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (h *Handlers) issueCode(ctx context.Context, userID int, email, ip, kind string) error {
	code := RandomCode(6)
	col := "email_code"
	if kind == "reset" {
		col = "reset_code"
	}
	if _, err := h.Store.DB.Exec(ctx, `UPDATE users SET `+col+`_hash=$1, `+col+`_expires=$2 WHERE id=$3`,
		HashToken(code), time.Now().Add(codeLifetime), userID); err != nil {
		return err
	}
	h.Store.LogEmail(ctx, email, ip, kind)
	if kind == "reset" {
		return h.Mailer.SendPasswordReset(email, code)
	}
	return h.Mailer.SendVerification(email, code)
}

func (h *Handlers) register(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := ReadJSON(r, &c); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	c.Username = strings.TrimSpace(c.Username)
	if err := ValidateUsername(c.Username); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	if err := ValidatePassword(c.Password); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	email, err := NormalizeEmail(c.Email)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	ip := RequestIP(r)
	if !h.Store.CanSendEmail(r.Context(), email, ip) {
		WriteError(w, 429, "too many emails sent, try again later")
		return
	}
	var id int
	err = h.Store.DB.QueryRow(r.Context(), `INSERT INTO users (username, password_hash, email) VALUES ($1,$2,$3) RETURNING id`,
		c.Username, HashPassword(c.Password), email).Scan(&id)
	if isUniqueViolation(err) {
		WriteError(w, 409, "username or email already in use")
		return
	}
	if err != nil {
		WriteError(w, 500, "could not create account")
		return
	}
	if err := h.issueCode(r.Context(), id, email, ip, "verify"); err != nil {
		WriteError(w, 500, "could not send verification email")
		return
	}
	h.Store.Audit(r.Context(), c.Username, "register", c.Username, ip)
	h.finishLogin(w, r, id)
}

func (h *Handlers) finishLogin(w http.ResponseWriter, r *http.Request, userID int) {
	token, err := h.Store.CreateSession(r.Context(), userID, r)
	if err != nil {
		WriteError(w, 500, "could not create session")
		return
	}
	u, _ := h.Store.UserByID(r.Context(), userID)
	setSessionCookie(w, r, token)
	WriteJSON(w, 200, map[string]any{"token": token, "user": u, "requireVerifiedEmail": h.RequireVerifiedEmail})
}

func (h *Handlers) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := ReadJSON(r, &c); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	var id int
	var hash string
	err := h.Store.DB.QueryRow(r.Context(), `SELECT id, password_hash FROM users WHERE lower(username)=lower($1) OR email=lower($1)`,
		strings.TrimSpace(c.Username)).Scan(&id, &hash)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !VerifyPassword(c.Password, hash)) {
		// Still run a hash on miss so timing does not reveal whether the username exists.
		if err != nil {
			VerifyPassword(c.Password, HashPassword("x"))
		}
		WriteError(w, 401, "wrong username or password")
		return
	}
	if err != nil {
		WriteError(w, 500, "login failed")
		return
	}
	if ban, _ := h.Store.ActiveBan(r.Context(), id); ban != nil {
		WriteJSON(w, 403, map[string]any{"error": "account banned", "ban": ban})
		return
	}
	h.finishLogin(w, r, id)
}

func (h *Handlers) logout(w http.ResponseWriter, r *http.Request) {
	if hash := SessionHashFrom(r.Context()); hash != "" {
		h.Store.RevokeSession(r.Context(), hash)
	}
	clearSessionCookie(w, r)
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) me(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if u == nil {
		WriteJSON(w, 200, map[string]any{"user": nil, "requireVerifiedEmail": h.RequireVerifiedEmail})
		return
	}
	ban, _ := h.Store.ActiveBan(r.Context(), u.ID)
	WriteJSON(w, 200, map[string]any{"user": u, "ban": ban, "requireVerifiedEmail": h.RequireVerifiedEmail})
}

func (h *Handlers) verifyEmail(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	tag, err := h.Store.DB.Exec(r.Context(), `UPDATE users SET email_verified=TRUE, email_code_hash=NULL, email_code_expires=NULL
		WHERE id=$1 AND email_code_hash=$2 AND email_code_expires > NOW()`, u.ID, HashToken(strings.TrimSpace(body.Code)))
	if err != nil || tag.RowsAffected() == 0 {
		WriteError(w, 400, "wrong or expired code")
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) resendVerification(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if u.Email == nil || u.EmailVerified {
		WriteError(w, 400, "nothing to verify")
		return
	}
	ip := RequestIP(r)
	if !h.Store.CanSendEmail(r.Context(), *u.Email, ip) {
		WriteError(w, 429, "too many emails sent, try again later")
		return
	}
	if err := h.issueCode(r.Context(), u.ID, *u.Email, ip, "verify"); err != nil {
		WriteError(w, 500, "could not send email")
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	email, err := NormalizeEmail(body.Email)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	ip := RequestIP(r)
	var id int
	// Always answer ok so the endpoint does not reveal which emails are registered.
	if err := h.Store.DB.QueryRow(r.Context(), `SELECT id FROM users WHERE email=$1`, email).Scan(&id); err == nil && h.Store.CanSendEmail(r.Context(), email, ip) {
		h.issueCode(r.Context(), id, email, ip, "reset")
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email, Code, Password string
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	email, err := NormalizeEmail(body.Email)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	if err := ValidatePassword(body.Password); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	var id int
	err = h.Store.DB.QueryRow(r.Context(), `UPDATE users SET password_hash=$1, reset_code_hash=NULL, reset_code_expires=NULL
		WHERE email=$2 AND reset_code_hash=$3 AND reset_code_expires > NOW() RETURNING id`,
		HashPassword(body.Password), email, HashToken(strings.TrimSpace(body.Code))).Scan(&id)
	if err != nil {
		WriteError(w, 400, "wrong or expired code")
		return
	}
	h.Store.RevokeAllSessions(r.Context(), id)
	h.Store.Audit(r.Context(), email, "password_reset", email, RequestIP(r))
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) changePassword(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Current, Password string
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	if err := ValidatePassword(body.Password); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	var hash string
	h.Store.DB.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, u.ID).Scan(&hash)
	if !VerifyPassword(body.Current, hash) {
		WriteError(w, 401, "current password is wrong")
		return
	}
	h.Store.DB.Exec(r.Context(), `UPDATE users SET password_hash=$1 WHERE id=$2`, HashPassword(body.Password), u.ID)
	h.Store.RevokeOtherSessions(r.Context(), u.ID, SessionHashFrom(r.Context()))
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) changeEmail(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Email, Password string
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	email, err := NormalizeEmail(body.Email)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	var hash string
	h.Store.DB.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, u.ID).Scan(&hash)
	if !VerifyPassword(body.Password, hash) {
		WriteError(w, 401, "password is wrong")
		return
	}
	ip := RequestIP(r)
	if !h.Store.CanSendEmail(r.Context(), email, ip) {
		WriteError(w, 429, "too many emails sent, try again later")
		return
	}
	_, err = h.Store.DB.Exec(r.Context(), `UPDATE users SET email=$1, email_verified=FALSE WHERE id=$2`, email, u.ID)
	if isUniqueViolation(err) {
		WriteError(w, 409, "email already in use")
		return
	}
	if err != nil {
		WriteError(w, 500, "could not update email")
		return
	}
	h.issueCode(r.Context(), u.ID, email, ip, "verify")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) listSessions(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	list, err := h.Store.ListSessions(r.Context(), u.ID, SessionHashFrom(r.Context()))
	if err != nil {
		WriteError(w, 500, "could not list sessions")
		return
	}
	WriteJSON(w, 200, map[string]any{"sessions": list})
}

func (h *Handlers) revokeSession(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	if err := h.Store.RevokeSessionByPrefix(r.Context(), u.ID, r.PathValue("id")); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) revokeOthers(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	h.Store.RevokeOtherSessions(r.Context(), u.ID, SessionHashFrom(r.Context()))
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) deleteAccount(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Password string `json:"password"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	var hash string
	h.Store.DB.QueryRow(r.Context(), `SELECT password_hash FROM users WHERE id=$1`, u.ID).Scan(&hash)
	if !VerifyPassword(body.Password, hash) {
		WriteError(w, 401, "password is wrong")
		return
	}
	if _, err := h.Store.DB.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, u.ID); err != nil {
		WriteError(w, 500, "could not delete account")
		return
	}
	h.Store.Audit(r.Context(), u.Username, "delete_account", u.Username, RequestIP(r))
	clearSessionCookie(w, r)
	WriteJSON(w, 200, map[string]any{"ok": true})
}
