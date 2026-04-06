package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NicoSchwandner/cordon/internal/api/handlers"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	apiws "github.com/NicoSchwandner/cordon/internal/api/ws"
	auditpkg "github.com/NicoSchwandner/cordon/internal/application/audit"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/application/progress"
	"github.com/NicoSchwandner/cordon/internal/application/proxy"
	"github.com/NicoSchwandner/cordon/internal/application/workspace"
	"github.com/NicoSchwandner/cordon/internal/domain"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/devcontainer"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/docker"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/proxyproto"
	gh "github.com/NicoSchwandner/cordon/internal/infrastructure/github"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/postgres"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/sops"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/tlsca"
	ws "github.com/NicoSchwandner/cordon/internal/infrastructure/websocket"
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

	// MITM CA — signs per-hostname TLS certificates so the proxy can inspect
	// HTTPS traffic from workspace containers transparently.
	ca, err := tlsca.New()
	if err != nil {
		log.Fatalf("creating MITM CA: %v", err)
	}
	log.Printf("MITM CA ready (%d byte cert)", len(ca.PEM()))

	// Workspace registry — maps gateway IPs to workspace IDs so the CONNECT
	// handler can attribute MITM traffic to the originating workspace.
	wsRegistry := workspace.NewMemoryRegistry()

	// Workspace orchestrator (ComputeBackend → Orchestrator)
	var orchestrator *workspace.Orchestrator
	dockerBackend, err := docker.NewBackend()
	if err != nil {
		log.Printf("WARNING: Docker not available, workspace features disabled: %v", err)
	} else {
		// Wrap concrete types in port adapters
		var builder ports.ImageBuilder
		if dcBuilder != nil {
			builder = devcontainer.NewImageBuilderAdapter(dcBuilder)
		}
		gitHost := gh.NewGitHostAdapter(ghClient)

		// Egress enforcement: workspace containers are placed on internal Docker
		// networks that can only reach the Cordon proxy. This is always on unless
		// explicitly disabled — running without it means any process inside a
		// workspace can bypass the proxy and reach the internet directly.
		egressDisabled := os.Getenv("CORDON_EGRESS_ENFORCE") == "false"
		egressPolicy := domain.EgressPolicy{
			Enabled:   !egressDisabled,
			ProxyAddr: envOr("CORDON_PROXY_ADDR", "host.docker.internal:"+port),
		}
		if egressDisabled {
			log.Println("WARNING: Network-level egress enforcement DISABLED (CORDON_EGRESS_ENFORCE=false). Workspace containers can reach any host.")
		}

		orchestrator = workspace.NewOrchestrator(workspace.OrchestratorConfig{
			Backend:  dockerBackend,
			Builder:  builder,
			Token:    githubToken,
			GitHost:  gitHost,
			Progress: progressStore,
			Egress:   egressPolicy,
			CAPem:    ca.PEM(),
			Registry: wsRegistry,
		})
	}

	// Application services
	auditBroadcast := auditpkg.NewBroadcaster()
	auditService := auditpkg.NewService(auditStore)
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
		Broadcast:       auditBroadcast,
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

	// Workspace persistence
	workspaceStore := postgres.NewWorkspaceStore(pool)

	// Workspace lifecycle management (idle timeout + TTL enforcement)
	var lifecycleMgr *workspace.LifecycleManager
	var wsService ports.WorkspaceService
	var reaperCancel context.CancelFunc
	if orchestrator != nil {
		lifecycleMgr = workspace.NewLifecycleManager(15 * time.Minute)

		// Wrap orchestrator with persistent service so all operations update the DB.
		persistent := workspace.NewPersistentService(workspaceStore, orchestrator)
		wsService = persistent

		reaper := workspace.NewReaper(dockerBackend, persistent, lifecycleMgr, 30*time.Second)
		var reaperCtx context.Context
		reaperCtx, reaperCancel = context.WithCancel(context.Background())
		go reaper.Start(reaperCtx)
	}

	// Handlers
	healthHandler := handlers.NewHealthHandler(pool)
	proxyHandler := handlers.NewProxyHandler(pipeline)
	auditHandler := handlers.NewAuditHandler(auditService)
	approvalHandler := handlers.NewApprovalHandler(approvalStore)
	workspaceHandler := handlers.NewWorkspaceHandler(wsService, progressStore, lifecycleMgr, lifecycleMgr)
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
	mux.Handle("GET /api/github/user/orgs", authMW(http.HandlerFunc(githubHandler.UserOrgs)))
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
	mux.Handle("POST /api/workspaces/{id}/extend", authMW(http.HandlerFunc(workspaceHandler.Extend)))
	mux.Handle("POST /api/workspaces/{id}/{action}", authMW(http.HandlerFunc(workspaceHandler.Action)))

	// WebSocket endpoints
	agentRegistry := apiws.NewAgentRegistry()
	if orchestrator != nil {
		terminalHandler := apiws.NewTerminalHandler(orchestrator, lifecycleMgr)
		mux.Handle("/ws/terminal/", authMW(terminalHandler))
	}
	mux.Handle("/ws/agent/", authMW(apiws.NewAgentWSHandler(agentRegistry)))
	mux.Handle("/ws/approvals", authMW(apiws.NewApprovalWSHandler()))
	mux.Handle("/ws/audit", authMW(apiws.NewAuditWSHandler(auditBroadcast)))

	// HTTP CONNECT handler — lets workspace containers route git/curl/apt through
	// the proxy via standard http_proxy/https_proxy env vars, same egress path as
	// explicit /api/proxy/http calls. Sits before Logger/Recovery because CONNECT
	// tunnels hijack the connection for raw TCP — they're not request/response.
	connectHandler := handlers.NewConnectHandler(egress, pipeline, ca, tenantID, wsRegistry)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectHandler.ServeHTTP(w, r)
			return
		}
		middleware.Logger(middleware.Recovery(mux)).ServeHTTP(w, r)
	})

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
		if reaperCancel != nil {
			reaperCancel()
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutCtx)
	}()

	// Listen with PROXY protocol v1 support. Gateway containers use haproxy
	// with send-proxy to report the real workspace container IP. Direct browser
	// connections (no PROXY header) pass through unchanged.
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	ppListener := proxyproto.NewListener(ln)

	log.Printf("Cordon server starting on :%s (auth=%s, workspaces=%v)", port, authMode, orchestrator != nil)
	if err := server.Serve(ppListener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
