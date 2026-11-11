package store

import "testing"

func TestOperationsTeamAssetCountsUseTeamRatherThanAssetName(t *testing.T) {
	f := newOperationsFixture(t)
	asset, err := f.repo.CreateAsset(f.ctx, f.workspace.ID, "Customers", "fixture", "Security")
	f.must(t, err)
	_, err = f.repo.CreateAsset(f.ctx, f.workspace.ID, "Security", "fixture", "Other")
	f.must(t, err)
	_, err = f.repo.CreateAsset(f.ctx, f.workspace.ID, "Unassigned asset", "fixture", "")
	f.must(t, err)
	foreign, err := f.repo.Membership(f.ctx, f.foreign.ID, f.slug+"-foreign")
	f.must(t, err)
	_, err = f.repo.CreateAsset(f.ctx, foreign.ID, "Foreign asset", "fixture", "Security")
	f.must(t, err)
	_, err = f.repo.CreateCheck(f.ctx, f.workspace.ID, f.owner.ID, CheckInput{AssetID: asset.ID, RequirementCode: "G03", OwnerTeam: "Responder", Note: "Synthetic team attribution"})
	f.must(t, err)
	overview, err := f.repo.OperationsOverview(f.ctx, f.workspace.ID, f.viewer.ID)
	f.must(t, err)
	teams := map[string]OperationsTeam{}
	var total int64
	for _, team := range overview.Teams {
		teams[team.Name] = team
		total += team.Assets
	}
	if total != 3 || teams["Security"].Assets != 1 || teams["Other"].Assets != 1 || teams["未分配"].Assets != 1 {
		t.Fatalf("team ownership or workspace boundary lost: %+v", overview.Teams)
	}
	if teams["Responder"].Checks != 1 || teams["Responder"].OpenChecks != 1 || teams["Responder"].Assets != 0 {
		t.Fatalf("check team should not reassign asset ownership: %+v", teams["Responder"])
	}
}
