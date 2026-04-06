package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)


// workspaceConfigJSON is the JSONB representation of a workspace config.
type workspaceConfigJSON struct {
	Mode             string                      `json:"mode,omitempty"`
	Repos            []domain.RepoConfig         `json:"repos,omitempty"`
	Investigation    *domain.InvestigationState   `json:"investigation,omitempty"`
	DevcontainerPath string                      `json:"devcontainer_path,omitempty"`
	CPU              int                         `json:"cpu,omitempty"`
	MemoryMB         int                         `json:"memory_mb,omitempty"`
	IdleTimeout      string                      `json:"idle_timeout,omitempty"`
	MaxLifetime      string                      `json:"max_lifetime,omitempty"`
}

func configToJSON(c domain.WorkspaceConfig) ([]byte, error) {
	j := workspaceConfigJSON{
		Mode:             string(c.Mode),
		Repos:            c.Repos,
		Investigation:    c.Investigation,
		DevcontainerPath: c.DevcontainerPath,
		CPU:              c.CPU,
		MemoryMB:         c.MemoryMB,
	}
	if c.IdleTimeout > 0 {
		j.IdleTimeout = c.IdleTimeout.String()
	}
	if c.MaxLifetime > 0 {
		j.MaxLifetime = c.MaxLifetime.String()
	}
	return json.Marshal(j)
}

func configFromJSON(data []byte, wsID uuid.UUID) domain.WorkspaceConfig {
	var j workspaceConfigJSON
	json.Unmarshal(data, &j)

	c := domain.WorkspaceConfig{
		ID:               wsID,
		Mode:             domain.WorkspaceMode(j.Mode),
		Repos:            j.Repos,
		Investigation:    j.Investigation,
		DevcontainerPath: j.DevcontainerPath,
		CPU:              j.CPU,
		MemoryMB:         j.MemoryMB,
	}
	if j.IdleTimeout != "" {
		c.IdleTimeout, _ = time.ParseDuration(j.IdleTimeout)
	}
	if j.MaxLifetime != "" {
		c.MaxLifetime, _ = time.ParseDuration(j.MaxLifetime)
	}
	return c
}

// WorkspaceStore implements ports.WorkspaceStore using PostgreSQL.
type WorkspaceStore struct {
	pool *pgxpool.Pool
}

var _ ports.WorkspaceStore = (*WorkspaceStore)(nil)

func NewWorkspaceStore(pool *pgxpool.Pool) *WorkspaceStore {
	return &WorkspaceStore{pool: pool}
}

func (s *WorkspaceStore) Insert(ctx context.Context, ws domain.Workspace) error {
	configData, err := configToJSON(ws.Config)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	var spawnedFrom *uuid.UUID
	if ws.SpawnedFrom != nil {
		spawnedFrom = ws.SpawnedFrom
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO workspaces (id, tenant_id, name, status, mode, config, spawned_from, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		ws.ID, ws.TenantID, ws.Name, string(ws.Status),
		string(ws.Config.Mode), configData, spawnedFrom,
		ws.CreatedAt, ws.ExpiresAt,
	)
	return err
}

func (s *WorkspaceStore) Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	var ws domain.Workspace
	var status, mode string
	var configData []byte
	var spawnedFrom *uuid.UUID
	var suspendedAt, destroyedAt, lastActivity *time.Time

	err := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, name, status, mode, config, spawned_from,
		        created_at, expires_at, suspended_at, destroyed_at, last_activity_at
		 FROM workspaces WHERE id = $1 AND tenant_id = $2`,
		workspaceID, tenantID,
	).Scan(
		&ws.ID, &ws.TenantID, &ws.Name, &status, &mode, &configData,
		&spawnedFrom, &ws.CreatedAt, &ws.ExpiresAt,
		&suspendedAt, &destroyedAt, &lastActivity,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, ports.ErrWorkspaceNotFound
	}
	if err != nil {
		return domain.Workspace{}, err
	}

	ws.Status = domain.WorkspaceStatus(status)
	ws.Config = configFromJSON(configData, ws.ID)
	ws.Config.Mode = domain.WorkspaceMode(mode)
	ws.SpawnedFrom = spawnedFrom
	ws.SuspendedAt = suspendedAt
	return ws, nil
}

func (s *WorkspaceStore) List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, name, status, mode, config, spawned_from,
		        created_at, expires_at, suspended_at, destroyed_at, last_activity_at
		 FROM workspaces
		 WHERE tenant_id = $1 AND status != 'destroyed'
		 ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []domain.Workspace
	for rows.Next() {
		var ws domain.Workspace
		var status, mode string
		var configData []byte
		var spawnedFrom *uuid.UUID
		var suspendedAt, destroyedAt, lastActivity *time.Time

		if err := rows.Scan(
			&ws.ID, &ws.TenantID, &ws.Name, &status, &mode, &configData,
			&spawnedFrom, &ws.CreatedAt, &ws.ExpiresAt,
			&suspendedAt, &destroyedAt, &lastActivity,
		); err != nil {
			return nil, err
		}

		ws.Status = domain.WorkspaceStatus(status)
		ws.Config = configFromJSON(configData, ws.ID)
		ws.Config.Mode = domain.WorkspaceMode(mode)
		ws.SpawnedFrom = spawnedFrom
		ws.SuspendedAt = suspendedAt
		result = append(result, ws)
	}
	return result, rows.Err()
}

func (s *WorkspaceStore) UpdateStatus(ctx context.Context, workspaceID uuid.UUID, status domain.WorkspaceStatus) error {
	var query string
	switch status {
	case domain.WorkspaceSuspended:
		query = `UPDATE workspaces SET status = $2, suspended_at = NOW() WHERE id = $1`
	case domain.WorkspaceDestroyed:
		query = `UPDATE workspaces SET status = $2, destroyed_at = NOW() WHERE id = $1`
	case domain.WorkspaceRunning:
		query = `UPDATE workspaces SET status = $2, suspended_at = NULL WHERE id = $1`
	default:
		query = `UPDATE workspaces SET status = $2 WHERE id = $1`
	}
	_, err := s.pool.Exec(ctx, query, workspaceID, string(status))
	return err
}

func (s *WorkspaceStore) UpdateExpiry(ctx context.Context, workspaceID uuid.UUID, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workspaces SET expires_at = $2 WHERE id = $1`,
		workspaceID, expiresAt,
	)
	return err
}

func (s *WorkspaceStore) RecordActivity(ctx context.Context, workspaceID uuid.UUID, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE workspaces SET last_activity_at = $2 WHERE id = $1`,
		workspaceID, at,
	)
	return err
}

func (s *WorkspaceStore) UpdateConfig(ctx context.Context, workspaceID uuid.UUID, config domain.WorkspaceConfig) error {
	configData, err := configToJSON(config)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	_, err = s.pool.Exec(ctx,
		`UPDATE workspaces SET config = $2 WHERE id = $1`,
		workspaceID, configData,
	)
	return err
}
