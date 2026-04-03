package handlers

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"github.com/nicobistolfi/cordon/internal/application/proxy"
)

// ProxyHandler handles SQL and HTTP proxy requests.
type ProxyHandler struct {
	pipeline *proxy.Pipeline
}

func NewProxyHandler(pipeline *proxy.Pipeline) *ProxyHandler {
	return &ProxyHandler{pipeline: pipeline}
}

// SQLRequest is the JSON body for POST /api/proxy/sql
type SQLRequest struct {
	Query  string `json:"query"`
	Target string `json:"target"`
	Caller string `json:"caller"`
}

// ProxyResult is the JSON response for proxy operations.
type ProxyResult struct {
	Allowed  bool   `json:"allowed"`
	Tier     int    `json:"tier"`
	Decision string `json:"decision"`
	Error    string `json:"error,omitempty"`
}

func (h *ProxyHandler) SQL(w http.ResponseWriter, r *http.Request) {
	var req SQLRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	caller := req.Caller
	if caller == "" {
		caller = middleware.UserIDFromContext(r.Context())
	}

	proxyReq := proxy.ProxyRequest{
		QueryType:   proxy.QueryTypeSQL,
		SQLQuery:    req.Query,
		Body:        body,
		TenantID:    tenantID,
		Caller:      caller,
		Host:        req.Target,
	}

	resp := h.pipeline.Process(r.Context(), proxyReq)

	w.Header().Set("Content-Type", "application/json")
	if resp.Problem != nil {
		middleware.WriteProblem(w, *resp.Problem)
		return
	}

	json.NewEncoder(w).Encode(ProxyResult{
		Allowed:  resp.Allowed,
		Tier:     int(resp.Tier),
		Decision: string(resp.Decision),
	})
}

// HTTPProxyRequest is the JSON body for POST /api/proxy/http
type HTTPProxyRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Caller  string            `json:"caller"`
}

func (h *ProxyHandler) HTTP(w http.ResponseWriter, r *http.Request) {
	var req HTTPProxyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	caller := req.Caller
	if caller == "" {
		caller = middleware.UserIDFromContext(r.Context())
	}

	proxyReq := proxy.ProxyRequest{
		QueryType:   proxy.QueryTypeHTTP,
		Method:      req.Method,
		Path:        req.URL,
		Host:        req.Host,
		Headers:     req.Headers,
		Body:        []byte(req.Body),
		TenantID:    tenantID,
		Caller:      caller,
	}

	resp := h.pipeline.Process(r.Context(), proxyReq)

	w.Header().Set("Content-Type", "application/json")
	if resp.Problem != nil {
		middleware.WriteProblem(w, *resp.Problem)
		return
	}

	json.NewEncoder(w).Encode(ProxyResult{
		Allowed:  resp.Allowed,
		Tier:     int(resp.Tier),
		Decision: string(resp.Decision),
	})
}
