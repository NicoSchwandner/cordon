package domain

// ProblemDetails follows RFC 7807 for error responses.
type ProblemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
}
