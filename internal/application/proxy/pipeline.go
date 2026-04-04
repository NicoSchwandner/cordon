package proxy

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// Pipeline orchestrates the full proxy flow: egress check → classify → approve → swap → audit.
type Pipeline struct {
	sqlClassifier  *SQLClassifier
	httpClassifier *HTTPClassifier
	swapper        *SecretSwapper
	audit          ports.AuditStore
	approver       ports.ApprovalService
	egress         *EgressChecker
	overrides      []ports.TierOverride
	approvalTimeout time.Duration
}

type PipelineConfig struct {
	SQLClassifier   *SQLClassifier
	HTTPClassifier  *HTTPClassifier
	Swapper         *SecretSwapper
	Audit           ports.AuditStore
	Approver        ports.ApprovalService
	Egress          *EgressChecker
	Overrides       []ports.TierOverride
	ApprovalTimeout time.Duration
}

func NewPipeline(cfg PipelineConfig) *Pipeline {
	timeout := cfg.ApprovalTimeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	return &Pipeline{
		sqlClassifier:  cfg.SQLClassifier,
		httpClassifier: cfg.HTTPClassifier,
		swapper:        cfg.Swapper,
		audit:          cfg.Audit,
		approver:       cfg.Approver,
		egress:         cfg.Egress,
		overrides:      cfg.Overrides,
		approvalTimeout: timeout,
	}
}

func (p *Pipeline) Process(ctx context.Context, req ProxyRequest) ProxyResponse {
	start := time.Now()

	// Step 1: Egress check (HTTP only — SQL goes through the internal proxy, not external)
	if p.egress != nil && req.QueryType == QueryTypeHTTP && req.Host != "" && !p.egress.IsAllowed(req.Host) {
		entry := p.auditEntry(req, domain.TierDestructive, domain.DecisionBlocked, start)
		entry.Detail = "egress blocked: " + req.Host
		p.writeAudit(ctx, entry)
		return ProxyResponse{
			Tier:     domain.TierDestructive,
			Decision: domain.DecisionBlocked,
			Problem: &domain.ProblemDetails{
				Type:   "https://cordon.dev/problems/egress-denied",
				Title:  "Egress Blocked",
				Status: 403,
				Detail: "Egress blocked: " + req.Host + " not in allowlist",
				Code:   "egress_denied",
			},
		}
	}

	// Step 2: Classify tier
	var result domain.TierResult
	switch req.QueryType {
	case QueryTypeSQL:
		result = p.sqlClassifier.ClassifySQL(req.SQLQuery, p.overrides)
	case QueryTypeHTTP:
		result = p.httpClassifier.ClassifyHTTP(req.Method, req.Path, p.overrides)
	default:
		result = domain.TierResult{Tier: domain.TierDestructive, Reason: "unknown query type"}
	}

	// Step 3: Tier 4 — always block
	if result.Tier.IsBlocked() {
		entry := p.auditEntry(req, result.Tier, domain.DecisionBlocked, start)
		entry.Detail = "blocked: " + result.Reason
		p.writeAudit(ctx, entry)
		return ProxyResponse{
			Tier:     result.Tier,
			Decision: domain.DecisionBlocked,
			Problem: &domain.ProblemDetails{
				Type:   "https://cordon.dev/problems/tier-blocked",
				Title:  "Operation Blocked",
				Status: 403,
				Detail: "Operation blocked: " + result.Reason,
				Code:   "tier_blocked",
			},
		}
	}

	// Step 4: Tier 3 — check approval
	if result.Tier == domain.TierDestructive && p.approver != nil {
		grant, err := p.approver.CheckExistingGrant(ctx, req.TenantID, req.WorkspaceID, operationStr(req), req.Host)
		if err != nil || grant == nil {
			// No existing grant — request approval
			approvalReq := domain.ApprovalRequest{
				ID:          uuid.New(),
				TenantID:    req.TenantID,
				WorkspaceID: req.WorkspaceID,
				Tier:        result.Tier,
				Operation:   operationStr(req),
				Target:      targetStr(req),
				Caller:      req.Caller,
				RequestedAt: time.Now().UTC(),
				ExpiresAt:   time.Now().UTC().Add(p.approvalTimeout),
				Status:      domain.ApprovalPending,
			}
			if err := p.approver.RequestApproval(ctx, approvalReq); err != nil {
				entry := p.auditEntry(req, result.Tier, domain.DecisionDenied, start)
				entry.Detail = "approval request failed: " + err.Error()
				p.writeAudit(ctx, entry)
				return ProxyResponse{
					Tier:     result.Tier,
					Decision: domain.DecisionDenied,
					Problem: &domain.ProblemDetails{
						Type:   "https://cordon.dev/problems/approval-failed",
						Title:  "Approval Failed",
						Status: 500,
						Detail: "Failed to request approval",
						Code:   "approval_failed",
					},
				}
			}

			decision, err := p.approver.AwaitDecision(ctx, approvalReq.ID, p.approvalTimeout)
			if err != nil {
				entry := p.auditEntry(req, result.Tier, domain.DecisionDenied, start)
				entry.Detail = "approval timeout or error"
				p.writeAudit(ctx, entry)
				return ProxyResponse{
					Tier:     result.Tier,
					Decision: domain.DecisionDenied,
					Problem: &domain.ProblemDetails{
						Type:   "https://cordon.dev/problems/approval-timeout",
						Title:  "Approval Timeout",
						Status: 408,
						Detail: "Approval request timed out",
						Code:   "approval_timeout",
					},
				}
			}

			if decision.Scope == "" {
				entry := p.auditEntry(req, result.Tier, domain.DecisionDenied, start)
				entry.Detail = "approval denied"
				p.writeAudit(ctx, entry)
				return ProxyResponse{
					Tier:     result.Tier,
					Decision: domain.DecisionDenied,
					Problem: &domain.ProblemDetails{
						Type:   "https://cordon.dev/problems/approval-denied",
						Title:  "Operation Denied",
						Status: 403,
						Detail: "Operation denied by approver",
						Code:   "approval_denied",
					},
				}
			}
		}
	}

	// Step 5: Secret swap
	modified := &req
	var swappedSecrets []string
	if p.swapper != nil {
		var err error
		modified, swappedSecrets, err = p.swapper.Swap(ctx, req.TenantID, &req)
		if err != nil {
			entry := p.auditEntry(req, result.Tier, domain.DecisionDenied, start)
			entry.Detail = "secret swap failed"
			p.writeAudit(ctx, entry)
			return ProxyResponse{
				Tier:     result.Tier,
				Decision: domain.DecisionDenied,
				Problem: &domain.ProblemDetails{
					Type:   "https://cordon.dev/problems/secret-swap-failed",
					Title:  "Secret Swap Failed",
					Status: 500,
					Detail: "Failed to swap secrets",
					Code:   "secret_swap_failed",
				},
			}
		}
	}

	// Step 6: Write audit (with redacted detail — no real credentials in audit)
	decision := domain.DecisionAllowed
	entry := p.auditEntry(req, result.Tier, decision, start)
	detail := operationDetail(req)
	if p.swapper != nil && len(swappedSecrets) > 0 {
		redacted, _ := p.swapper.RedactSecrets(ctx, req.TenantID, detail)
		entry.Detail = redacted
	} else {
		entry.Detail = detail
	}
	p.writeAudit(ctx, entry)

	return ProxyResponse{
		Allowed:     true,
		Tier:        result.Tier,
		Decision:    decision,
		ModifiedReq: modified,
	}
}

func (p *Pipeline) auditEntry(req ProxyRequest, tier domain.Tier, decision domain.Decision, start time.Time) domain.AuditEntry {
	return domain.AuditEntry{
		ID:          uuid.New(),
		TenantID:    req.TenantID,
		WorkspaceID: req.WorkspaceID,
		Timestamp:   time.Now().UTC(),
		Tier:        tier,
		Operation:   operationStr(req),
		Target:      targetStr(req),
		Caller:      req.Caller,
		Decision:    decision,
		DurationMs:  time.Since(start).Milliseconds(),
	}
}

func (p *Pipeline) writeAudit(ctx context.Context, entry domain.AuditEntry) {
	if p.audit != nil {
		_ = p.audit.Write(ctx, entry) // best-effort; don't block request on audit failure
	}
}

func operationStr(req ProxyRequest) string {
	if req.QueryType == QueryTypeSQL {
		return "SQL"
	}
	return "HTTP:" + req.Method
}

func targetStr(req ProxyRequest) string {
	if req.Host != "" {
		return req.Host + req.Path
	}
	return req.Path
}

func operationDetail(req ProxyRequest) string {
	if req.QueryType == QueryTypeSQL {
		return req.SQLQuery
	}
	return req.Method + " " + req.Path
}
