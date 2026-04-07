package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ApprovalGrantStore implements ports.GrantStore using PostgreSQL.
type ApprovalGrantStore struct {
	pool *pgxpool.Pool
}

func NewApprovalGrantStore(pool *pgxpool.Pool) *ApprovalGrantStore {
	return &ApprovalGrantStore{pool: pool}
}

func (s *ApprovalGrantStore) SaveGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string, grant domain.ApprovalGrant) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO approval_grants (tenant_id, workspace_id, operation, target, scope, pattern, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tenantID, workspaceID, operation, target,
		string(grant.Scope), nullableString(grant.Pattern), grant.ExpiresAt,
	)
	return err
}

func (s *ApprovalGrantStore) CheckGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string) (*domain.ApprovalGrant, error) {
	var scope string
	var pattern *string
	var expiresAt time.Time
	var id uuid.UUID

	err := s.pool.QueryRow(ctx,
		`SELECT id, scope, pattern, expires_at
		 FROM approval_grants
		 WHERE tenant_id = $1
		   AND workspace_id = $2
		   AND operation = $3
		   AND target = $4
		   AND NOT consumed
		   AND expires_at > NOW()
		 ORDER BY granted_at DESC
		 LIMIT 1`,
		tenantID, workspaceID, operation, target,
	).Scan(&id, &scope, &pattern, &expiresAt)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// One-time grants are consumed immediately on check.
	if domain.GrantScope(scope) == domain.GrantOneTime {
		_, err = s.pool.Exec(ctx,
			`UPDATE approval_grants SET consumed = TRUE WHERE id = $1`,
			id,
		)
		if err != nil {
			return nil, err
		}
	}

	grant := &domain.ApprovalGrant{
		Scope:     domain.GrantScope(scope),
		ExpiresAt: expiresAt,
	}
	if pattern != nil {
		grant.Pattern = *pattern
	}
	return grant, nil
}

func (s *ApprovalGrantStore) CleanExpired(ctx context.Context) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM approval_grants WHERE expires_at < NOW()`,
	)
	return err
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
