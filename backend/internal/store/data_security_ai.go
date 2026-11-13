package store

import (
	"context"
	"github.com/hjh006/scarlet-plume/backend/internal/classificationai"
	"github.com/jackc/pgx/v5"
)

func DataSecurityCaseFacts(c DataSecurityCase) []classificationai.EventFact {
	out := []classificationai.EventFact{}
	chains := map[int64]int{}
	for i, chain := range c.Snapshot.Chains {
		for _, id := range chain.EventIDs {
			chains[id] = i + 1
		}
	}
	for _, e := range c.Snapshot.Window.Items {
		d := e.DataContext
		if d == nil {
			continue
		}
		out = append(out, classificationai.EventFact{EventID: e.ID, Chain: chains[e.ID], Kind: d.Kind, Channel: d.Channel, Rule: e.Rule, Severity: e.Severity, Enforcement: d.Enforcement, Categories: d.Categories, SourceCurrent: e.SourceCurrent, Capture: d.Capture})
	}
	return out
}
func (s *Store) SaveDataSecurityAIReference(ctx context.Context, ws, actor, id int64, digest string, profile ClassificationAIProfile, result classificationai.EventAssessment) error {
	return storeError(pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := classificationAIEditor(ctx, tx, ws, actor, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT id FROM plume.workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
			return err
		}
		settings, err := readClassificationEngineSettings(ctx, tx, ws)
		if err != nil {
			return err
		}
		if settings.Mode != "hybrid" {
			return ErrForbidden
		}
		current, err := scanClassificationAIProfile(tx.QueryRow(ctx, "SELECT "+classificationAIProfileColumns+" FROM plume.classification_ai_profiles WHERE workspace_id=$1 AND id=$2", ws, profile.ID), false)
		if err != nil {
			return err
		}
		if current.Revision != profile.Revision || !current.Enabled || !current.AllowMetadata {
			return ErrConflict
		}
		c, err := scanDataSecurityCase(tx.QueryRow(ctx, `SELECT `+dataSecurityCaseColumns+` FROM plume.data_security_cases c JOIN plume.users u ON u.id=c.actor_id WHERE c.workspace_id=$1 AND c.id=$2`, ws, id))
		if err != nil {
			return err
		}
		if c.SnapshotDigest != digest || !classificationai.ValidateEventAssessment(result, DataSecurityCaseFacts(c)) {
			return ErrInvalid
		}
		if err := dataSecurityCaseNoteSlot(ctx, tx, ws, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO plume.data_security_case_notes(workspace_id,case_id,actor_id,kind,snapshot)VALUES($1,$2,$3,'ai_reference',$4)`, ws, id, actor, map[string]any{"profileId": profile.ID, "profileRevision": profile.Revision, "model": profile.Model, "protocol": profile.Protocol, "caseDigest": digest, "result": result, "adopted": false})
		return err
	}))
}
