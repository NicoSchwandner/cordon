package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nicobistolfi/cordon/internal/api/handlers"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	apiws "github.com/nicobistolfi/cordon/internal/api/ws"
	"github.com/nicobistolfi/cordon/internal/application/audit"
	"github.com/nicobistolfi/cordon/internal/application/progress"
	"github.com/nicobistolfi/cordon/internal/application/proxy"
	"github.com/nicobistolfi/cordon/internal/infrastructure/devcontainer"
	"github.com/nicobistolfi/cordon/internal/infrastructure/docker"
	gh "github.com/nicobistolfi/cordon/internal/infrastructure/github"
	"github.com/nicobistolfi/cordon/internal/infrastructure/postgres"
	"github.com/nicobistolfi/cordon/internal/infrastructure/sops"
	ws "github.com/nicobistolfi/cordon/internal/infrastructure/websocket"
)

func main() {
	port := envOr("PORT", "8443")
	dbURL := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/cordon?sslmode=disable")
	authMode := envOr("AUTH_MODE", "static")
	defaultTenant := envOr("DEFAULT_TENANT_ID", "00000000-0000-0000-0000-000000000001")

	ctx := context.Background()

	// Database
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Printf("WARNING: database not reachable: %v", err)
	}

	// Infrastructure
	auditStore := postgres.NewAuditStore(pool)
	approvalStore := ws.NewApprovalStore()
	vault := sops.NewMemoryVault()

	tenantID := uuid.MustParse(defaultTenant)
	vault.AddSecret(tenantID, "DATABASE_URL", "cordon-placeholder-database-url", envOr("REAL_DATABASE_URL", "postgresql://real:secret@db:5432/prod"))
	vault.AddSecret(tenantID, "API_KEY", "cordon-placeholder-api-key", envOr("REAL_API_KEY", "sk-real-key-12345"))

	// Devcontainer builder (optional — needs `devcontainer` CLI on PATH)
	githubToken := os.Getenv("GITHUB_TOKEN")
	var dcBuilder *devcontainer.Builder
	dcBuilder, err = devcontainer.NewBuilder()
	if err != nil {
		log.Printf("WARNING: devcontainer CLI not available, repo-based workspaces disabled: %v", err)
	}
	if githubToken != "" {
		log.Printf("GITHUB_TOKEN configured — private repo cloning enabled")
		vault.AddSecret(tenantID, "GITHUB_TOKEN", "cordon-placeholder-github-token", githubToken)
	}

	// GitHub API client (shared across provider + handlers)
	ghClient := gh.NewClient(githubToken)

	// Progress tracking
	timingStore := progress.NewTimingStore("data/build-timings.json")
	progressStore := progress.NewStore(timingStore)

	// Workspace orchestrator (Docker)
	var workspaceProvider *docker.Provider
	workspaceProvider, err = docker.NewProvider(dcBuilder, githubToken, ghClient)
	if err != nil {
		log.Printf("WARNING: Docker not available, workspace features disabled: %v", err)
	}
	if workspaceProvider != nil {
		workspaceProvider.SetProgressStore(progressStore)
	}

	// Application services
	auditService := audit.NewService(auditStore)
	swapper := proxy.NewSecretSwapper(vault)
	egress := proxy.NewEgressChecker([]string{
		"github.com", "*.github.com",
		"api.anthropic.com",
		"registry.npmjs.org",
		"nuget.org", "*.nuget.org",
	})

	pipeline := proxy.NewPipeline(proxy.PipelineConfig{
		SQLClassifier:   proxy.NewSQLClassifier(),
		HTTPClassifier:  proxy.NewHTTPClassifier(),
		Swapper:         swapper,
		Audit:           auditStore,
		Approver:        approvalStore,
		Egress:          egress,
		ApprovalTimeout: 5 * time.Minute,
	})

	// Auth
	var authValidator middleware.AuthValidator
	switch authMode {
	case "token":
		authValidator = &middleware.TokenAuth{
			Tokens: map[string]middleware.TokenInfo{
				envOr("API_TOKEN", "dev-token"): {TenantID: tenantID, UserID: "developer"},
			},
		}
	default:
		authValidator = &middleware.StaticAuth{TenantID: tenantID, UserID: "developer"}
	}

	// Handlers
	healthHandler := handlers.NewHealthHandler(pool)
	proxyHandler := handlers.NewProxyHandler(pipeline)
	auditHandler := handlers.NewAuditHandler(auditService)
	approvalHandler := handlers.NewApprovalHandler(approvalStore)
	workspaceHandler := handlers.NewWorkspaceHandler(workspaceProvider, progressStore)
	secretHandler := handlers.NewSecretHandler(vault)

	githubHandler := handlers.NewGitHubHandler(ghClient)

	// Router
	mux := http.NewServeMux()
	authMW := middleware.Auth(authValidator)

	// Public routes
	mux.HandleFunc("GET /health", healthHandler.Health)
	mux.HandleFunc("GET /ready", healthHandler.Ready)

	// Proxy endpoints
	mux.Handle("POST /api/proxy/sql", authMW(http.HandlerFunc(proxyHandler.SQL)))
	mux.Handle("POST /api/proxy/http", authMW(http.HandlerFunc(proxyHandler.HTTP)))

	// Audit
	mux.Handle("GET /api/audit", authMW(http.HandlerFunc(auditHandler.Query)))

	// Approvals
	mux.Handle("POST /api/approvals/", authMW(http.HandlerFunc(approvalHandler.Decide)))

	// Secrets
	mux.Handle("GET /api/secrets", authMW(http.HandlerFunc(secretHandler.List)))
	mux.Handle("POST /api/secrets", authMW(http.HandlerFunc(secretHandler.Set)))
	mux.Handle("PUT /api/secrets", authMW(http.HandlerFunc(secretHandler.Set)))
	mux.Handle("GET /api/secrets/", authMW(http.HandlerFunc(secretHandler.Reveal)))
	mux.Handle("DELETE /api/secrets/", authMW(http.HandlerFunc(secretHandler.Delete)))

	// GitHub API proxy (works without Docker — only needs GitHub token)
	mux.Handle("GET /api/github/default-branch", authMW(http.HandlerFunc(githubHandler.DefaultBranch)))
	mux.Handle("GET /api/github/orgs/{org}/repos", authMW(http.HandlerFunc(githubHandler.OrgRepos)))
	mux.Handle("GET /api/github/repos/{owner}/{repo}/branches", authMW(http.HandlerFunc(githubHandler.RepoBranches)))

	// Workspaces
	mux.Handle("POST /api/workspaces", authMW(http.HandlerFunc(workspaceHandler.Create)))
	mux.Handle("GET /api/workspaces", authMW(http.HandlerFunc(workspaceHandler.List)))
	mux.Handle("GET /api/workspaces/{id}/logs", authMW(http.HandlerFunc(workspaceHandler.CreationLogs)))
	mux.Handle("GET /api/workspaces/", authMW(http.HandlerFunc(workspaceHandler.Get)))
	mux.Handle("DELETE /api/workspaces/", authMW(http.HandlerFunc(workspaceHandler.Delete)))
	mux.Handle("POST /api/workspaces/{id}/activate", authMW(http.HandlerFunc(workspaceHandler.Activate)))
	mux.Handle("POST /api/workspaces/{id}/exec", authMW(http.HandlerFunc(workspaceHandler.ExecInWorkspace)))
	mux.Handle("POST /api/workspaces/{id}/{action}", authMW(http.HandlerFunc(workspaceHandler.Action)))

	// WebSocket endpoints
	agentRegistry := apiws.NewAgentRegistry()
	if workspaceProvider != nil {
		terminalHandler, err := apiws.NewTerminalHandler(workspaceProvider)
		if err != nil {
			log.Printf("WARNING: terminal handler not available: %v", err)
		} else {
			mux.Handle("/ws/terminal/", authMW(terminalHandler))
		}
	}
	mux.Handle("/ws/agent/", authMW(apiws.NewAgentWSHandler(agentRegistry)))
	mux.Handle("/ws/approvals", authMW(apiws.NewApprovalWSHandler()))
	mux.Handle("/ws/audit", authMW(apiws.NewAuditWSHandler()))

	handler := middleware.Logger(middleware.Recovery(mux))

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Disable for WebSocket
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutCtx)
	}()

	log.Printf("Cordon server starting on :%s (auth=%s, docker=%v)", port, authMode, workspaceProvider != nil)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
