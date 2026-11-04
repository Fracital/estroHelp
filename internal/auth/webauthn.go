package auth

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jackc/pgx/v5"
)

const (
	challengeLifetime = 5 * time.Minute
	maxPasskeysEach   = 10
	rpDisplayName     = "Estro"
)

type Passkey struct {
	ID       int        `json:"id"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"lastUsed"`
	BackedUp bool       `json:"backedUp"`
}

// relyingParty picks the WebAuthn RP for a request. PUBLIC_HOST pins the RP id
// and origin in production; without it the request's own host is used, which
// is what a dev server on 127.0.0.1 needs.
var (
	rpMu    sync.Mutex
	rpCache = map[string]*webauthn.WebAuthn{}
)

func relyingParty(r *http.Request) (*webauthn.WebAuthn, error) {
	scheme := "http"
	if secureRequest(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(os.Getenv("PUBLIC_HOST"))
	if host != "" {
		scheme = "https"
	} else {
		host = r.Host
	}
	rpID := host
	if h, _, err := splitHostPort(host); err == nil {
		rpID = h
	}
	origin := scheme + "://" + host
	rpMu.Lock()
	defer rpMu.Unlock()
	if rp, ok := rpCache[origin]; ok {
		return rp, nil
	}
	rp, err := webauthn.New(&webauthn.Config{RPDisplayName: rpDisplayName, RPID: rpID, RPOrigins: []string{origin}})
	if err != nil {
		return nil, err
	}
	rpCache[origin] = rp
	return rp, nil
}

func splitHostPort(host string) (string, string, error) {
	i := strings.LastIndex(host, ":")
	if i < 0 || strings.Contains(host[i:], "]") {
		return "", "", errors.New("no port")
	}
	return host[:i], host[i+1:], nil
}

// waUser adapts an account to webauthn.User. The user handle is the account id,
// so a discoverable login can find the account before any username is typed.
type waUser struct {
	user  *User
	creds []webauthn.Credential
}

func userHandle(id int) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(id))
	return b
}

func (u *waUser) WebAuthnID() []byte                         { return userHandle(u.user.ID) }
func (u *waUser) WebAuthnName() string                       { return u.user.Username }
func (u *waUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

type passkeyRow struct {
	ID     int
	UserID int
	Cred   webauthn.Credential
}

func (s *Store) passkeyCredentials(ctx context.Context, userID int) ([]webauthn.Credential, error) {
	rows, err := s.DB.Query(ctx, `SELECT credential_id, public_key, coalesce(aaguid, ''::bytea), sign_count, transports, attestation_type, backup_eligible FROM passkey WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webauthn.Credential
	for rows.Next() {
		var c webauthn.Credential
		var transports []string
		if err := rows.Scan(&c.ID, &c.PublicKey, &c.Authenticator.AAGUID, &c.Authenticator.SignCount, &transports, &c.AttestationType, &c.Flags.BackupEligible); err != nil {
			return nil, err
		}
		for _, t := range transports {
			c.Transport = append(c.Transport, protocol.AuthenticatorTransport(t))
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) waUser(ctx context.Context, u *User) (*waUser, error) {
	creds, err := s.passkeyCredentials(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &waUser{user: u, creds: creds}, nil
}

func (s *Store) ListPasskeys(ctx context.Context, userID int) ([]Passkey, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, name, created, last_used, backup_eligible FROM passkey WHERE user_id=$1 ORDER BY created`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		if err := rows.Scan(&p.ID, &p.Name, &p.Created, &p.LastUsed, &p.BackedUp); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PasskeyCount(ctx context.Context, userID int) int {
	var n int
	s.DB.QueryRow(ctx, `SELECT count(*) FROM passkey WHERE user_id=$1`, userID).Scan(&n)
	return n
}

func (s *Store) saveChallenge(ctx context.Context, userID *int, kind string, session *webauthn.SessionData) (string, error) {
	id := RandomToken()
	data, _ := json.Marshal(session)
	_, err := s.DB.Exec(ctx, `INSERT INTO webauthn_challenge (id, user_id, kind, challenge, session_data, expires) VALUES ($1,$2,$3,$4,$5,$6)`,
		id, userID, kind, []byte(session.Challenge), data, time.Now().Add(challengeLifetime))
	return id, err
}

// takeChallenge loads and deletes a ceremony in one step so it cannot be replayed.
func (s *Store) takeChallenge(ctx context.Context, id, kind string, userID *int) (*webauthn.SessionData, error) {
	var data []byte
	var owner *int
	err := s.DB.QueryRow(ctx, `DELETE FROM webauthn_challenge WHERE id=$1 AND kind=$2 AND expires > NOW() RETURNING session_data, user_id`, id, kind).Scan(&data, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errors.New("challenge expired or unknown")
	}
	if err != nil {
		return nil, err
	}
	if userID != nil && (owner == nil || *owner != *userID) {
		return nil, errors.New("challenge belongs to another account")
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	go s.DB.Exec(context.Background(), `DELETE FROM webauthn_challenge WHERE expires < NOW()`)
	return &session, nil
}

func (h *Handlers) RegisterPasskeys(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/passkeys", RequireUser(h.listPasskeys))
	mux.HandleFunc("POST /api/account/passkeys/register/begin", RequireUser(h.passkeyRegisterBegin))
	mux.HandleFunc("POST /api/account/passkeys/register/finish", RequireUser(h.passkeyRegisterFinish))
	mux.HandleFunc("PATCH /api/account/passkeys/{id}", RequireUser(h.renamePasskey))
	mux.HandleFunc("DELETE /api/account/passkeys/{id}", RequireUser(h.deletePasskey))
	mux.HandleFunc("POST /api/auth/passkey/begin", h.passkeyLoginBegin)
	mux.HandleFunc("POST /api/auth/passkey/finish", h.passkeyLoginFinish)
}

func (h *Handlers) listPasskeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.Store.ListPasskeys(r.Context(), UserFrom(r.Context()).ID)
	if err != nil {
		WriteError(w, 500, "could not list passkeys")
		return
	}
	WriteJSON(w, 200, map[string]any{"passkeys": keys})
}

func (h *Handlers) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	rp, err := relyingParty(r)
	if err != nil {
		WriteError(w, 500, "passkeys are not configured: "+err.Error())
		return
	}
	wu, err := h.Store.waUser(r.Context(), u)
	if err != nil {
		WriteError(w, 500, "could not load passkeys")
		return
	}
	if len(wu.creds) >= maxPasskeysEach {
		WriteError(w, 400, "too many passkeys; remove one first")
		return
	}
	var exclude []protocol.CredentialDescriptor
	for _, c := range wu.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, session, err := rp.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationPreferred,
		}),
		webauthn.WithExclusions(exclude))
	if err != nil {
		WriteError(w, 500, "could not start registration")
		return
	}
	id, err := h.Store.saveChallenge(r.Context(), &u.ID, "register", session)
	if err != nil {
		WriteError(w, 500, "could not save challenge")
		return
	}
	WriteJSON(w, 200, map[string]any{"challengeId": id, "options": creation.Response})
}

func (h *Handlers) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	var body struct {
		ChallengeID string          `json:"challengeId"`
		Name        string          `json:"name"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := ReadJSON(r, &body); err != nil || len(body.Credential) == 0 {
		WriteError(w, 400, "invalid json")
		return
	}
	rp, err := relyingParty(r)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	session, err := h.Store.takeChallenge(r.Context(), body.ChallengeID, "register", &u.ID)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		WriteError(w, 400, "malformed credential")
		return
	}
	wu, err := h.Store.waUser(r.Context(), u)
	if err != nil {
		WriteError(w, 500, "could not load passkeys")
		return
	}
	cred, err := rp.CreateCredential(wu, *session, parsed)
	if err != nil {
		WriteError(w, 400, "passkey registration failed: "+err.Error())
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	var id int
	err = h.Store.DB.QueryRow(r.Context(), `INSERT INTO passkey (user_id, credential_id, public_key, aaguid, sign_count, transports, attestation_type, backup_eligible, name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`,
		u.ID, cred.ID, cred.PublicKey, cred.Authenticator.AAGUID, int64(cred.Authenticator.SignCount), transports, cred.AttestationType, cred.Flags.BackupEligible, name).Scan(&id)
	if isUniqueViolation(err) {
		WriteError(w, 409, "this passkey is already registered")
		return
	}
	if err != nil {
		WriteError(w, 500, "could not save passkey")
		return
	}
	h.Store.Audit(r.Context(), u.Username, "passkey_add", name, strconv.Itoa(id))
	WriteJSON(w, 201, map[string]any{"passkey": Passkey{ID: id, Name: name, Created: time.Now(), BackedUp: cred.Flags.BackupEligible}})
}

func (h *Handlers) renamePasskey(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	id, _ := strconv.Atoi(r.PathValue("id"))
	var body struct {
		Name string `json:"name"`
	}
	if err := ReadJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		WriteError(w, 400, "name required")
		return
	}
	name := strings.TrimSpace(body.Name)
	if len(name) > 60 {
		name = name[:60]
	}
	tag, err := h.Store.DB.Exec(r.Context(), `UPDATE passkey SET name=$1 WHERE id=$2 AND user_id=$3`, name, id, u.ID)
	if err != nil || tag.RowsAffected() == 0 {
		WriteError(w, 404, "no such passkey")
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) deletePasskey(w http.ResponseWriter, r *http.Request) {
	u := UserFrom(r.Context())
	id, _ := strconv.Atoi(r.PathValue("id"))
	tag, err := h.Store.DB.Exec(r.Context(), `DELETE FROM passkey WHERE id=$1 AND user_id=$2`, id, u.ID)
	if err != nil || tag.RowsAffected() == 0 {
		WriteError(w, 404, "no such passkey")
		return
	}
	h.Store.Audit(r.Context(), u.Username, "passkey_remove", strconv.Itoa(id), "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	rp, err := relyingParty(r)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	assertion, session, err := rp.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		WriteError(w, 500, "could not start sign-in")
		return
	}
	id, err := h.Store.saveChallenge(r.Context(), nil, "login", session)
	if err != nil {
		WriteError(w, 500, "could not save challenge")
		return
	}
	WriteJSON(w, 200, map[string]any{"challengeId": id, "options": assertion.Response})
}

// passkeyLoginFinish validates a discoverable assertion and opens a session. A
// passkey is a strong factor, so TOTP is not asked for on this path.
func (h *Handlers) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChallengeID string          `json:"challengeId"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := ReadJSON(r, &body); err != nil || len(body.Credential) == 0 {
		WriteError(w, 400, "invalid json")
		return
	}
	rp, err := relyingParty(r)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	session, err := h.Store.takeChallenge(r.Context(), body.ChallengeID, "login", nil)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		WriteError(w, 400, "malformed assertion")
		return
	}
	var owner *User
	var rowID int
	handler := func(rawID, handle []byte) (webauthn.User, error) {
		var userID int
		if err := h.Store.DB.QueryRow(r.Context(), `SELECT id, user_id FROM passkey WHERE credential_id=$1`, rawID).Scan(&rowID, &userID); err != nil {
			return nil, errors.New("unknown passkey")
		}
		if len(handle) == 8 && !bytes.Equal(handle, userHandle(userID)) {
			return nil, errors.New("passkey does not belong to that account")
		}
		u, err := h.Store.UserByID(r.Context(), userID)
		if err != nil {
			return nil, err
		}
		owner = u
		return h.Store.waUser(r.Context(), u)
	}
	cred, err := rp.ValidateDiscoverableLogin(handler, *session, parsed)
	if err != nil {
		WriteError(w, 401, "passkey sign-in failed")
		return
	}
	if cred.Authenticator.CloneWarning {
		h.Store.Audit(r.Context(), owner.Username, "passkey_clone_warning", strconv.Itoa(rowID), "")
		WriteError(w, 401, "this passkey looks cloned; remove it and register a new one")
		return
	}
	h.Store.DB.Exec(r.Context(), `UPDATE passkey SET sign_count=$1, last_used=NOW() WHERE id=$2`, int64(cred.Authenticator.SignCount), rowID)
	if ban, _ := h.Store.ActiveBan(r.Context(), owner.ID); ban != nil {
		WriteJSON(w, 403, map[string]any{"error": "account banned", "ban": ban})
		return
	}
	h.finishLogin(w, r, owner.ID)
}
