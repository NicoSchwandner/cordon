package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/NicoSchwandner/cordon/internal/domain"
)

// Recovery catches panics and returns RFC 7807 ProblemDetails.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "error", rec, "method", r.Method, "path", r.URL.Path)
				WriteProblem(w, domain.ProblemDetails{
					Type:   "https://cordon.dev/problems/internal-error",
					Title:  "Internal Server Error",
					Status: 500,
					Detail: "An internal error occurred",
					Code:   "internal_error",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// WriteProblem writes an RFC 7807 ProblemDetails response.
func WriteProblem(w http.ResponseWriter, problem domain.ProblemDetails) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	json.NewEncoder(w).Encode(problem)
}
