package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
}

type Workspace struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func storeError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		switch pgError.Code {
		case "23503":
			return ErrNotFound
		case "23505":
			return ErrConflict
		}
	}
	return err
}

// Bootstrap creates an initial owner and workspace while preserving existing credentials.
func (s *Store) Bootstrap(ctx context.Context, username, passwordHash, slug, name string) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plume.users (username, password_hash) VALUES ($1, $2)
			ON CONFLICT (username) DO NOTHING
		`, username, passwordHash); err != nil {
			return err
		}

		var workspaceID int64
		err := tx.QueryRow(ctx, `
			INSERT INTO plume.workspaces (slug, name) VALUES ($1, $2)
			ON CONFLICT (slug) DO NOTHING RETURNING id
		`, slug, name).Scan(&workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			var ownsWorkspace bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM plume.memberships AS m
					JOIN plume.users AS u ON u.id = m.user_id
					JOIN plume.workspaces AS w ON w.id = m.workspace_id
					WHERE u.username = $1 AND w.slug = $2 AND m.role = 'owner'
				)
			`, username, slug).Scan(&ownsWorkspace); err != nil {
				return err
			}
			if !ownsWorkspace {
				return ErrConflict
			}
			return nil
		}
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO plume.memberships (workspace_id, user_id, role)
			SELECT $1, id, 'owner' FROM plume.users WHERE username = $2
		`, workspaceID, username)
		return err
	})
}

func (s *Store) FindUser(ctx context.Context, username string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT id, username, password_hash FROM plume.users WHERE username = $1
	`, username).Scan(&user.ID, &user.Username, &user.PasswordHash)
	return user, storeError(err)
}

func (s *Store) CreateSession(ctx context.Context, userID int64, tokenHash string, expires time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO plume.sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)
	`, userID, tokenHash, expires)
	return storeError(err)
}

func (s *Store) SessionUser(ctx context.Context, tokenHash string) (User, error) {
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.username
		FROM plume.sessions AS s
		JOIN plume.users AS u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > pg_catalog.now()
	`, tokenHash).Scan(&user.ID, &user.Username)
	return user, storeError(err)
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.pool.Exec(ctx, "DELETE FROM plume.sessions WHERE token_hash = $1", tokenHash)
	return err
}

const workspaceSelect = `
	SELECT w.id, w.slug, w.name, m.role
	FROM plume.memberships AS m
	JOIN plume.workspaces AS w ON w.id = m.workspace_id
	WHERE m.user_id = $1
`

func (s *Store) ListWorkspaces(ctx context.Context, userID int64) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx, workspaceSelect+" ORDER BY w.name, w.slug", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	workspaces := make([]Workspace, 0)
	for rows.Next() {
		var workspace Workspace
		if err := rows.Scan(&workspace.ID, &workspace.Slug, &workspace.Name, &workspace.Role); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

func (s *Store) Membership(ctx context.Context, userID int64, slug string) (Workspace, error) {
	var workspace Workspace
	err := s.pool.QueryRow(ctx, workspaceSelect+" AND w.slug = $2", userID, slug).
		Scan(&workspace.ID, &workspace.Slug, &workspace.Name, &workspace.Role)
	return workspace, storeError(err)
}
