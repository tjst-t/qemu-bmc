package fault

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

// matchHTTP reports whether a redfish-* fault applies to request r.
func (f *Fault) matchHTTP(r *http.Request) bool {
	if f.Method != "" && f.Method != r.Method {
		return false
	}
	return matchPath(f.Path, r.URL.Path)
}

// matchPath matches a request path against a glob (if pattern has *, ? or
// [) or a prefix. An empty pattern matches every path.
func matchPath(pattern, p string) bool {
	if pattern == "" {
		return true
	}
	if strings.ContainsAny(pattern, "*?[") {
		if p != "/" {
			p = strings.TrimSuffix(p, "/")
		}
		ok, _ := path.Match(pattern, p)
		return ok
	}
	return strings.HasPrefix(p, pattern)
}

// Middleware applies the redfish-* and bmc-unavailable faults to the
// requests of the Redfish server. A nil store applies none.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e := s.take(func(f *Fault) bool {
			if f.Kind == BMCUnavailable {
				return true
			}
			return f.Target == "redfish" && f.matchHTTP(r)
		}, "Redfish "+r.Method+" "+r.URL.Path)
		if e == nil {
			next.ServeHTTP(w, r)
			return
		}
		switch e.Kind {
		case RedfishDelay:
			e.wait(time.Duration(e.DelayMs)*time.Millisecond, r.Context().Done())
			next.ServeHTTP(w, r)
		case RedfishError:
			writeRedfishError(w, e.Status)
		case RedfishHang:
			e.wait(time.Duration(e.HangSeconds)*time.Second, r.Context().Done())
			dropConnection(w)
		case RedfishDrop:
			dropConnection(w)
		case BMCUnavailable:
			// No response until the fault ends (or the client gives up)
			e.wait(0, r.Context().Done())
			dropConnection(w)
		}
	})
}

// writeRedfishError responds with status and a Redfish error body.
func writeRedfishError(w http.ResponseWriter, status int) {
	msg := fmt.Sprintf("%d %s (injected fault)", status, http.StatusText(status))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    "Base.1.0.GeneralError",
			"message": msg,
			"@Message.ExtendedInfo": []map[string]any{{
				"@odata.type": "#Message.v1_1_1.Message",
				"MessageId":   "Base.1.0.GeneralError",
				"Message":     msg,
				"Severity":    "Critical",
				"Resolution":  "None",
			}},
		},
	})
}

// dropConnection closes the connection of the request without a response:
// a TCP reset for HTTP/1.x, a stream reset for HTTP/2 (which cannot be
// hijacked).
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			if nc, ok := conn.(interface{ NetConn() net.Conn }); ok {
				// The TLS connection: reset the TCP connection under it
				// instead of sending a close_notify alert
				conn = nc.NetConn()
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				tcp.SetLinger(0)
			}
			conn.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}
