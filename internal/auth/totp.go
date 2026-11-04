package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const (
	mfaTokenLifetime = 5 * time.Minute
	mfaMaxAttempts   = 5
	backupCodeCount  = 10
)

// mfaSecret signs the short-lived token handed out between password and code.
// Random per process unless SESSION_SECRET pins it across restarts.
var mfaSecret = func() []byte {
	if s := os.Getenv("SESSION_SECRET"); s != "" {
		sum := sha256.Sum256([]byte(s))
		return sum[:]
	}
	b := make([]byte, 32)
	rand.Read(b)
	return b
}()

type mfaAttempt struct {
	count   int
	expires time.Time
}

var (
	mfaMu       sync.Mutex
	mfaAttempts = map[string]*mfaAttempt{}
)

// newMFAToken returns "userID.exp.nonce.sig".
func newMFAToken(userID int) string {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	body := fmt.Sprintf("%d.%d.%s", userID, time.Now().Add(mfaTokenLifetime).Unix(), hex.EncodeToString(nonce))
	return body + "." + mfaSign(body)
}

func mfaSign(body string) string {
	m := hmac.New(sha256.New, mfaSecret)
	m.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// parseMFAToken validates the signature and expiry and returns the user id and nonce.
func parseMFAToken(token string) (int, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return 0, "", errors.New("malformed token")
	}
	body := strings.Join(parts[:3], ".")
	if subtle.ConstantTimeCompare([]byte(mfaSign(body)), []byte(parts[3])) != 1 {
		return 0, "", errors.New("bad signature")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, "", errors.New("token expired")
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, "", errors.New("malformed token")
	}
	return id, parts[2], nil
}

// mfaTry counts an attempt for a nonce; false once the cap is reached.
func mfaTry(nonce string) bool {
	mfaMu.Lock()
	defer mfaMu.Unlock()
	now := time.Now()
	for k, a := range mfaAttempts {
		if now.After(a.expires) {
			delete(mfaAttempts, k)
		}
	}
	a, ok := mfaAttempts[nonce]
	if !ok {
		a = &mfaAttempt{expires: now.Add(mfaTokenLifetime)}
		mfaAttempts[nonce] = a
	}
	a.count++
	return a.count <= mfaMaxAttempts
}

func mfaDone(nonce string) {
	mfaMu.Lock()
	defer mfaMu.Unlock()
	mfaAttempts[nonce] = &mfaAttempt{count: mfaMaxAttempts + 1, expires: time.Now().Add(mfaTokenLifetime)}
}

func validTOTP(code, secret string) bool {
	ok, _ := totp.ValidateCustom(strings.ReplaceAll(strings.TrimSpace(code), " ", ""), secret, time.Now().UTC(),
		totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	return ok
}

func newBackupCodes() ([]string, []string) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	codes := make([]string, backupCodeCount)
	hashes := make([]string, backupCodeCount)
	for i := range codes {
		b := make([]byte, 10)
		rand.Read(b)
		var sb strings.Builder
		for j, x := range b {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(x)%len(alphabet)])
		}
		codes[i] = sb.String()
		hashes[i] = HashToken(normalizeBackup(codes[i]))
	}
	return codes, hashes
}

func normalizeBackup(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
}

// consumeBackupCode removes a matching backup code; false when none matched.
func (s *Store) consumeBackupCode(ctx context.Context, userID int, code string) bool {
	h := HashToken(normalizeBackup(code))
	tag, err := s.DB.Exec(ctx, `UPDATE users SET totp_backup_codes = array_remove(totp_backup_codes, $1) WHERE id=$2 AND $1 = ANY(totp_backup_codes)`, h, userID)
	return err == nil && tag.RowsAffected() == 1
}

type totpState struct {
	Secret  string
	Pending string
	Enabled bool
	Backups int
}

func (s *Store) totpState(ctx context.Context, userID int) (*totpState, error) {
	var st totpState
	var secret, pending *string
	err := s.DB.QueryRow(ctx, `SELECT totp_secret, totp_pending_secret, totp_enabled, cardinality(totp_backup_codes) FROM users WHERE id=$1`, userID).Scan(&secret, &pending, &st.Enabled, &st.Backups)
	if err != nil {
		return nil, err
	}
	if secret != nil {
		st.Secret = *secret
	}
	if pending != nil {
		st.Pending = *pending
	}
	return &st, nil
}

// verifySecondFactor accepts a current TOTP code or an unused backup code.
func (s *Store) verifySecondFactor(ctx context.Context, userID int, secret, code string) bool {
	if validTOTP(code, secret) {
		return true
	}
	return len(normalizeBackup(code)) >= 8 && s.consumeBackupCode(ctx, userID, code)
}

func (h *Handlers) RegisterTOTP(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account/totp/begin", RequireUser(h.totpBegin))
	mux.HandleFunc("POST /api/account/totp/enable", RequireUser(h.totpEnable))
	mux.HandleFunc("POST /api/account/totp/disable", RequireUser(h.totpDisable))
	mux.HandleFunc("POST /api/account/totp/backup-codes", RequireUser(h.totpBackupCodes))
	mux.HandleFunc("POST /api/auth/login/totp", h.loginTOTP)
}

func (h *Handlers) totpBegin(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	key, err := totp.Generate(totp.GenerateOpts{Issuer: rpDisplayName, AccountName: u.Username})
	if err != nil {
		WriteError(w, 500, "could not create a secret")
		return
	}
	if _, err := h.Store.DB.Exec(r.Context(), `UPDATE users SET totp_pending_secret=$1 WHERE id=$2`, key.Secret(), u.ID); err != nil {
		WriteError(w, 500, "could not save the secret")
		return
	}
	qr := ""
	if img, err := key.Image(220, 220); err == nil {
		var buf bytes.Buffer
		if png.Encode(&buf, img) == nil {
			qr = "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
		}
	}
	WriteJSON(w, 200, map[string]any{"secret": key.Secret(), "url": key.URL(), "qr": qr})
}

func (h *Handlers) totpEnable(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	st, err := h.Store.totpState(r.Context(), u.ID)
	if err != nil || st.Pending == "" {
		WriteError(w, 400, "start enrolment first")
		return
	}
	if !validTOTP(body.Code, st.Pending) {
		WriteError(w, 400, "that code did not match; check the time on your device and try again")
		return
	}
	codes, hashes := newBackupCodes()
	if _, err := h.Store.DB.Exec(r.Context(), `UPDATE users SET totp_secret=totp_pending_secret, totp_pending_secret=NULL, totp_enabled=TRUE, totp_backup_codes=$1 WHERE id=$2`, hashes, u.ID); err != nil {
		WriteError(w, 500, "could not enable two-factor")
		return
	}
	h.Store.RevokeOtherSessions(r.Context(), u.ID, SessionHashFrom(r.Context()))
	h.Store.Audit(r.Context(), u.Username, "totp_enable", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true, "backupCodes": codes})
}

func (h *Handlers) totpDisable(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	st, err := h.Store.totpState(r.Context(), u.ID)
	if err != nil || !st.Enabled {
		WriteError(w, 400, "two-factor is not enabled")
		return
	}
	if !h.Store.verifySecondFactor(r.Context(), u.ID, st.Secret, body.Code) {
		WriteError(w, 400, "that code did not match")
		return
	}
	h.Store.DB.Exec(r.Context(), `UPDATE users SET totp_secret=NULL, totp_pending_secret=NULL, totp_enabled=FALSE, totp_backup_codes='{}' WHERE id=$1`, u.ID)
	h.Store.Audit(r.Context(), u.Username, "totp_disable", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) totpBackupCodes(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		Code string `json:"code"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	st, err := h.Store.totpState(r.Context(), u.ID)
	if err != nil || !st.Enabled {
		WriteError(w, 400, "two-factor is not enabled")
		return
	}
	if !validTOTP(body.Code, st.Secret) {
		WriteError(w, 400, "enter a current code from your authenticator")
		return
	}
	codes, hashes := newBackupCodes()
	h.Store.DB.Exec(r.Context(), `UPDATE users SET totp_backup_codes=$1 WHERE id=$2`, hashes, u.ID)
	h.Store.Audit(r.Context(), u.Username, "totp_backup_regenerate", u.Username, "")
	WriteJSON(w, 200, map[string]any{"backupCodes": codes})
}

// loginTOTP completes a password sign-in that was answered with {mfa:"totp"}.
func (h *Handlers) loginTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MFAToken string `json:"mfaToken"`
		Code     string `json:"code"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	userID, nonce, err := parseMFAToken(body.MFAToken)
	if err != nil {
		WriteError(w, 401, "sign in again")
		return
	}
	if !mfaTry(nonce) {
		WriteError(w, 429, "too many attempts; sign in again")
		return
	}
	st, err := h.Store.totpState(r.Context(), userID)
	if err != nil || !st.Enabled {
		WriteError(w, 401, "sign in again")
		return
	}
	if !h.Store.verifySecondFactor(r.Context(), userID, st.Secret, body.Code) {
		WriteError(w, 401, "wrong code")
		return
	}
	mfaDone(nonce)
	if ban, _ := h.Store.ActiveBan(r.Context(), userID); ban != nil {
		WriteJSON(w, 403, map[string]any{"error": "account banned", "ban": ban})
		return
	}
	h.finishLogin(w, r, userID)
}
