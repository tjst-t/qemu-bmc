package redfish

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
)

const (
	sessionsPath = "/redfish/v1/SessionService/Sessions"

	// DefaultSessionTimeout is how long a session lives without being used.
	DefaultSessionTimeout = 30 * time.Minute
	minSessionTimeout     = 30 * time.Second
	maxSessionTimeout     = 24 * time.Hour

	// maxSessions bounds the number of live sessions; creating one more
	// fails with 503 until one is deleted or expires.
	maxSessions = 64
)

var errTooManySessions = errors.New("too many sessions")

type session struct {
	id       string
	token    string
	userName string
	lastUsed time.Time
}

// sessionStore holds the Redfish sessions (X-Auth-Token) in memory. Sessions
// expire after the timeout without use and are lost when qemu-bmc restarts.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]*session // by id
	timeout  time.Duration
	nextID   int
	now      func() time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{
		sessions: make(map[string]*session),
		timeout:  DefaultSessionTimeout,
		now:      time.Now,
	}
}

// expireLocked removes sessions not used within the timeout.
func (st *sessionStore) expireLocked() {
	now := st.now()
	for id, s := range st.sessions {
		if now.Sub(s.lastUsed) > st.timeout {
			delete(st.sessions, id)
		}
	}
}

func (st *sessionStore) create(userName string) (*session, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked()
	if len(st.sessions) >= maxSessions {
		return nil, errTooManySessions
	}
	st.nextID++
	s := &session{
		id:       strconv.Itoa(st.nextID),
		token:    hex.EncodeToString(buf),
		userName: userName,
		lastUsed: st.now(),
	}
	st.sessions[s.id] = s
	return s, nil
}

// authenticate reports whether token belongs to a live session and, if so,
// marks the session as used.
func (st *sessionStore) authenticate(token string) bool {
	if token == "" {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked()
	for _, s := range st.sessions {
		if subtle.ConstantTimeCompare([]byte(s.token), []byte(token)) == 1 {
			s.lastUsed = st.now()
			return true
		}
	}
	return false
}

func (st *sessionStore) get(id string) (session, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked()
	s, ok := st.sessions[id]
	if !ok {
		return session{}, false
	}
	return *s, true
}

func (st *sessionStore) delete(id string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked()
	if _, ok := st.sessions[id]; !ok {
		return false
	}
	delete(st.sessions, id)
	return true
}

// ids returns the live session ids in creation order.
func (st *sessionStore) ids() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.expireLocked()
	ids := make([]string, 0, len(st.sessions))
	for id := range st.sessions {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.Atoi(ids[i])
		b, _ := strconv.Atoi(ids[j])
		return a < b
	})
	return ids
}

func (st *sessionStore) getTimeout() time.Duration {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.timeout
}

func (st *sessionStore) setTimeout(d time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.timeout = d
}

// SessionService is the Redfish SessionService resource
type SessionService struct {
	ODataType      string  `json:"@odata.type"`
	ODataID        string  `json:"@odata.id"`
	ID             string  `json:"Id"`
	Name           string  `json:"Name"`
	ServiceEnabled bool    `json:"ServiceEnabled"`
	SessionTimeout int     `json:"SessionTimeout"`
	Sessions       ODataID `json:"Sessions"`
}

// Session is a Redfish Session resource. The password is never returned.
type Session struct {
	ODataType string `json:"@odata.type"`
	ODataID   string `json:"@odata.id"`
	ID        string `json:"Id"`
	Name      string `json:"Name"`
	UserName  string `json:"UserName"`
}

func sessionResource(s session) Session {
	return Session{
		ODataType: "#Session.v1_0_0.Session",
		ODataID:   sessionsPath + "/" + s.id,
		ID:        s.id,
		Name:      "User Session",
		UserName:  s.userName,
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) handleGetSessionService(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, SessionService{
		ODataType:      "#SessionService.v1_0_0.SessionService",
		ODataID:        "/redfish/v1/SessionService",
		ID:             "SessionService",
		Name:           "Session Service",
		ServiceEnabled: true,
		SessionTimeout: int(s.sessions.getTimeout() / time.Second),
		Sessions:       ODataID{ODataID: sessionsPath},
	})
}

func (s *Server) handlePatchSessionService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionTimeout *int `json:"SessionTimeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}
	if req.SessionTimeout == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "SessionTimeout is required")
		return
	}
	d := time.Duration(*req.SessionTimeout) * time.Second
	if d < minSessionTimeout || d > maxSessionTimeout {
		writeError(w, http.StatusBadRequest, "PropertyValueNotInList",
			"SessionTimeout must be between 30 and 86400 seconds")
		return
	}
	s.sessions.setTimeout(d)
	s.handleGetSessionService(w, r)
}

func (s *Server) handleSessionCollection(w http.ResponseWriter, r *http.Request) {
	ids := s.sessions.ids()
	members := make([]ODataID, 0, len(ids))
	for _, id := range ids {
		members = append(members, ODataID{ODataID: sessionsPath + "/" + id})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"@odata.type":         "#SessionCollection.SessionCollection",
		"@odata.id":           sessionsPath,
		"Name":                "Session Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	})
}

// handleCreateSession logs in: it checks UserName/Password and returns the
// session token in the X-Auth-Token header and its URI in Location.
func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserName *string `json:"UserName"`
		Password *string `json:"Password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}
	if req.UserName == nil || req.Password == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "UserName and Password are required")
		return
	}
	if s.authEnabled() && !s.validCredentials(*req.UserName, *req.Password) {
		writeError(w, http.StatusUnauthorized, "InvalidCredentials", "Invalid UserName or Password")
		return
	}
	sess, err := s.sessions.create(*req.UserName)
	if errors.Is(err, errTooManySessions) {
		writeError(w, http.StatusServiceUnavailable, "SessionLimitExceeded",
			"Too many sessions; delete unused sessions or wait for them to expire")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError", err.Error())
		return
	}
	res := sessionResource(*sess)
	w.Header().Set("X-Auth-Token", sess.token)
	w.Header().Set("Location", res.ODataID)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessions.get(mux.Vars(r)["sid"])
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Session not found")
		return
	}
	writeJSON(w, http.StatusOK, sessionResource(sess))
}

// handleDeleteSession logs out.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.sessions.delete(mux.Vars(r)["sid"]) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Session not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// noAuthRequired reports whether a request may be served without
// credentials: the version and service root documents (so clients can find
// the session service) and session creation (login).
func noAuthRequired(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodGet && (path == "/redfish" || path == "/redfish/v1"):
		return true
	case r.Method == http.MethodPost && path == sessionsPath:
		return true
	}
	return false
}
