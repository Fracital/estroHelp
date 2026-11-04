package provider

import (
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/relay"
)

// DumpFailure records a cliproxy upstream failure with the calling user attached.
func DumpFailure(r *http.Request, dialect Dialect, upstreamURL string, reqBody []byte, status int, respHeaders http.Header, respBody []byte, transportErr error) {
	var user string
	var id int
	if u := auth.UserFrom(r.Context()); u != nil {
		user, id = u.Username, u.ID
	}
	relay.DumpFailure(r, user, id, "cliproxy", string(dialect), upstreamURL, reqBody, status, respHeaders, respBody, transportErr)
}
