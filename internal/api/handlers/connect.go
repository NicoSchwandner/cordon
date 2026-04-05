package handlers

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/proxy"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/tlsca"
)

// ConnectHandler handles HTTP CONNECT requests as a MITM proxy. Instead of
// blindly tunneling TCP, it terminates TLS using a per-hostname certificate
// signed by Cordon's CA, reads the plaintext HTTP requests, and runs them
// through the full proxy pipeline (egress, classify, audit, secret swap)
// before executing the real upstream HTTPS request.
//
// This makes all HTTPS traffic from workspace containers fully transparent —
// git, curl, npm, apt all work without knowing about Cordon, while the proxy
// has full visibility to enforce policy and swap secrets.
type ConnectHandler struct {
	egress          *proxy.EgressChecker
	pipeline        *proxy.Pipeline
	ca              *tlsca.CA
	defaultTenantID uuid.UUID
	client          *http.Client
}

func NewConnectHandler(egress *proxy.EgressChecker, pipeline *proxy.Pipeline, ca *tlsca.CA, defaultTenantID uuid.UUID) *ConnectHandler {
	return &ConnectHandler{
		egress:          egress,
		pipeline:        pipeline,
		ca:              ca,
		defaultTenantID: defaultTenantID,
		client: &http.Client{
			Transport: &http.Transport{
				// Force HTTP/1.1 for upstream requests. The MITM connection back
				// to the client is HTTP/1.1, and http.Response.Write writes the
				// protocol version from the response — an HTTP/2 response would
				// produce "HTTP/2.0 200 OK" which is unparseable by HTTP/1.1 clients.
				ForceAttemptHTTP2: false,
				TLSNextProto:     make(map[string]func(string, *tls.Conn) http.RoundTripper),
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (h *ConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	hostname := stripPort(host)

	if !h.egress.IsAllowed(host) {
		log.Printf("[connect] egress denied: %s", host)
		http.Error(w, "egress denied: "+host+" not in allowlist", http.StatusForbidden)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		log.Printf("[connect] hijack failed: %v", err)
		return
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	cert, err := h.ca.CertForHost(hostname)
	if err != nil {
		log.Printf("[connect] cert generation failed for %s: %v", hostname, err)
		clientConn.Close()
		return
	}

	tlsConn := tls.Server(clientConn, &tls.Config{
		Certificates: []tls.Certificate{*cert},
	})
	if err := tlsConn.Handshake(); err != nil {
		log.Printf("[connect] TLS handshake failed for %s: %v", hostname, err)
		clientConn.Close()
		return
	}
	defer tlsConn.Close()

	log.Printf("[connect] MITM tunnel established: %s", host)

	reader := bufio.NewReader(tlsConn)
	for {
		req, err := http.ReadRequest(reader)
		if err != nil {
			if err != io.EOF {
				log.Printf("[connect] read request error for %s: %v", host, err)
			}
			return
		}

		if err := h.proxyRequest(tlsConn, req, hostname, host); err != nil {
			log.Printf("[connect] proxy error for %s: %v", host, err)
			return
		}
	}
}

// proxyRequest runs a single intercepted HTTP request through the pipeline
// and writes the upstream response back to the client's TLS connection.
func (h *ConnectHandler) proxyRequest(tlsConn *tls.Conn, req *http.Request, hostname, hostPort string) error {
	log.Printf("[connect] %s %s %s", req.Method, hostname, req.URL.RequestURI())

	// Handle Expect: 100-continue — git uses this for POST /git-upload-pack.
	// The client won't send the body until we acknowledge with 100 Continue.
	if strings.EqualFold(req.Header.Get("Expect"), "100-continue") {
		tlsConn.Write([]byte("HTTP/1.1 100 Continue\r\n\r\n"))
		req.Header.Del("Expect")
	}

	// Read the request body
	var body []byte
	if req.Body != nil && req.ContentLength != 0 {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return fmt.Errorf("reading request body: %w", err)
		}
	}

	// Build headers map
	headers := make(map[string]string, len(req.Header))
	for k := range req.Header {
		headers[k] = req.Header.Get(k)
	}

	// Build proxy request and run through the pipeline
	proxyReq := proxy.ProxyRequest{
		QueryType: proxy.QueryTypeHTTP,
		Method:    req.Method,
		Path:      req.URL.RequestURI(),
		Host:      hostname,
		Headers:   headers,
		Body:      body,
		TenantID:  h.defaultTenantID,
		Caller:    "workspace",
	}

	resp := h.pipeline.Process(req.Context(), proxyReq)

	if !resp.Allowed || resp.ModifiedReq == nil {
		statusCode := 403
		detail := "blocked by proxy"
		if resp.Problem != nil {
			statusCode = resp.Problem.Status
			detail = resp.Problem.Detail
		}
		log.Printf("[connect] blocked: %s %s → %d %s", req.Method, hostname+req.URL.RequestURI(), statusCode, detail)
		httpResp := &http.Response{
			StatusCode: statusCode,
			ProtoMajor: 1, ProtoMinor: 1,
			Header:        http.Header{"Content-Type": {"text/plain"}},
			Body:          io.NopCloser(strings.NewReader(detail)),
			ContentLength: int64(len(detail)),
		}
		return httpResp.Write(tlsConn)
	}

	// Execute the upstream HTTPS request with the (secret-swapped) request
	modified := resp.ModifiedReq
	upstreamURL := "https://" + hostname + modified.Path

	var bodyReader io.Reader
	if len(modified.Body) > 0 {
		bodyReader = bytes.NewReader(modified.Body)
	}
	upstreamReq, err := http.NewRequest(modified.Method, upstreamURL, bodyReader)
	if err != nil {
		writeErrorResponse(tlsConn, 502, "failed to build upstream request")
		return fmt.Errorf("building upstream request: %w", err)
	}
	for k, v := range modified.Headers {
		upstreamReq.Header.Set(k, v)
	}
	upstreamReq.Host = hostname

	upstreamResp, err := h.client.Do(upstreamReq)
	if err != nil {
		log.Printf("[connect] upstream error: %s %s: %v", modified.Method, hostname+modified.Path, err)
		writeErrorResponse(tlsConn, 502, "upstream request failed")
		return fmt.Errorf("upstream request: %w", err)
	}
	defer upstreamResp.Body.Close()

	// Ensure HTTP/1.1 framing — the client-side TLS connection is HTTP/1.1
	upstreamResp.ProtoMajor = 1
	upstreamResp.ProtoMinor = 1

	log.Printf("[connect] %s %s → %d", req.Method, hostname+req.URL.RequestURI(), upstreamResp.StatusCode)

	return upstreamResp.Write(tlsConn)
}

func writeErrorResponse(w io.Writer, status int, body string) {
	resp := &http.Response{
		StatusCode: status,
		ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"text/plain"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Write(w)
}

func stripPort(host string) string {
	if i := strings.LastIndex(host, ":"); i != -1 {
		return host[:i]
	}
	return host
}
