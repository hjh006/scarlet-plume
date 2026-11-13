package store

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/classificationml"
	"github.com/jackc/pgx/v5"
)

type DataSecurityScope struct {
	SourceID   int64     `json:"sourceId,omitempty"`
	SubjectRef string    `json:"subjectRef,omitempty"`
	DeviceRef  string    `json:"deviceRef,omitempty"`
	Channel    string    `json:"channel,omitempty"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
}
type DataSecurityEvent struct {
	OperationsSecurityEvent
	SourceCurrent bool `json:"sourceCurrent"`
}
type DataSecurityWindow struct {
	Scope     DataSecurityScope   `json:"scope"`
	Items     []DataSecurityEvent `json:"items"`
	Total     int64               `json:"total"`
	Truncated bool                `json:"truncated"`
}
type DataSecurityChain struct {
	EventIDs   []int64  `json:"eventIds"`
	SourceID   int64    `json:"sourceId"`
	SubjectRef string   `json:"subjectRef,omitempty"`
	DeviceRef  string   `json:"deviceRef,omitempty"`
	Channels   []string `json:"channels"`
	Summary    string   `json:"summary"`
}
type DataSecuritySnapshot struct {
	Window        DataSecurityWindow  `json:"window"`
	Chains        []DataSecurityChain `json:"chains"`
	Coverage      []string            `json:"coverage"`
	EngineVersion string              `json:"engineVersion"`
}
type DataSecurityCase struct {
	ID             int64                  `json:"id"`
	Title          string                 `json:"title"`
	Scope          DataSecurityScope      `json:"scope"`
	Snapshot       DataSecuritySnapshot   `json:"snapshot"`
	SnapshotDigest string                 `json:"snapshotDigest"`
	Actor          string                 `json:"actor"`
	CreatedAt      time.Time              `json:"createdAt"`
	Notes          []DataSecurityCaseNote `json:"notes"`
}
type DataSecurityCaseNote struct {
	ID        int64          `json:"id"`
	Kind      string         `json:"kind"`
	Snapshot  map[string]any `json:"snapshot"`
	Actor     string         `json:"actor"`
	CreatedAt time.Time      `json:"createdAt"`
}

func validateDataSecurityScope(s DataSecurityScope) bool {
	if s.SourceID < 0 || s.From.IsZero() || !s.To.After(s.From) || s.To.Sub(s.From) > 31*24*time.Hour || s.To.After(time.Now().Add(5*time.Minute)) {
		return false
	}
	for _, v := range []string{s.SubjectRef, s.DeviceRef} {
		if v != "" && !digestPattern.MatchString(v) {
			return false
		}
	}
	return s.Channel == "" || slices.Contains([]string{"web_upload", "mail", "im", "usb", "print", "clipboard", "file_copy", "database", "ai_prompt", "file_change"}, s.Channel)
}
func readDataSecurityWindow(ctx context.Context, tx pgx.Tx, ws int64, scope DataSecurityScope) (DataSecurityWindow, error) {
	out := DataSecurityWindow{Scope: scope, Items: []DataSecurityEvent{}}
	if !validateDataSecurityScope(scope) {
		return out, ErrInvalid
	}
	const filter = ` e.workspace_id=$1 AND e.metadata->'dataContext' IS NOT NULL AND e.metadata->'dataContext'<>'null'::jsonb AND e.observed_at >= $2 AND e.observed_at <= $3 AND ($4::bigint=0 OR e.source_id=$4) AND ($5='' OR e.metadata->'dataContext'->>'subjectRef'=$5) AND ($6='' OR e.metadata->'dataContext'->>'deviceRef'=$6) AND ($7='' OR e.metadata->'dataContext'->>'channel'=$7)`
	args := []any{ws, scope.From, scope.To, scope.SourceID, scope.SubjectRef, scope.DeviceRef, scope.Channel}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM plume.operations_security_events e WHERE`+filter, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.source_id,s.name,COALESCE(e.check_id,0),COALESCE(c.status,''),e.asset_id,e.metadata,s.revoked_at IS NULL AND EXISTS(SELECT 1 FROM plume.memberships m WHERE m.workspace_id=s.workspace_id AND m.user_id=s.actor_id AND m.role='owner') FROM plume.operations_security_events e JOIN plume.operations_sources s ON s.id=e.source_id LEFT JOIN plume.checks c ON c.id=e.check_id AND c.workspace_id=e.workspace_id WHERE`+filter+` ORDER BY e.observed_at,e.id LIMIT 200`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e DataSecurityEvent
		var assetID int64
		if err = rows.Scan(&e.ID, &e.SourceID, &e.Source, &e.CheckID, &e.CheckStatus, &assetID, &e.SecurityEventInput, &e.SourceCurrent); err != nil {
			return out, err
		}
		e.AssetID = assetID
		out.Items = append(out.Items, e)
	}
	out.Truncated = out.Total > int64(len(out.Items))
	return out, rows.Err()
}
func (s *Store) DataSecurityEvents(ctx context.Context, ws, actor int64, scope DataSecurityScope) (DataSecurityWindow, error) {
	var out DataSecurityWindow
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var err error
		out, err = readDataSecurityWindow(ctx, tx, ws, scope)
		return err
	})
	return out, storeError(err)
}
func dataSecuritySnapshot(w DataSecurityWindow) DataSecuritySnapshot {
	out := DataSecuritySnapshot{Window: w, Chains: []DataSecurityChain{}, EngineVersion: "source-bound-data-events-v1", Coverage: []string{"仅使用已授权来源提交的脱敏投影；原始文件、SQL、用户名、路径与提示词不进入此报告。", "activity为活动审计，不表示风险命中或风险已消除；风险观察与cleared分别保留。", "同一来源内的主体或设备引用支持事件关联，不证明真实人员身份、恶意意图或跨来源同一身份。", "源端声称阻断与实际执行链路分别核验；未接入通道或来源沉默不表示没有泄漏。"}}
	if w.Truncated {
		out.Coverage = append(out.Coverage, "超过200条事件预算，超出部分未纳入关联。")
	}
	indexes := map[string]int{}
	for _, e := range w.Items {
		c := e.DataContext
		if c == nil {
			continue
		}
		identity := c.SubjectRef
		identityKind := "subject:"
		if identity == "" {
			identity = c.DeviceRef
			identityKind = "device:"
		}
		if identity == "" {
			identity = fmt.Sprint("event:", e.ID)
		}
		key := fmt.Sprint(e.SourceID, ":", identityKind, identity)
		i, ok := indexes[key]
		if !ok {
			i = len(out.Chains)
			indexes[key] = i
			out.Chains = append(out.Chains, DataSecurityChain{SourceID: e.SourceID, SubjectRef: c.SubjectRef, DeviceRef: c.DeviceRef, EventIDs: []int64{}, Channels: []string{}})
		}
		chain := &out.Chains[i]
		if chain.DeviceRef != c.DeviceRef {
			chain.DeviceRef = ""
		}
		chain.EventIDs = append(chain.EventIDs, e.ID)
		if !slices.Contains(chain.Channels, c.Channel) {
			chain.Channels = append(chain.Channels, c.Channel)
		}
		if !e.SourceCurrent {
			out.Coverage = append(out.Coverage, fmt.Sprintf("事件#%d的来源已撤销或维护人权限失效，仅保留历史声明。", e.ID))
		}
		if c.Capture != "complete" {
			out.Coverage = append(out.Coverage, fmt.Sprintf("事件#%d缺少完整投影，未观察维度保持未知。", e.ID))
		}
	}
	for i := range out.Chains {
		c := &out.Chains[i]
		sort.Strings(c.Channels)
		c.Summary = fmt.Sprintf("同来源引用关联%d条事件、%d个已声明通道；按下列事件证据核验，关联不自动形成泄漏或人员结论。", len(c.EventIDs), len(c.Channels))
	}
	return out
}
func (s *Store) CreateDataSecurityCase(ctx context.Context, ws, actor int64, title string, scope DataSecurityScope) (DataSecurityCase, error) {
	var out DataSecurityCase
	if !validCheckText(title, 120) || !validateDataSecurityScope(scope) {
		return out, ErrInvalid
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		if _, err := tx.Exec(ctx, "SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.data_security_cases WHERE workspace_id=$1", ws).Scan(&n); err != nil {
			return err
		}
		if n >= 500 {
			return ErrRuntimeLimit
		}
		window, err := readDataSecurityWindow(ctx, tx, ws, scope)
		if err != nil {
			return err
		}
		if len(window.Items) == 0 {
			return ErrEvidenceRequired
		}
		out.Title, out.Scope, out.Snapshot = title, scope, dataSecuritySnapshot(window)
		out.SnapshotDigest = classificationml.Digest(out.Snapshot)
		return tx.QueryRow(ctx, `INSERT INTO plume.data_security_cases(workspace_id,actor_id,title,scope,snapshot,snapshot_digest)VALUES($1,$2,$3,$4,$5,$6)RETURNING id,created_at,(SELECT username FROM plume.users WHERE id=$2)`, ws, actor, title, scope, out.Snapshot, out.SnapshotDigest).Scan(&out.ID, &out.CreatedAt, &out.Actor)
	})
	out.Notes = []DataSecurityCaseNote{}
	return out, storeError(err)
}

const dataSecurityCaseColumns = `c.id,c.title,c.scope,c.snapshot,c.snapshot_digest,u.username,c.created_at`

func scanDataSecurityCase(row pgx.Row) (DataSecurityCase, error) {
	var c DataSecurityCase
	err := row.Scan(&c.ID, &c.Title, &c.Scope, &c.Snapshot, &c.SnapshotDigest, &c.Actor, &c.CreatedAt)
	c.Notes = []DataSecurityCaseNote{}
	return c, err
}

type DataSecurityCaseSummary struct {
	ID             int64             `json:"id"`
	Title          string            `json:"title"`
	Scope          DataSecurityScope `json:"scope"`
	SnapshotDigest string            `json:"snapshotDigest"`
	Actor          string            `json:"actor"`
	CreatedAt      time.Time         `json:"createdAt"`
	EventCount     int               `json:"eventCount"`
}

func (s *Store) DataSecurityCases(ctx context.Context, ws, actor int64) ([]DataSecurityCaseSummary, error) {
	out := []DataSecurityCaseSummary{}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT c.id,c.title,c.scope,c.snapshot_digest,u.username,c.created_at,jsonb_array_length(c.snapshot#>'{window,items}') FROM plume.data_security_cases c JOIN plume.users u ON u.id=c.actor_id WHERE c.workspace_id=$1 ORDER BY c.id DESC LIMIT 100`, ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c DataSecurityCaseSummary
			if err = rows.Scan(&c.ID, &c.Title, &c.Scope, &c.SnapshotDigest, &c.Actor, &c.CreatedAt, &c.EventCount); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, storeError(err)
}
func (s *Store) DataSecurityCase(ctx context.Context, ws, actor, id int64) (DataSecurityCase, error) {
	var out DataSecurityCase
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := checkRole(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		var err error
		out, err = scanDataSecurityCase(tx.QueryRow(ctx, `SELECT `+dataSecurityCaseColumns+` FROM plume.data_security_cases c JOIN plume.users u ON u.id=c.actor_id WHERE c.workspace_id=$1 AND c.id=$2`, ws, id))
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.id,n.kind,n.snapshot,u.username,n.created_at FROM plume.data_security_case_notes n JOIN plume.users u ON u.id=n.actor_id WHERE n.workspace_id=$1 AND n.case_id=$2 ORDER BY n.id LIMIT 200`, ws, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n DataSecurityCaseNote
			if err = rows.Scan(&n.ID, &n.Kind, &n.Snapshot, &n.Actor, &n.CreatedAt); err != nil {
				return err
			}
			out.Notes = append(out.Notes, n)
		}
		return rows.Err()
	})
	return out, storeError(err)
}
func (s *Store) AddDataSecurityCaseFeedback(ctx context.Context, ws, actor, id int64, decision, note string) error {
	if !slices.Contains([]string{"needs_investigation", "expected_activity", "false_positive"}, decision) || !validCheckText(note, 600) {
		return ErrInvalid
	}
	return s.addDataSecurityCaseNote(ctx, ws, actor, id, "feedback", map[string]any{"decision": decision, "note": note, "effect": "human_feedback_only_no_check_closure"})
}
func (s *Store) addDataSecurityCaseNote(ctx context.Context, ws, actor, id int64, kind string, snapshot any) error {
	return storeError(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		role, err := checkRole(ctx, tx, ws, actor, true)
		if err != nil {
			return err
		}
		if !checkEditor(role) {
			return ErrForbidden
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM plume.data_security_cases WHERE workspace_id=$1 AND id=$2)", ws, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := dataSecurityCaseNoteSlot(ctx, tx, ws, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.data_security_case_notes(workspace_id,case_id,actor_id,kind,snapshot)VALUES($1,$2,$3,$4,$5)`, ws, id, actor, kind, snapshot)
		return err
	}))
}

func dataSecurityCaseNoteSlot(ctx context.Context, tx pgx.Tx, ws, id int64) error {
	var locked int64
	if err := tx.QueryRow(ctx, "SELECT id FROM plume.data_security_cases WHERE workspace_id=$1 AND id=$2 FOR UPDATE", ws, id).Scan(&locked); err != nil {
		return storeError(err)
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM plume.data_security_case_notes WHERE workspace_id=$1 AND case_id=$2", ws, id).Scan(&count); err != nil {
		return err
	}
	if count >= 200 {
		return ErrRuntimeLimit
	}
	return nil
}
