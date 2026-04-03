package proxy

import (
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// QueryType indicates whether the proxied operation is SQL or HTTP.
type QueryType string

const (
	QueryTypeSQL  QueryType = "sql"
	QueryTypeHTTP QueryType = "http"
)

// ProxyRequest represents an operation to be classified and proxied.
type ProxyRequest struct {
	Method      string
	Path        string
	Host        string
	Headers     map[string]string
	Body        []byte
	QueryType   QueryType
	SQLQuery    string
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	Caller      string
}

// ProxyResponse is the result of processing a proxy request.
type ProxyResponse struct {
	Allowed     bool
	Tier        domain.Tier
	Decision    domain.Decision
	Problem     *domain.ProblemDetails
	ModifiedReq *ProxyRequest // after secret swap, if allowed
}
