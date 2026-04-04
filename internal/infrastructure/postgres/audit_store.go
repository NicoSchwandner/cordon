package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// AuditStore implements ports.AuditStore using PostgreSQL.
type AuditStore struct {
	pool *pgxpool.Pool
}

func NewAuditStore(pool *pgxpool.Pool) *AuditStore {
	return &AuditStore{pool: pool}
}

func (s *AuditStore) Write(ctx context.Context, entry domain.AuditEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO audit_entries (id, tenant_id, workspace_id, timestamp, tier, operation, target, caller, decision, duration_ms, detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		entry.ID, entry.TenantID, entry.WorkspaceID, entry.Timestamp,
		int(entry.Tier), entry.Operation, entry.Target, entry.Caller,
		string(entry.Decision), entry.DurationMs, entry.Detail,
	)
	return err
}

func (s *AuditStore) Query(ctx context.Context, filter ports.AuditFilter) ([]domain.AuditEntry, error) {
	var conditions []string
	var args []any
	argN := 1

	conditions = append(conditions, fmt.Sprintf("tenant_id = $%d", argN))
	args = append(args, filter.TenantID)
	argN++

	if filter.WorkspaceID != nil {
		conditions = append(conditions, fmt.Sprintf("workspace_id = $%d", argN))
		args = append(args, *filter.WorkspaceID)
		argN++
	}
	if filter.TierMin != nil {
		conditions = append(conditions, fmt.Sprintf("tier >= $%d", argN))
		args = append(args, int(*filter.TierMin))
		argN++
	}
	if filter.Decision != nil {
		conditions = append(conditions, fmt.Sprintf("decision = $%d", argN))
		args = append(args, string(*filter.Decision))
		argN++
	}
	if filter.Since != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp >= $%d", argN))
		args = append(args, *filter.Since)
		argN++
	}
	if filter.Until != nil {
		conditions = append(conditions, fmt.Sprintf("timestamp <= $%d", argN))
		args = append(args, *filter.Until)
		argN++
	}

	query := "SELECT id, tenant_id, workspace_id, timestamp, tier, operation, target, caller, decision, duration_ms, detail FROM audit_entries"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY timestamp DESC"

	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}
	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var tierInt int
		var decision string
		if err := rows.Scan(&e.ID, &e.TenantID, &e.WorkspaceID, &e.Timestamp,
			&tierInt, &e.Operation, &e.Target, &e.Caller,
			&decision, &e.DurationMs, &e.Detail); err != nil {
			return nil, err
		}
		e.Tier = domain.Tier(tierInt)
		e.Decision = domain.Decision(decision)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
