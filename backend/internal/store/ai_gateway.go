package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var gatewayRuntimeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,79}$`)
var gatewayID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,127}$`)
var gatewayHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var gatewayReference = regexp.MustCompile(`^env:SCARLET_GATEWAY_[A-Z0-9_]{1,96}$`)

func gatewayText(s string, max int) bool {
	return utf8.ValidString(s) && strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}

func ValidateGatewayConfig(c GatewayConfig) error {
	if c.Enabled && !gatewayHash.MatchString(c.CatalogDigest) {
		return ErrInvalid
	}
	if c.CatalogDigest != "" && !gatewayHash.MatchString(c.CatalogDigest) {
		return ErrInvalid
	}
	if c.Revision < 0 || len(c.Applications) > 200 || len(c.Providers) > 200 {
		return ErrInvalid
	}
	if err := gatewayValidateExtensions(c); err != nil {
		return err
	}
	providers, apps := map[string]bool{}, map[string]bool{}
	models := map[string]bool{}
	for _, p := range c.Providers {
		if !gatewayID.MatchString(p.ID) || providers[p.ID] || !gatewayText(p.Name, 128) || !gatewayRuntimeID.MatchString(p.Provider) || !gatewayRuntimeID.MatchString(p.ModelID) || !gatewayText(p.UpstreamModel, 255) || !gatewayRuntimeID.MatchString(p.Region) || !slices.Contains([]string{"json", "openai_chat"}, p.ResponseFormat) || !gatewayReference.MatchString(p.EndpointRef) || !gatewayReference.MatchString(p.SecretRef) || p.InputMicrosPerMillion < 0 || p.InputMicrosPerMillion > 1_000_000_000_000 || p.OutputMicrosPerMillion < 0 || p.OutputMicrosPerMillion > 1_000_000_000_000 {
			return ErrInvalid
		}
		providers[p.ID] = true
		// Each catalog model is one explicitly governed route; fallback uses separate catalog models.
		if p.Enabled {
			if models[p.ModelID] {
				return ErrInvalid
			}
			models[p.ModelID] = true
		}
	}
	for _, a := range c.Applications {
		if !gatewayRuntimeID.MatchString(a.ID) || apps[a.ID] || !gatewayText(a.Name, 128) || !gatewayRuntimeID.MatchString(a.PrincipalID) || a.BudgetMicros < 0 || a.BudgetMicros > 9_000_000_000_000_000 || a.RequestsPerMinute < 0 || a.RequestsPerMinute > 1_000_000 || a.TokensPerMinute < 0 || a.TokensPerMinute > 1_000_000_000 || len(a.ModelIDs) > 200 || len(a.ToolIDs) > 200 {
			return ErrInvalid
		}
		apps[a.ID] = true
		for _, ids := range [][]string{a.ModelIDs, a.ToolIDs} {
			seen := map[string]bool{}
			for _, id := range ids {
				if !gatewayRuntimeID.MatchString(id) || seen[id] {
					return ErrInvalid
				}
				seen[id] = true
			}
		}
		if a.Enabled {
			for _, m := range a.ModelIDs {
				if !models[m] {
					return ErrInvalid
				}
			}
		}
	}
	return nil
}

func gatewayConfig(ctx context.Context, tx pgx.Tx, ws int64, lock string) (c GatewayConfig, err error) {
	c.Applications = []GatewayApplication{}
	c.Providers = []GatewayProvider{}
	var raw []byte
	q := `SELECT revision,configuration FROM plume.ai_gateway_settings WHERE workspace_id=$1` + lock
	err = tx.QueryRow(ctx, q, ws).Scan(&c.Revision, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return
	}
	revision := c.Revision
	err = json.Unmarshal(raw, &c)
	c.Revision = revision
	return
}

func gatewayRole(ctx context.Context, tx pgx.Tx, ws, actor int64, owner bool) error {
	role, err := checkRole(ctx, tx, ws, actor, true)
	if err != nil {
		return err
	}
	if owner && role != "owner" || !owner && !checkEditor(role) {
		return ErrForbidden
	}
	return nil
}

func (s *Store) GatewayOverview(ctx context.Context, ws, actor int64) (out GatewayOverview, err error) {
	out.Usage = []GatewayUsage{}
	out.Credentials = []GatewayCredential{}
	out.Nodes = []GatewayNode{}
	out.RouteHealth = []GatewayRouteHealth{}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, e := checkRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		var e error
		out.Config, e = gatewayConfig(ctx, tx, ws, "")
		if e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT application_id,reserved_micros,spent_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 ORDER BY application_id`, ws)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v GatewayUsage
			if e = rows.Scan(&v.ApplicationID, &v.ReservedMicros, &v.SpentMicros); e != nil {
				rows.Close()
				return e
			}
			out.Usage = append(out.Usage, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT id,application_id,name,prefix,enabled,created_at FROM plume.ai_gateway_credentials WHERE workspace_id=$1 ORDER BY id DESC LIMIT 1000`, ws)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v GatewayCredential
			if e = rows.Scan(&v.ID, &v.ApplicationID, &v.Name, &v.Prefix, &v.Enabled, &v.CreatedAt); e != nil {
				rows.Close()
				return e
			}
			out.Credentials = append(out.Credentials, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT id,CASE WHEN last_seen_at < now()-interval '90 seconds' THEN 'stale' ELSE status END,failures,last_seen_at,capabilities,version FROM plume.ai_gateway_nodes WHERE workspace_id=$1 ORDER BY id LIMIT 1000`, ws)
		if e != nil {
			return e
		}
		for rows.Next() {
			var v GatewayNode
			if e = rows.Scan(&v.ID, &v.Status, &v.Failures, &v.LastSeenAt, &v.Capabilities, &v.Version); e != nil {
				rows.Close()
				return e
			}
			out.Nodes = append(out.Nodes, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = tx.Query(ctx, `SELECT provider_id,failures,open_until,last_outcome FROM plume.ai_gateway_route_health WHERE workspace_id=$1 ORDER BY provider_id`, ws)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v GatewayRouteHealth
			if e = rows.Scan(&v.ProviderID, &v.Failures, &v.OpenUntil, &v.LastOutcome); e != nil {
				return e
			}
			out.RouteHealth = append(out.RouteHealth, v)
		}
		return rows.Err()
	})
	return
}

func (s *Store) SaveGatewayConfig(ctx context.Context, ws, actor, expected int64, in GatewayConfig) (out GatewayConfig, err error) {
	if expected < 0 {
		return out, ErrInvalid
	}
	in.Revision = expected
	if err = ValidateGatewayConfig(in); err != nil {
		return
	}
	if in.Applications == nil {
		in.Applications = []GatewayApplication{}
	}
	if in.Providers == nil {
		in.Providers = []GatewayProvider{}
	}
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, true); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO plume.ai_gateway_settings(workspace_id) VALUES($1) ON CONFLICT DO NOTHING`, ws); e != nil {
			return e
		}
		old, e := gatewayConfig(ctx, tx, ws, " FOR UPDATE")
		if e != nil {
			return e
		}
		if old.Revision != expected {
			return ErrConflict
		}
		// This row lock is shared by authorization, settlement and configuration writes across nodes.
		for _, a := range in.Applications {
			if _, e = tx.Exec(ctx, `INSERT INTO plume.ai_gateway_accounts(workspace_id,application_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, ws, a.ID); e != nil {
				return e
			}
			var used int64
			if e = tx.QueryRow(ctx, `SELECT reserved_micros+spent_micros FROM plume.ai_gateway_accounts WHERE workspace_id=$1 AND application_id=$2 FOR UPDATE`, ws, a.ID).Scan(&used); e != nil {
				return e
			}
			if a.BudgetMicros < used {
				return ErrGatewayBudget
			}
		}
		in.Revision = expected + 1
		raw, e := json.Marshal(in)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE plume.ai_gateway_settings SET revision=$2,configuration=$3,updated_at=now() WHERE workspace_id=$1`, ws, in.Revision, raw); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM plume.ai_gateway_cache WHERE workspace_id=$1`, ws); e != nil {
			return e
		}
		if e = identityAudit(ctx, tx, ws, actor, "gateway.config_saved", fmt.Sprint(in.Revision), in); e != nil {
			return e
		}
		out = in
		return nil
	})
	return
}

func (s *Store) CreateGatewayCredential(ctx context.Context, ws, actor int64, app, name string) (out GatewayCredential, token string, err error) {
	if !gatewayRuntimeID.MatchString(app) || !gatewayText(name, 128) {
		return out, "", ErrInvalid
	}
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return
	}
	token = "sgw_" + hex.EncodeToString(b[:])
	hash := sha256.Sum256([]byte(token))
	err = pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		cfg, e := gatewayConfig(ctx, tx, ws, " FOR UPDATE")
		if e != nil {
			return e
		}
		if !slices.ContainsFunc(cfg.Applications, func(a GatewayApplication) bool { return a.ID == app }) {
			return ErrNotFound
		}
		var count int
		if e = tx.QueryRow(ctx, `SELECT count(*) FROM plume.ai_gateway_credentials WHERE workspace_id=$1 AND enabled`, ws).Scan(&count); e != nil {
			return e
		}
		if count >= 1000 {
			return ErrConflict
		}
		e = tx.QueryRow(ctx, `INSERT INTO plume.ai_gateway_credentials(workspace_id,application_id,name,prefix,token_hash) VALUES($1,$2,$3,$4,$5) RETURNING id,application_id,name,prefix,enabled,created_at`, ws, app, name, token[:12], hex.EncodeToString(hash[:])).Scan(&out.ID, &out.ApplicationID, &out.Name, &out.Prefix, &out.Enabled, &out.CreatedAt)
		if e != nil {
			return e
		}
		return identityAudit(ctx, tx, ws, actor, "gateway.credential_created", fmt.Sprint(out.ID), out)
	})
	if err != nil {
		token = ""
	}
	return
}

func (s *Store) RevokeGatewayCredential(ctx context.Context, ws, actor, id int64) error {
	if id <= 0 {
		return ErrInvalid
	}
	return pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		if e := gatewayRole(ctx, tx, ws, actor, false); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE plume.ai_gateway_credentials SET enabled=false WHERE workspace_id=$1 AND id=$2`, ws, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return identityAudit(ctx, tx, ws, actor, "gateway.credential_revoked", fmt.Sprint(id), map[string]int64{"credentialId": id})
	})
}

func gatewayRandomID() (string, error) {
	var b [16]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}

func gatewayNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	e := tx.QueryRow(ctx, `SELECT date_trunc('minute',clock_timestamp())`).Scan(&now)
	return now, e
}
