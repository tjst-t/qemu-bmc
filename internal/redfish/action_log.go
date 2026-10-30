package redfish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/audit"
)

type actionKey struct{}
type userKey struct{}

// withUser returns r with the authenticated user name.
func withUser(r *http.Request, user string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userKey{}, user))
}

// requestUser returns the authenticated user of r ("" if none).
func requestUser(r *http.Request) string {
	user, _ := r.Context().Value(userKey{}).(string)
	return user
}

// action returns the action line of the request (nil, which ignores every
// call, if the handler is not wrapped by logAction).
func action(r *http.Request) *audit.Entry {
	e, _ := r.Context().Value(actionKey{}).(*audit.Entry)
	return e
}

// logAction wraps the handler of a write command so that every request
// writes one action line. The handler adds the fields of the command with
// action(r).Set; unless it sets the result itself (action(r).Result), the
// result follows the response status: 2xx accepted, 4xx rejected, 5xx
// failed, with status= and reason= (the Redfish message id or error code)
// for errors.
func (s *Server) logAction(kind string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e := audit.New(kind, audit.ViaRedfish, r.RemoteAddr).SetUser(requestUser(r))
		rec := &actionRecorder{ResponseWriter: w}
		defer func() {
			if !e.HasResult() {
				status := rec.status
				if status == 0 {
					status = http.StatusOK
				}
				switch {
				case status < 400:
					e.Result(audit.Accepted)
				case status < 500:
					e.Result(audit.Rejected, "status", strconv.Itoa(status), "reason", rec.reason())
				default:
					e.Result(audit.Failed, "status", strconv.Itoa(status), "reason", rec.reason())
				}
			}
			e.Log()
		}()
		h(rec, r.WithContext(context.WithValue(r.Context(), actionKey{}, e)))
	}
}

// actionRoutePrefix starts the name of the routes of the write commands
// (followed by the kind), see LogFaultedAction.
const actionRoutePrefix = "action:"

// LogFaultedAction writes the action line of a request that an injected
// fault failed or dropped before it reached the server (redfish-error,
// redfish-drop, redfish-hang, bmc-unavailable), if it is a write command.
// The user is not known (the request was not authenticated); a power
// action shows its ResetType if the body has one.
func (s *Server) LogFaultedAction(r *http.Request, faultID, faultKind string) {
	var match mux.RouteMatch
	if !s.router.Match(r, &match) || match.Route == nil {
		return
	}
	kind, ok := strings.CutPrefix(match.Route.GetName(), actionRoutePrefix)
	if !ok {
		return
	}
	e := audit.New(kind, audit.ViaRedfish, r.RemoteAddr)
	if vmid, ok := match.Vars["vmid"]; ok {
		e.Set("device", vmid)
	}
	if kind == audit.KindPower && r.Body != nil {
		var req ResetRequest
		if json.NewDecoder(io.LimitReader(r.Body, maxErrorBody)).Decode(&req) == nil {
			e.Set("reset_type", req.ResetType)
		}
	}
	e.Result(audit.Fault, "fault", faultID, "reason", faultKind).Log()
}

// maxErrorBody is how much of an error response actionRecorder keeps.
const maxErrorBody = 16 << 10

// actionRecorder records the status of a response and the start of its
// body if it is an error.
type actionRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (a *actionRecorder) WriteHeader(status int) {
	if a.status == 0 {
		a.status = status
	}
	a.ResponseWriter.WriteHeader(status)
}

func (a *actionRecorder) Write(p []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	if a.status >= 400 && a.body.Len() < maxErrorBody {
		n := min(len(p), maxErrorBody-a.body.Len())
		a.body.Write(p[:n])
	}
	return a.ResponseWriter.Write(p)
}

func (a *actionRecorder) Flush() {
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (a *actionRecorder) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// reason returns the first message id of a Redfish error response, or its
// error code ("" if the body is no Redfish error).
func (a *actionRecorder) reason() string {
	var body struct {
		Error struct {
			Code string `json:"code"`
			Info []struct {
				MessageID string `json:"MessageId"`
			} `json:"@Message.ExtendedInfo"`
		} `json:"error"`
	}
	if json.Unmarshal(a.body.Bytes(), &body) != nil {
		return ""
	}
	for _, m := range body.Error.Info {
		if m.MessageID != "" {
			return m.MessageID
		}
	}
	return body.Error.Code
}
