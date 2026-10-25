package fault

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

// APIHandler returns the HTTP handler of the fault API:
//
//	GET    /faults       list the active faults
//	POST   /faults       add a fault (201, the fault with its id)
//	DELETE /faults/{id}  delete a fault (204, 404 if unknown)
//	DELETE /faults       delete all faults (204)
//
// If token is not empty, requests need "Authorization: Bearer <token>".
func (s *Store) APIHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="qemu-bmc faults"`)
				writeAPIError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		p := strings.TrimSuffix(r.URL.Path, "/")
		switch {
		case p == "/faults":
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, http.StatusOK, s.List())
			case http.MethodPost:
				var f Fault
				dec := json.NewDecoder(r.Body)
				dec.DisallowUnknownFields()
				if err := dec.Decode(&f); err != nil {
					writeAPIError(w, http.StatusBadRequest, "invalid fault: "+err.Error())
					return
				}
				added, err := s.Add(f)
				if err != nil {
					writeAPIError(w, http.StatusBadRequest, err.Error())
					return
				}
				w.Header().Set("Location", "/faults/"+added.ID)
				writeJSON(w, http.StatusCreated, added)
			case http.MethodDelete:
				s.Clear()
				w.WriteHeader(http.StatusNoContent)
			default:
				w.Header().Set("Allow", "GET, POST, DELETE")
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
			}
		case strings.HasPrefix(p, "/faults/") && !strings.Contains(p[len("/faults/"):], "/"):
			if r.Method != http.MethodDelete {
				w.Header().Set("Allow", "DELETE")
				writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			if !s.Delete(p[len("/faults/"):]) {
				writeAPIError(w, http.StatusNotFound, "no such fault")
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeAPIError(w, http.StatusNotFound, "not found")
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
