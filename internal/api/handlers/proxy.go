package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/NicoSchwandner/cordon/internal/api/middleware"

	"github.com/NicoSchwandner/cordon/internal/application/proxy"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ProxyHandlerConfig holds configurable values for the proxy handler.
type ProxyHandlerConfig struct {
	HTTPTimeout     time.Duration
	MaxResponseBody int64
}

// ProxyHandler handles SQL and HTTP proxy requests.
type ProxyHandler struct {
	pipeline        *proxy.Pipeline
	client          *http.Client
	maxResponseBody int64
}

func NewProxyHandler(pipeline *proxy.Pipeline, cfg ProxyHandlerConfig) *ProxyHandler {
	return &ProxyHandler{
		pipeline: pipeline,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse // don't follow redirects
			},
		},
		maxResponseBody: cfg.MaxResponseBody,
	}
}

// SQLRequest is the JSON body for POST /api/proxy/sql
type SQLRequest struct {
	Query  string `json:"query"`
	Target string `json:"target"`
	Caller string `json:"caller"`
}

// ProxyResult is the JSON response for proxy operations.
type ProxyResult struct {
	Allowed         bool              `json:"allowed"`
	Tier            int               `json:"tier"`
	Decision        string            `json:"decision"`
	Error           string            `json:"error,omitempty"`
	UpstreamStatus  int               `json:"upstream_status,omitempty"`
	UpstreamHeaders map[string]string `json:"upstream_headers,omitempty"`
	UpstreamBody    string            `json:"upstream_body,omitempty"`
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
		QueryType: proxy.QueryTypeHTTP,
		Method:    req.Method,
		Path:      req.URL,
		Host:      req.Host,
		Headers:   req.Headers,
		Body:      []byte(req.Body),
		TenantID:  tenantID,
		Caller:    caller,
	}

	resp := h.pipeline.Process(r.Context(), proxyReq)

	w.Header().Set("Content-Type", "application/json")
	if resp.Problem != nil {
		middleware.WriteProblem(w, *resp.Problem)
		return
	}

	if !resp.Allowed || resp.ModifiedReq == nil {
		json.NewEncoder(w).Encode(ProxyResult{
			Allowed:  resp.Allowed,
			Tier:     int(resp.Tier),
			Decision: string(resp.Decision),
		})
		return
	}

	// Execute the outbound HTTP call with secret-swapped request
	upstreamResult, err := h.executeUpstream(r.Context(), resp.ModifiedReq)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type:   "https://cordon.dev/problems/upstream-failed",
			Title:  "Upstream Request Failed",
			Status: 502,
			Detail: err.Error(),
			Code:   "upstream_failed",
		})
		return
	}

	json.NewEncoder(w).Encode(ProxyResult{
		Allowed:         true,
		Tier:            int(resp.Tier),
		Decision:        string(resp.Decision),
		UpstreamStatus:  upstreamResult.Status,
		UpstreamHeaders: upstreamResult.Headers,
		UpstreamBody:    upstreamResult.Body,
	})
}

// upstreamResponse holds the result of an outbound HTTP call.
type upstreamResponse struct {
	Status  int
	Headers map[string]string
	Body    string
}

// executeUpstream builds and executes an HTTP request from the secret-swapped proxy request.
func (h *ProxyHandler) executeUpstream(ctx context.Context, modified *proxy.ProxyRequest) (*upstreamResponse, error) {
	url := modified.Path
	if len(url) == 0 || url[0] == '/' {
		url = "https://" + modified.Host + url
	}

	httpReq, err := http.NewRequestWithContext(ctx, modified.Method, url, bytes.NewReader(modified.Body))
	if err != nil {
		return nil, err
	}

	for k, v := range modified.Headers {
		httpReq.Header.Set(k, v)
	}
	if httpReq.Header.Get("Host") == "" {
		httpReq.Host = modified.Host
	}

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, h.maxResponseBody))
	if err != nil {
		return nil, err
	}

	headers := make(map[string]string)
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}

	return &upstreamResponse{
		Status:  resp.StatusCode,
		Headers: headers,
		Body:    string(body),
	}, nil
}
