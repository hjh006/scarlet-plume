package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// IdentitySettings contains references, never client secrets or provisioning tokens.
type IdentitySettings struct {
	Revision        int64             `json:"revision"`
	Enabled         bool              `json:"enabled"`
	Issuer          string            `json:"issuer"`
	ClientID        string            `json:"clientId"`
	ClientSecretRef string            `json:"clientSecretRef"`
	SCIMEnabled     bool              `json:"scimEnabled"`
	HasSCIMToken    bool              `json:"hasScimToken"`
	DefaultRole     string            `json:"defaultRole"`
	GroupRoles      map[string]string `json:"groupRoles"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

type IdentityUser struct {
	ID          int64     `json:"id"`
	ExternalID  string    `json:"externalId"`
	UserName    string    `json:"userName"`
	DisplayName string    `json:"displayName"`
	Active      bool      `json:"active"`
	Role        string    `json:"role"`
	Groups      []string  `json:"groups"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type IdentityGroup struct {
	ID          int64     `json:"id"`
	ExternalID  string    `json:"externalId"`
	DisplayName string    `json:"displayName"`
	Members     []int64   `json:"members"`
	Revision    int64     `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type IdentityLoginState struct {
	StateHash, BrowserHash, Nonce, Verifier string
	WorkspaceID, SettingsRevision           int64
	ExpiresAt                               time.Time
}

var identitySecretRef = regexp.MustCompile(`^env:SCARLET_IDP_[A-Z0-9_]{1,96}$`)
var identityTokenHash = regexp.MustCompile(`^[a-f0-9]{64}$`)

func identityText(v string, max int) bool {
	return utf8.ValidString(v) && strings.TrimSpace(v) != "" && utf8.RuneCountInString(v) <= max && strings.IndexFunc(v, unicode.IsControl) < 0
}

func identityRole(v string) bool { return v == "viewer" || v == "reviewer" || v == "operator" }

func ValidateIdentitySettings(c IdentitySettings) error {
	if c.Revision < 0 || !identityRole(c.DefaultRole) || len(c.GroupRoles) > 64 {
		return ErrInvalid
	}
	for k, v := range c.GroupRoles {
		if !identityText(k, 255) || !identityRole(v) {
			return ErrInvalid
		}
	}
	if c.Issuer == "" && !c.Enabled && !c.SCIMEnabled && c.ClientID == "" && c.ClientSecretRef == "" {
		return nil
	}
	u, err := url.Parse(c.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || !identityText(c.Issuer, 2048) || !identityText(c.ClientID, 255) || !identitySecretRef.MatchString(c.ClientSecretRef) {
		return ErrInvalid
	}
	return nil
}

func identityConfig(ctx context.Context, tx pgx.Tx, ws int64, lock bool) (IdentitySettings, error) {
	q := `SELECT revision,enabled,issuer,client_id,client_secret_ref,scim_enabled,scim_token_hash <> '',default_role,group_roles,updated_at FROM plume.ai_identity_settings WHERE workspace_id=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	c := IdentitySettings{DefaultRole: "viewer", GroupRoles: map[string]string{}}
	var groups []byte
	err := tx.QueryRow(ctx, q, ws).Scan(&c.Revision, &c.Enabled, &c.Issuer, &c.ClientID, &c.ClientSecretRef, &c.SCIMEnabled, &c.HasSCIMToken, &c.DefaultRole, &groups, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) && !lock {
		return c, nil
	}
	if err == nil {
		err = json.Unmarshal(groups, &c.GroupRoles)
	}
	return c, storeError(err)
}

func identityOwner(ctx context.Context, tx pgx.Tx, ws, actor int64) error {
	var yes bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.memberships WHERE workspace_id=$1 AND user_id=$2 AND role='owner')`, ws, actor).Scan(&yes)
	if err == nil && !yes {
		return ErrNotFound
	}
	return err
}

func (s *Store) IdentitySettings(ctx context.Context, ws, actor int64) (out IdentitySettings, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := identityOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		out, err = identityConfig(ctx, tx, ws, false)
		return err
	})
	return
}

func (s *Store) SaveIdentitySettings(ctx context.Context, ws, actor int64, in IdentitySettings) (out IdentitySettings, err error) {
	if err = ValidateIdentitySettings(in); err != nil {
		return
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := identityOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO plume.ai_identity_settings(workspace_id) VALUES($1) ON CONFLICT DO NOTHING`, ws); err != nil {
			return err
		}
		old, err := identityConfig(ctx, tx, ws, true)
		if err != nil {
			return err
		}
		if old.Revision != in.Revision {
			return ErrConflict
		}
		if old.Issuer != in.Issuer || old.ClientID != in.ClientID {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM plume.ai_identity_users WHERE workspace_id=$1)`, ws).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrConflict
			}
		}
		groups, _ := json.Marshal(in.GroupRoles)
		if in.GroupRoles == nil {
			groups = []byte(`{}`)
		}
		_, err = tx.Exec(ctx, `UPDATE plume.ai_identity_settings SET revision=revision+1,enabled=$2,issuer=$3,client_id=$4,client_secret_ref=$5,scim_enabled=$6,default_role=$7,group_roles=$8,updated_at=now() WHERE workspace_id=$1`, ws, in.Enabled, in.Issuer, in.ClientID, in.ClientSecretRef, in.SCIMEnabled, in.DefaultRole, groups)
		if err != nil {
			return err
		}
		in.Revision++
		if _, err := tx.Exec(ctx, `DELETE FROM plume.sessions s USING plume.ai_identity_users i WHERE i.workspace_id=$1 AND i.user_id=s.user_id`, ws); err != nil {
			return err
		}
		if err := identityReconcile(ctx, tx, ws, in); err != nil {
			return err
		}
		if err := identityAudit(ctx, tx, ws, actor, "identity.settings", fmt.Sprint(in.Revision), in); err != nil {
			return err
		}
		out, err = identityConfig(ctx, tx, ws, false)
		return err
	})
	return
}

func (s *Store) RotateIdentitySCIMToken(ctx context.Context, ws, actor int64, tokenHash string) error {
	if !identityTokenHash.MatchString(tokenHash) {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if err := identityOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		c, err := identityConfig(ctx, tx, ws, true)
		if err != nil {
			return err
		}
		if !c.SCIMEnabled {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE plume.ai_identity_settings SET scim_token_hash=$2,revision=revision+1,updated_at=now() WHERE workspace_id=$1`, ws, tokenHash); err != nil {
			return err
		}
		return identityAudit(ctx, tx, ws, actor, "identity.scim_token_rotated", "settings", map[string]int64{"revision": c.Revision + 1})
	})
}

// Recompute grants from trusted SCIM groups. IdP token role/email claims never grant access.
func identityReconcile(ctx context.Context, tx pgx.Tx, ws int64, c IdentitySettings) error {
	users, err := identityReadUsers(ctx, tx, ws, 0, "", "", 0, 10001)
	if err != nil {
		return err
	}
	if len(users) > 10000 {
		return ErrConflict
	}
	ids, roles, active := make([]int64, 0, len(users)), make([]string, 0, len(users)), make([]bool, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
		roles = append(roles, identityEffectiveRole(c, u.Groups))
		active = append(active, c.Enabled && u.Active)
	}
	// Bounded to 10,000 provisioned accounts; apply the set in three database
	// operations instead of issuing per-user queries or logging out unrelated users.
	if _, err := tx.Exec(ctx, `DELETE FROM plume.sessions s USING plume.ai_identity_users i,unnest($2::bigint[],$3::text[],$4::bool[]) AS g(id,role,active) WHERE i.workspace_id=$1 AND i.id=g.id AND i.user_id=s.user_id AND (NOT g.active OR NOT EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=i.workspace_id AND m.user_id=i.user_id AND m.role=g.role))`, ws, ids, roles, active); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plume.memberships m USING plume.ai_identity_users i,unnest($2::bigint[],$3::bool[]) AS g(id,active) WHERE i.workspace_id=$1 AND i.id=g.id AND m.workspace_id=i.workspace_id AND m.user_id=i.user_id AND NOT g.active`, ws, ids, active); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO plume.memberships(workspace_id,user_id,role) SELECT i.workspace_id,i.user_id,g.role FROM plume.ai_identity_users i JOIN unnest($2::bigint[],$3::text[],$4::bool[]) AS g(id,role,active) ON g.id=i.id WHERE i.workspace_id=$1 AND g.active ON CONFLICT(workspace_id,user_id) DO UPDATE SET role=excluded.role`, ws, ids, roles, active)
	return err
}

func identityEffectiveRole(c IdentitySettings, groups []string) string {
	role := c.DefaultRole
	rank := map[string]int{"viewer": 0, "reviewer": 1, "operator": 2}
	for _, g := range groups {
		if r, ok := c.GroupRoles[g]; ok && rank[r] > rank[role] {
			role = r
		}
	}
	return role
}

func identityReadUsers(ctx context.Context, tx pgx.Tx, ws, id int64, externalID, userName string, start, count int) ([]IdentityUser, error) {
	rows, err := tx.Query(ctx, `SELECT i.id,i.external_id,i.user_name,i.display_name,i.active,coalesce(m.role,''),i.revision,i.updated_at,
		coalesce((SELECT array_agg(g.external_id ORDER BY g.external_id) FROM plume.ai_identity_group_members gm JOIN plume.ai_identity_groups g ON g.id=gm.group_id AND g.workspace_id=gm.workspace_id WHERE gm.workspace_id=i.workspace_id AND gm.identity_id=i.id),ARRAY[]::text[])
		FROM plume.ai_identity_users i LEFT JOIN plume.memberships m ON m.workspace_id=i.workspace_id AND m.user_id=i.user_id
		WHERE i.workspace_id=$1 AND ($2::bigint=0 OR i.id=$2) AND ($3='' OR i.external_id=$3) AND ($4='' OR lower(i.user_name)=lower($4)) ORDER BY i.id OFFSET $5 LIMIT $6`, ws, id, externalID, userName, start, count)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IdentityUser{}
	for rows.Next() {
		var u IdentityUser
		if err := rows.Scan(&u.ID, &u.ExternalID, &u.UserName, &u.DisplayName, &u.Active, &u.Role, &u.Revision, &u.UpdatedAt, &u.Groups); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) IdentityUsers(ctx context.Context, ws, actor int64) (out []IdentityUser, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := identityOwner(ctx, tx, ws, actor); err != nil {
			return err
		}
		out, err = identityReadUsers(ctx, tx, ws, 0, "", "", 0, 1000)
		return err
	})
	return
}

func identitySCIM(ctx context.Context, tx pgx.Tx, slug, tokenHash string) (ws int64, c IdentitySettings, err error) {
	if !identityTokenHash.MatchString(tokenHash) {
		return 0, c, ErrNotFound
	}
	err = tx.QueryRow(ctx, `SELECT s.workspace_id FROM plume.ai_identity_settings s JOIN plume.workspaces w ON w.id=s.workspace_id WHERE w.slug=$1 AND s.scim_enabled AND s.scim_token_hash=$2 FOR UPDATE OF s`, slug, tokenHash).Scan(&ws)
	if err != nil {
		return 0, c, storeError(err)
	}
	c, err = identityConfig(ctx, tx, ws, false)
	return
}

func (s *Store) SCIMIdentityUsers(ctx context.Context, slug, tokenHash string, id int64, externalID, userName string, start, count int) (out []IdentityUser, total int, err error) {
	if start < 0 || count < 0 || count > 200 {
		return nil, 0, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, _, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_users WHERE workspace_id=$1 AND ($2::bigint=0 OR id=$2) AND ($3='' OR external_id=$3) AND ($4='' OR lower(user_name)=lower($4))`, ws, id, externalID, userName).Scan(&total); e != nil {
			return e
		}
		out, err = identityReadUsers(ctx, tx, ws, id, externalID, userName, start, count)
		return err
	})
	return
}

func (s *Store) SaveSCIMIdentityUser(ctx context.Context, slug, tokenHash string, id int64, in IdentityUser, expected int64) (out IdentityUser, err error) {
	if !identityText(in.ExternalID, 255) || !identityText(in.UserName, 255) || len(in.DisplayName) > 512 || strings.IndexFunc(in.DisplayName, unicode.IsControl) >= 0 {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, c, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		if id == 0 {
			var n int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_users WHERE workspace_id=$1`, ws).Scan(&n); e != nil {
				return e
			}
			if n >= 10000 {
				return ErrConflict
			}
			// Never link an external identity to a local account by email or userName.
			sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", ws, c.Issuer, in.ExternalID)))
			var uid int64
			if e := tx.QueryRow(ctx, `INSERT INTO plume.users(username,password_hash) VALUES($1,'!federated-only') RETURNING id`, "idp-"+hex.EncodeToString(sum[:])).Scan(&uid); e != nil {
				return storeError(e)
			}
			e = tx.QueryRow(ctx, `INSERT INTO plume.ai_identity_users(workspace_id,user_id,external_id,user_name,display_name,active) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, ws, uid, in.ExternalID, in.UserName, in.DisplayName, in.Active).Scan(&id)
		} else {
			var external string
			var revision int64
			if e := tx.QueryRow(ctx, `SELECT external_id,revision FROM plume.ai_identity_users WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&external, &revision); e != nil {
				return storeError(e)
			}
			if external != in.ExternalID {
				return ErrInvalid
			}
			if expected != 0 && expected != revision {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, `UPDATE plume.ai_identity_users SET user_name=$3,display_name=$4,active=$5,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, in.UserName, in.DisplayName, in.Active)
		}
		if e != nil {
			return storeError(e)
		}
		if e := identityReconcile(ctx, tx, ws, c); e != nil {
			return e
		}
		if e := identityAudit(ctx, tx, ws, 0, "identity.scim_user_saved", fmt.Sprint(id), map[string]any{"id": id, "active": in.Active}); e != nil {
			return e
		}
		users, e := identityReadUsers(ctx, tx, ws, id, "", "", 0, 1)
		if e != nil {
			return e
		}
		out = users[0]
		return nil
	})
	return
}

func (s *Store) DeleteSCIMIdentityUser(ctx context.Context, slug, tokenHash string, id, expected int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, _, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		var uid, revision int64
		if e := tx.QueryRow(ctx, `SELECT user_id,revision FROM plume.ai_identity_users WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&uid, &revision); e != nil {
			return storeError(e)
		}
		if expected != 0 && revision != expected {
			return ErrConflict
		}
		if _, e := tx.Exec(ctx, `DELETE FROM plume.ai_identity_users WHERE workspace_id=$1 AND id=$2`, ws, id); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `DELETE FROM plume.users WHERE id=$1`, uid); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, 0, "identity.scim_user_deleted", fmt.Sprint(id), map[string]int64{"id": id})
	})
}

func identityReadGroups(ctx context.Context, tx pgx.Tx, ws, id int64, externalID, displayName string, start, count int) ([]IdentityGroup, error) {
	rows, e := tx.Query(ctx, `SELECT g.id,g.external_id,g.display_name,g.revision,g.updated_at,coalesce((SELECT array_agg(identity_id ORDER BY identity_id) FROM plume.ai_identity_group_members m WHERE m.workspace_id=g.workspace_id AND m.group_id=g.id),ARRAY[]::bigint[]) FROM plume.ai_identity_groups g WHERE workspace_id=$1 AND ($2::bigint=0 OR id=$2) AND ($3='' OR external_id=$3) AND ($4='' OR display_name=$4) ORDER BY id OFFSET $5 LIMIT $6`, ws, id, externalID, displayName, start, count)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []IdentityGroup{}
	for rows.Next() {
		var g IdentityGroup
		if e := rows.Scan(&g.ID, &g.ExternalID, &g.DisplayName, &g.Revision, &g.UpdatedAt, &g.Members); e != nil {
			return nil, e
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) SCIMIdentityGroups(ctx context.Context, slug, tokenHash string, id int64, externalID, displayName string, start, count int) (out []IdentityGroup, total int, err error) {
	if start < 0 || count < 0 || count > 200 {
		return nil, 0, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, _, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_groups WHERE workspace_id=$1 AND ($2::bigint=0 OR id=$2) AND ($3='' OR external_id=$3) AND ($4='' OR display_name=$4)`, ws, id, externalID, displayName).Scan(&total); e != nil {
			return e
		}
		out, err = identityReadGroups(ctx, tx, ws, id, externalID, displayName, start, count)
		return err
	})
	return
}

func (s *Store) SaveSCIMIdentityGroup(ctx context.Context, slug, tokenHash string, id int64, in IdentityGroup, expected int64) (out IdentityGroup, err error) {
	if !identityText(in.ExternalID, 255) || !identityText(in.DisplayName, 255) || len(in.Members) > 1000 {
		return out, ErrInvalid
	}
	slices.Sort(in.Members)
	if len(slices.Compact(in.Members)) != len(in.Members) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, c, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		if id == 0 {
			var n int
			if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_groups WHERE workspace_id=$1`, ws).Scan(&n); e != nil {
				return e
			}
			if n >= 1000 {
				return ErrConflict
			}
			e = tx.QueryRow(ctx, `INSERT INTO plume.ai_identity_groups(workspace_id,external_id,display_name) VALUES($1,$2,$3) RETURNING id`, ws, in.ExternalID, in.DisplayName).Scan(&id)
		} else {
			var external string
			var revision int64
			if e := tx.QueryRow(ctx, `SELECT external_id,revision FROM plume.ai_identity_groups WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&external, &revision); e != nil {
				return storeError(e)
			}
			if external != in.ExternalID {
				return ErrInvalid
			}
			if expected != 0 && expected != revision {
				return ErrConflict
			}
			_, e = tx.Exec(ctx, `UPDATE plume.ai_identity_groups SET display_name=$3,revision=revision+1,updated_at=now() WHERE workspace_id=$1 AND id=$2`, ws, id, in.DisplayName)
		}
		if e != nil {
			return storeError(e)
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_users WHERE workspace_id=$1 AND id=ANY($2::bigint[])`, ws, in.Members).Scan(&n); e != nil {
			return e
		}
		if n != len(in.Members) {
			return ErrNotFound
		}
		if _, e := tx.Exec(ctx, `DELETE FROM plume.ai_identity_group_members WHERE workspace_id=$1 AND group_id=$2`, ws, id); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO plume.ai_identity_group_members(workspace_id,group_id,identity_id) SELECT $1,$2,unnest($3::bigint[])`, ws, id, in.Members); e != nil {
			return e
		}
		if e := identityReconcile(ctx, tx, ws, c); e != nil {
			return e
		}
		if e := identityAudit(ctx, tx, ws, 0, "identity.scim_group_saved", fmt.Sprint(id), map[string]any{"id": id, "memberCount": len(in.Members)}); e != nil {
			return e
		}
		groups, e := identityReadGroups(ctx, tx, ws, id, "", "", 0, 1)
		if e != nil {
			return e
		}
		out = groups[0]
		return nil
	})
	return
}

func (s *Store) DeleteSCIMIdentityGroup(ctx context.Context, slug, tokenHash string, id, expected int64) error {
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		ws, c, e := identitySCIM(ctx, tx, slug, tokenHash)
		if e != nil {
			return e
		}
		var revision int64
		if e := tx.QueryRow(ctx, `SELECT revision FROM plume.ai_identity_groups WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, ws, id).Scan(&revision); e != nil {
			return storeError(e)
		}
		if expected != 0 && expected != revision {
			return ErrConflict
		}
		tag, e := tx.Exec(ctx, `DELETE FROM plume.ai_identity_groups WHERE workspace_id=$1 AND id=$2`, ws, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if e := identityReconcile(ctx, tx, ws, c); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, 0, "identity.scim_group_deleted", fmt.Sprint(id), map[string]int64{"id": id})
	})
}

func (s *Store) IdentityLoginConfig(ctx context.Context, slug string) (ws int64, out IdentitySettings, err error) {
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT s.workspace_id FROM plume.ai_identity_settings s JOIN plume.workspaces w ON w.id=s.workspace_id WHERE w.slug=$1 AND s.enabled`, slug).Scan(&ws); e != nil {
			return storeError(e)
		}
		out, err = identityConfig(ctx, tx, ws, false)
		return err
	})
	return
}

func (s *Store) BeginIdentityLogin(ctx context.Context, in IdentityLoginState) error {
	if !identityTokenHash.MatchString(in.StateHash) || !identityTokenHash.MatchString(in.BrowserHash) || len(in.Nonce) != 43 || len(in.Verifier) != 43 || in.ExpiresAt.After(time.Now().Add(6*time.Minute)) || !in.ExpiresAt.After(time.Now()) {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		c, e := identityConfig(ctx, tx, in.WorkspaceID, true)
		if e != nil {
			return e
		}
		if !c.Enabled || c.Revision != in.SettingsRevision {
			return ErrConflict
		}
		if _, e := tx.Exec(ctx, `DELETE FROM plume.ai_identity_login_states WHERE expires_at<now()`); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_identity_login_states WHERE workspace_id=$1`, in.WorkspaceID).Scan(&n); e != nil {
			return e
		}
		if n >= 200 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO plume.ai_identity_login_states(state_hash,workspace_id,settings_revision,browser_hash,nonce,verifier,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, in.StateHash, in.WorkspaceID, in.SettingsRevision, in.BrowserHash, in.Nonce, in.Verifier, in.ExpiresAt)
		return storeError(e)
	})
}

func (s *Store) ConsumeIdentityLogin(ctx context.Context, ws int64, stateHash, browserHash string) (out IdentityLoginState, err error) {
	err = s.pool.QueryRow(ctx, `DELETE FROM plume.ai_identity_login_states WHERE workspace_id=$1 AND state_hash=$2 AND browser_hash=$3 AND expires_at>now() RETURNING state_hash,workspace_id,settings_revision,browser_hash,nonce,verifier,expires_at`, ws, stateHash, browserHash).Scan(&out.StateHash, &out.WorkspaceID, &out.SettingsRevision, &out.BrowserHash, &out.Nonce, &out.Verifier, &out.ExpiresAt)
	return out, storeError(err)
}

func (s *Store) CompleteIdentityLogin(ctx context.Context, ws, revision int64, issuer, subject, sessionHash string, expires time.Time) (out User, err error) {
	if !identityText(subject, 255) || !identityTokenHash.MatchString(sessionHash) {
		return out, ErrInvalid
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		c, e := identityConfig(ctx, tx, ws, true)
		if e != nil {
			return e
		}
		if !c.Enabled || c.Revision != revision || c.Issuer != issuer {
			return ErrConflict
		}
		e = tx.QueryRow(ctx, `SELECT u.id,u.username FROM plume.ai_identity_users i JOIN plume.users u ON u.id=i.user_id JOIN plume.memberships m ON m.user_id=u.id AND m.workspace_id=i.workspace_id WHERE i.workspace_id=$1 AND i.external_id=$2 AND i.active`, ws, subject).Scan(&out.ID, &out.Username)
		if e != nil {
			return storeError(e)
		}
		if _, e := tx.Exec(ctx, `INSERT INTO plume.sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, sessionHash, out.ID, expires); e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, out.ID, "identity.oidc_login", fmt.Sprint(out.ID), map[string]int64{"userId": out.ID})
	})
	return
}
