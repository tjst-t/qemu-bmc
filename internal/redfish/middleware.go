package redfish

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func (s *Server) authEnabled() bool {
	return s.user != "" && s.pass != ""
}

// validCredentials accepts IPMI_USER/IPMI_PASS and, if a user store is set,
// any enabled BMC user (e.g. created in-band).
func (s *Server) validCredentials(user, pass string) bool {
	if subtle.ConstantTimeCompare([]byte(user), []byte(s.user)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.pass)) == 1 {
		return true
	}
	return s.users != nil && s.users.Authenticate(user, pass)
}

// authMiddleware accepts a session token (X-Auth-Token) or HTTP Basic auth.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if noAuthRequired(r) {
			next.ServeHTTP(w, r)
			return
		}
		if user, ok := s.sessions.authenticate(r.Header.Get("X-Auth-Token")); ok {
			next.ServeHTTP(w, withUser(r, user))
			return
		}
		if user, pass, ok := r.BasicAuth(); ok && s.validCredentials(user, pass) {
			next.ServeHTTP(w, withUser(r, user))
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Redfish"`)
		writeError(w, http.StatusUnauthorized, "NoValidSession",
			"Authentication required: use HTTP Basic auth or an X-Auth-Token from /redfish/v1/SessionService/Sessions")
	})
}

func (s *Server) trailingSlashMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") {
			r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		}
		next.ServeHTTP(w, r)
	})
}
