package handlers

import (
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/NicoSchwandner/cordon/internal/application/proxy"
)

// ConnectHandler handles HTTP CONNECT requests, tunneling TCP connections to
// allowed hosts. This lets workspace containers use standard proxy env vars
// (http_proxy/https_proxy) so that git, curl, apt, and other tools route
// through the Cordon proxy — the same egress path used for all other traffic.
type ConnectHandler struct {
	egress      *proxy.EgressChecker
	dialTimeout time.Duration
}

func NewConnectHandler(egress *proxy.EgressChecker) *ConnectHandler {
	return &ConnectHandler{
		egress:      egress,
		dialTimeout: 10 * time.Second,
	}
}

func (h *ConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host

	if !h.egress.IsAllowed(host) {
		log.Printf("[connect] egress denied: %s", host)
		http.Error(w, "egress denied: "+host+" not in allowlist", http.StatusForbidden)
		return
	}

	// Dial the target host
	target, err := net.DialTimeout("tcp", host, h.dialTimeout)
	if err != nil {
		log.Printf("[connect] dial failed: %s: %v", host, err)
		http.Error(w, "connection failed", http.StatusBadGateway)
		return
	}

	// Hijack the client connection to get raw TCP access
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		target.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	client, _, err := hijacker.Hijack()
	if err != nil {
		target.Close()
		log.Printf("[connect] hijack failed: %v", err)
		return
	}

	// Send 200 to signal the tunnel is established
	client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	log.Printf("[connect] tunnel established: %s", host)

	// Bidirectional copy — when either side closes, the other follows
	go func() {
		io.Copy(target, client)
		target.Close()
	}()
	io.Copy(client, target)
	client.Close()
}
