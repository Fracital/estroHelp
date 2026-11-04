package auth

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type Admin struct {
	Store *Store
}

func (a *Admin) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/stats", RequireAdmin(a.stats))
	mux.HandleFunc("GET /api/admin/users", RequireAdmin(a.listUsers))
	mux.HandleFunc("GET /api/admin/users/{id}", RequireAdmin(a.getUser))
	mux.HandleFunc("POST /api/admin/users/{id}/ban", RequireAdmin(a.ban))
	mux.HandleFunc("POST /api/admin/users/{id}/unban", RequireAdmin(a.unban))
	mux.HandleFunc("POST /api/admin/users/{id}/sandbox", RequireAdmin(a.setFlag("sandbox")))
	mux.HandleFunc("POST /api/admin/users/{id}/admin", RequireAdmin(a.setFlag("admin")))
	mux.HandleFunc("POST /api/admin/users/{id}/verify-email", RequireAdmin(a.verifyEmail))
	mux.HandleFunc("POST /api/admin/users/{id}/revoke-sessions", RequireAdmin(a.revokeSessions))
	mux.HandleFunc("DELETE /api/admin/users/{id}", RequireAdmin(a.deleteUser))
	mux.HandleFunc("GET /api/admin/audit", RequireAdmin(a.audit))
	mux.HandleFunc("GET /api/admin/usage", RequireAdmin(a.usage))
}

type adminUser struct {
	User
	Banned        *Ban       `json:"ban"`
	RequestsToday int        `json:"requestsToday"`
	Threads       int        `json:"threads"`
	Plan          *string    `json:"plan"`
	PlanUntil     *time.Time `json:"planUntil"`
}

func pageParam(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const size = 50
	return size, (page - 1) * size
}

func (a *Admin) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var s struct {
		Users         int `json:"users"`
		Verified      int `json:"verified"`
		Sandbox       int `json:"sandbox"`
		Banned        int `json:"banned"`
		RequestsToday int `json:"requestsToday"`
		Requests7d    int `json:"requests7d"`
		ActiveToday   int `json:"activeToday"`
		Threads       int `json:"threads"`
	}
	a.Store.DB.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE email_verified), count(*) FILTER (WHERE sandbox) FROM users`).Scan(&s.Users, &s.Verified, &s.Sandbox)
	a.Store.DB.QueryRow(ctx, `SELECT count(DISTINCT user_id) FROM ban WHERE lifted IS NULL AND (expires IS NULL OR expires > NOW())`).Scan(&s.Banned)
	a.Store.DB.QueryRow(ctx, `SELECT coalesce(sum(requests),0), count(DISTINCT user_id) FROM ai_usage WHERE day=CURRENT_DATE`).Scan(&s.RequestsToday, &s.ActiveToday)
	a.Store.DB.QueryRow(ctx, `SELECT coalesce(sum(requests),0) FROM ai_usage WHERE day > CURRENT_DATE - 7`).Scan(&s.Requests7d)
	a.Store.DB.QueryRow(ctx, `SELECT count(*) FROM thread`).Scan(&s.Threads)
	WriteJSON(w, 200, s)
}

func (a *Admin) listUsers(w http.ResponseWriter, r *http.Request) {
	size, offset := pageParam(r)
	q := "%" + r.URL.Query().Get("q") + "%"
	rows, err := a.Store.DB.Query(r.Context(), `
		SELECT u.`+userCols+`,
		  (SELECT coalesce(sum(requests),0) FROM ai_usage WHERE user_id=u.id AND day=CURRENT_DATE),
		  (SELECT count(*) FROM thread WHERE user_id=u.id),
		  b.reason, b.expires,
		  CASE WHEN u.plan_until > NOW() THEN u.plan END, u.plan_until
		FROM users u
		LEFT JOIN LATERAL (SELECT reason, expires FROM ban WHERE user_id=u.id AND lifted IS NULL AND (expires IS NULL OR expires > NOW()) ORDER BY created DESC LIMIT 1) b ON TRUE
		WHERE u.username ILIKE $1 OR u.email ILIKE $1
		ORDER BY u.id DESC LIMIT $2 OFFSET $3`, q, size, offset)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []adminUser{}
	for rows.Next() {
		var au adminUser
		var reason *string
		var expires *time.Time
		if err := rows.Scan(&au.ID, &au.Username, &au.Email, &au.EmailVerified, &au.Admin, &au.Sandbox, &au.Created, &au.LastLogin, &au.TotpEnabled,
			&au.RequestsToday, &au.Threads, &reason, &expires, &au.Plan, &au.PlanUntil); err != nil {
			WriteError(w, 500, err.Error())
			return
		}
		if reason != nil {
			au.Banned = &Ban{Reason: *reason, Expires: expires}
		}
		out = append(out, au)
	}
	var total int
	a.Store.DB.QueryRow(r.Context(), `SELECT count(*) FROM users WHERE username ILIKE $1 OR email ILIKE $1`, q).Scan(&total)
	WriteJSON(w, 200, map[string]any{"users": out, "total": total, "pageSize": size})
}

func (a *Admin) target(w http.ResponseWriter, r *http.Request) (*User, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		WriteError(w, 400, "bad id")
		return nil, false
	}
	u, err := a.Store.UserByID(r.Context(), id)
	if err != nil {
		WriteError(w, 404, "no such user")
		return nil, false
	}
	return u, true
}

func (a *Admin) getUser(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	ban, _ := a.Store.ActiveBan(r.Context(), u.ID)
	rows, _ := a.Store.DB.Query(r.Context(), `SELECT day, model, requests, prompt_chars, sandbox FROM ai_usage WHERE user_id=$1 ORDER BY day DESC, model LIMIT 60`, u.ID)
	type usageRow struct {
		Day         time.Time `json:"day"`
		Model       string    `json:"model"`
		Requests    int       `json:"requests"`
		PromptChars int64     `json:"promptChars"`
		Sandbox     bool      `json:"sandbox"`
	}
	usage := []usageRow{}
	if rows != nil {
		for rows.Next() {
			var ur usageRow
			rows.Scan(&ur.Day, &ur.Model, &ur.Requests, &ur.PromptChars, &ur.Sandbox)
			usage = append(usage, ur)
		}
		rows.Close()
	}
	sessions, _ := a.Store.ListSessions(r.Context(), u.ID, "")
	WriteJSON(w, 200, map[string]any{"user": u, "ban": ban, "usage": usage, "sessions": sessions})
}

func (a *Admin) ban(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	actor := UserFrom(r.Context())
	if u.ID == actor.ID {
		WriteError(w, 400, "cannot ban yourself")
		return
	}
	var body struct {
		Reason string  `json:"reason"`
		Hours  float64 `json:"hours"`
	}
	if err := ReadJSON(r, &body); err != nil {
		WriteError(w, 400, "invalid json")
		return
	}
	var expires *time.Time
	if body.Hours > 0 {
		t := time.Now().Add(time.Duration(body.Hours * float64(time.Hour)))
		expires = &t
	}
	a.Store.DB.Exec(r.Context(), `UPDATE ban SET lifted=NOW() WHERE user_id=$1 AND lifted IS NULL`, u.ID)
	if _, err := a.Store.DB.Exec(r.Context(), `INSERT INTO ban (user_id, reason, expires, actor) VALUES ($1,$2,$3,$4)`, u.ID, body.Reason, expires, actor.Username); err != nil {
		WriteError(w, 500, "could not ban")
		return
	}
	a.Store.RevokeAllSessions(r.Context(), u.ID)
	a.Store.Audit(r.Context(), actor.Username, "ban", u.Username, fmt.Sprintf("%s (hours=%v)", body.Reason, body.Hours))
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (a *Admin) unban(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	a.Store.DB.Exec(r.Context(), `UPDATE ban SET lifted=NOW() WHERE user_id=$1 AND lifted IS NULL`, u.ID)
	a.Store.Audit(r.Context(), UserFrom(r.Context()).Username, "unban", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (a *Admin) setFlag(col string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.target(w, r)
		if !ok {
			return
		}
		var body struct {
			Value bool `json:"value"`
		}
		if err := ReadJSON(r, &body); err != nil {
			WriteError(w, 400, "invalid json")
			return
		}
		actor := UserFrom(r.Context())
		if col == "admin" && u.ID == actor.ID && !body.Value {
			WriteError(w, 400, "cannot remove your own admin flag")
			return
		}
		a.Store.DB.Exec(r.Context(), `UPDATE users SET `+col+`=$1 WHERE id=$2`, body.Value, u.ID)
		a.Store.Audit(r.Context(), actor.Username, "set_"+col, u.Username, strconv.FormatBool(body.Value))
		WriteJSON(w, 200, map[string]any{"ok": true})
	}
}

func (a *Admin) verifyEmail(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	a.Store.DB.Exec(r.Context(), `UPDATE users SET email_verified=TRUE, email_code_hash=NULL WHERE id=$1`, u.ID)
	a.Store.Audit(r.Context(), UserFrom(r.Context()).Username, "verify_email", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (a *Admin) revokeSessions(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	a.Store.RevokeAllSessions(r.Context(), u.ID)
	a.Store.Audit(r.Context(), UserFrom(r.Context()).Username, "revoke_sessions", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (a *Admin) deleteUser(w http.ResponseWriter, r *http.Request) {
	u, ok := a.target(w, r)
	if !ok {
		return
	}
	actor := UserFrom(r.Context())
	if u.ID == actor.ID {
		WriteError(w, 400, "cannot delete yourself")
		return
	}
	a.Store.DB.Exec(r.Context(), `DELETE FROM users WHERE id=$1`, u.ID)
	a.Store.Audit(r.Context(), actor.Username, "delete_user", u.Username, "")
	WriteJSON(w, 200, map[string]any{"ok": true})
}

func (a *Admin) audit(w http.ResponseWriter, r *http.Request) {
	size, offset := pageParam(r)
	rows, err := a.Store.DB.Query(r.Context(), `SELECT id, actor, action, target, detail, created FROM audit_log ORDER BY id DESC LIMIT $1 OFFSET $2`, size, offset)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type entry struct {
		ID      int       `json:"id"`
		Actor   string    `json:"actor"`
		Action  string    `json:"action"`
		Target  *string   `json:"target"`
		Detail  *string   `json:"detail"`
		Created time.Time `json:"created"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Target, &e.Detail, &e.Created)
		out = append(out, e)
	}
	WriteJSON(w, 200, map[string]any{"entries": out})
}

func (a *Admin) usage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days < 1 || days > 90 {
		days = 14
	}
	rows, err := a.Store.DB.Query(r.Context(), `SELECT day, model, sum(requests), sum(prompt_chars), count(DISTINCT user_id)
		FROM ai_usage WHERE day > CURRENT_DATE - $1::int GROUP BY day, model ORDER BY day, model`, days)
	if err != nil {
		WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		Day         time.Time `json:"day"`
		Model       string    `json:"model"`
		Requests    int       `json:"requests"`
		PromptChars int64     `json:"promptChars"`
		Users       int       `json:"users"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		rows.Scan(&x.Day, &x.Model, &x.Requests, &x.PromptChars, &x.Users)
		out = append(out, x)
	}
	WriteJSON(w, 200, map[string]any{"rows": out})
}
