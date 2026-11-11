package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Detection includes its adopted policy and observed denominator. The parent
// report transaction freezes these facts together with evidence and review state.
func readReportRuntimeFindings(ctx context.Context, tx pgx.Tx, report *ComplianceReport) error {
	for i := range report.Checks {
		finding, e := runtimeFindingForCheck(ctx, tx, report.Checks[i].Check)
		if e != nil {
			return e
		}
		if finding != nil {
			snapshot := finding.RuntimeFinding
			report.Checks[i].RuntimeFinding = &snapshot
		}
	}
	return nil
}
