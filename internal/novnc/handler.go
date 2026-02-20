package novnc

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"

	"github.com/gorilla/websocket"
)

//go:embed static
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	// Basic Auth handles security; allow all origins
	CheckOrigin:     func(r *http.Request) bool { return true },
	Subprotocols:    []string{"binary"},
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
}

// Handler serves noVNC static files and proxies WebSocket connections to a VNC server.
type Handler struct {
	vncAddr string
}

// NewHandler creates a new Handler that proxies to the given VNC TCP address.
func NewHandler(vncAddr string) *Handler {
	return &Handler{vncAddr: vncAddr}
}

// ServeFiles returns an http.Handler that serves the embedded noVNC static files.
// Mount it at /novnc/ so that the URL prefix matches file paths in the embed.
func (h *Handler) ServeFiles() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// Should never happen with a correct embed path
		panic("novnc: failed to create sub-filesystem: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}

// ServeWebSocket upgrades the HTTP connection to a WebSocket and proxies data
// bidirectionally to the VNC TCP server at h.vncAddr.
func (h *Handler) ServeWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("novnc: WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	vnc, err := net.Dial("tcp", h.vncAddr)
	if err != nil {
		log.Printf("novnc: failed to connect to VNC at %s: %v", h.vncAddr, err)
		conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "VNC unavailable"))
		return
	}
	defer vnc.Close()

	done := make(chan struct{}, 2)

	// VNC → WebSocket
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := vnc.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WebSocket → VNC
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if _, werr := vnc.Write(msg); werr != nil {
				return
			}
		}
	}()

	<-done
}
