package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

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
	"github.com/NicoSchwandner/cordon/internal/infrastructure/config"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/devcontainer"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/docker"
	gh "github.com/NicoSchwandner/cordon/internal/infrastructure/github"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/postgres"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/proxyproto"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/sops"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/tlsca"
	ws "github.com/NicoSchwandner/cordon/internal/infrastructure/websocket"
)

func main() {
	cfg, err := config.LoadServer()
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	// Structured logging: JSON in production, text in development.
	var logHandler slog.Handler
	if os.Getenv("CORDON_LOG_FORMAT") == "json" {
		logHandler = slog.NewJSONHandler(os.Stdout, nil)
	} else {
		logHandler = slog.NewTextHandler(os.Stdout, nil)
	}
	slog.SetDefault(slog.New(logHandler))

	ctx := context.Background()

	// Database
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Warn("database not reachable", "error", err)
	}

	// Infrastructure
	auditStore := postgres.NewAuditStore(pool)
	approvalStore := ws.NewApprovalStore()
	grantStore := postgres.NewApprovalGrantStore(pool)
	approvalStore.SetGrantStore(grantStore)
	vault := sops.NewMemoryVault()

	tenantID := uuid.MustParse(cfg.Auth.DefaultTenantID)
	vault.AddSecret(tenantID, "DATABASE_URL", "cordon-placeholder-database-url", cfg.Secrets.RealDatabaseURL)
	vault.AddSecret(tenantID, "API_KEY", "cordon-placeholder-api-key", cfg.Secrets.RealAPIKey)

	// Devcontainer builder (optional — needs `devcontainer` CLI on PATH)
	var dcBuilder *devcontainer.Builder
	dcBuilder, err = devcontainer.NewBuilder()
	if err != nil {
		slog.Warn("devcontainer CLI not available, repo-based workspaces disabled", "error", err)
	}

	// GitHub token — read from config, register as secret.
	// Users can also register/update via POST /api/secrets at runtime.
	if cfg.Secrets.GitHubToken != "" {
		slog.Info("GITHUB_TOKEN configured — private repo cloning enabled")
		vault.AddSecret(tenantID, "GITHUB_TOKEN", "PLACEHOLDER_GITHUB_TOKEN", cfg.Secrets.GitHubToken)
	}

	// GitHub API client (shared across provider + handlers)
	ghClient := gh.NewClient(cfg.Secrets.GitHubToken, gh.ClientConfig{
		RepoListTTL:   cfg.GitHub.RepoListTTL,
		BranchListTTL: cfg.GitHub.BranchListTTL,
		HTTPTimeout:   cfg.GitHub.HTTPTimeout,
	})

	// Progress tracking
	timingStore := progress.NewTimingStore("data/build-timings.json")
	progressStore := progress.NewStore(timingStore)

	// MITM CA — signs per-hostname TLS certificates so the proxy can inspect
	// HTTPS traffic from workspace containers transparently.
	ca, err := tlsca.New()
	if err != nil {
		log.Fatalf("creating MITM CA: %v", err)
	}
	slog.Info("MITM CA ready", "cert_bytes", len(ca.PEM()))

	// Workspace registry — maps gateway IPs to workspace IDs so the CONNECT
	// handler can attribute MITM traffic to the originating workspace.
	wsRegistry := workspace.NewMemoryRegistry()

	// Workspace orchestrator (ComputeBackend → Orchestrator)
	var orchestrator *workspace.Orchestrator
	dockerBackend, err := docker.NewBackend()
	if err != nil {
		slog.Warn("Docker not available, workspace features disabled", "error", err)
	} else {
		// Wrap concrete types in port adapters
		var builder ports.ImageBuilder
		if dcBuilder != nil {
			builder = devcontainer.NewImageBuilderAdapter(dcBuilder)
		}
		gitHost := gh.NewGitHostAdapter(ghClient)

		egressPolicy := domain.EgressPolicy{
			Enabled:   cfg.Egress.Enforce,
			ProxyAddr: cfg.Egress.ProxyAddr,
		}
		if !cfg.Egress.Enforce {
			slog.Warn("network-level egress enforcement DISABLED, workspace containers can reach any host", "env", "CORDON_EGRESS_ENFORCE=false")
		}

		orchestrator = workspace.NewOrchestrator(workspace.OrchestratorConfig{
			Backend:          dockerBackend,
			Builder:          builder,
			Vault:            vault,
			TenantID:         tenantID,
			GitHost:          gitHost,
			Progress:         progressStore,
			Egress:           egressPolicy,
			CAPem:            ca.PEM(),
			Registry:         wsRegistry,
			DefaultMaxLifetime: cfg.Workspace.DefaultMaxLifetime,
			CloneConcurrency:   cfg.Workspace.CloneConcurrency,
		})
	}

	// Application services
	auditBroadcast := auditpkg.NewBroadcaster()
	auditService := auditpkg.NewService(auditStore)
	swapper := proxy.NewSecretSwapper(vault)
	egress := proxy.NewEgressChecker(cfg.Egress.Allowlist)

	pipeline := proxy.NewPipeline(proxy.PipelineConfig{
		SQLClassifier:   proxy.NewSQLClassifier(),
		HTTPClassifier:  proxy.NewHTTPClassifier(),
		Swapper:         swapper,
		Audit:           auditStore,
		Approver:        approvalStore,
		Egress:          egress,
		ApprovalTimeout: cfg.Proxy.ApprovalTimeout,
		Broadcast:       auditBroadcast,
	})

	// Auth
	var authValidator middleware.AuthValidator
	switch cfg.Auth.Mode {
	case "token":
		authValidator = &middleware.TokenAuth{
			Tokens: map[string]middleware.TokenInfo{
				cfg.Auth.APIToken: {TenantID: tenantID, UserID: "developer"},
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
		lifecycleMgr = workspace.NewLifecycleManager(cfg.Workspace.DefaultIdleTimeout)

		// Wrap orchestrator with persistent service so all operations update the DB.
		persistent := workspace.NewPersistentService(workspaceStore, orchestrator)
		wsService = persistent

		reaper := workspace.NewReaper(dockerBackend, persistent, lifecycleMgr, cfg.Workspace.ReaperInterval)
		var reaperCtx context.Context
		reaperCtx, reaperCancel = context.WithCancel(context.Background())
		go reaper.Start(reaperCtx)
	}

	// Handlers
	agentRegistry := apiws.NewAgentRegistry()
	healthHandler := handlers.NewHealthHandler(pool)
	proxyHandler := handlers.NewProxyHandler(pipeline, handlers.ProxyHandlerConfig{
		HTTPTimeout:     cfg.Proxy.HTTPTimeout,
		MaxResponseBody: cfg.Proxy.MaxResponseBody,
	})
	auditHandler := handlers.NewAuditHandler(auditService)
	approvalHandler := handlers.NewApprovalHandler(approvalStore)
	workspaceHandler := handlers.NewWorkspaceHandler(wsService, progressStore, lifecycleMgr, lifecycleMgr, agentRegistry, handlers.WorkspaceHandlerConfig{
		DefaultMaxLifetime: cfg.Workspace.DefaultMaxLifetime,
		MaxExtension:       cfg.Workspace.MaxExtension,
		MinLifetime:        cfg.Workspace.MinLifetime,
		DefaultIdleTimeout: cfg.Workspace.DefaultIdleTimeout,
		DefaultCPU:         cfg.Workspace.DefaultCPU,
		DefaultMemoryMB:    cfg.Workspace.DefaultMemoryMB,
		AsyncTimeout:       cfg.Workspace.AsyncTimeout,
	})
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
		Addr:         ":" + cfg.Server.Port,
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: 0, // Disable for WebSocket
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("shutting down")
		if reaperCancel != nil {
			reaperCancel()
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		server.Shutdown(shutCtx)
	}()

	// Listen with PROXY protocol v1 support. Gateway containers use haproxy
	// with send-proxy to report the real workspace container IP. Direct browser
	// connections (no PROXY header) pass through unchanged.
	ln, err := net.Listen("tcp", ":"+cfg.Server.Port)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	ppListener := proxyproto.NewListener(ln)

	slog.Info("Cordon server starting", "port", cfg.Server.Port, "auth", cfg.Auth.Mode, "workspaces", orchestrator != nil)
	if err := server.Serve(ppListener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}
}
