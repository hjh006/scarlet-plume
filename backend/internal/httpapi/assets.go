package httpapi

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

func (s *server) overview(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	counts, err := s.store.OverviewCounts(r.Context(), workspace.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	assets, err := s.store.ListRecentAssets(r.Context(), workspace.ID, 6)
	if err != nil {
		storeFailure(w, err)
		return
	}
	runs, err := s.store.ListImports(r.Context(), workspace.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	sources, err := s.store.OverviewSources(r.Context(), workspace.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	findings, err := s.store.ListRuntimeFindings(r.Context(), workspace.ID)
	if err != nil {
		storeFailure(w, err)
		return
	}
	slices.SortStableFunc(findings, func(a, b store.RuntimeFinding) int { return b.LastSeen.Compare(a.LastSeen) })
	active := 0
	for _, item := range integrations {
		if item.Status == "available" {
			active++
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"workspace":   workspace,
		"generatedAt": time.Now().UTC(),
		"counts": struct {
			store.OverviewCounts
			Integrations int `json:"integrations"`
		}{OverviewCounts: counts, Integrations: active},
		"recentAssets":      assets,
		"recentImports":     runs[:min(len(runs), 20)],
		"recentFindings":    findings[:min(len(findings), 5)],
		"collectionSources": sources,
	})
}

func (s *server) listAssets(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	var cursor int64
	if raw := r.URL.Query().Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			fail(w, http.StatusBadRequest, "分页参数无效。")
			return
		}
		cursor = value
	}
	const limit = 50
	assets, err := s.store.ListAssets(r.Context(), workspace.ID, cursor, limit+1)
	if err != nil {
		storeFailure(w, err)
		return
	}
	var nextCursor *int64
	if len(assets) > limit {
		assets = assets[:limit]
		nextCursor = &assets[limit-1].ID
	}
	respond(w, http.StatusOK, map[string]any{"items": assets, "nextCursor": nextCursor})
}

func (s *server) createAsset(w http.ResponseWriter, r *http.Request, _ store.User, workspace store.Workspace) {
	var input struct {
		Name      string `json:"name"`
		System    string `json:"system"`
		OwnerTeam string `json:"ownerTeam"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	name, system, team := strings.TrimSpace(input.Name), strings.TrimSpace(input.System), strings.TrimSpace(input.OwnerTeam)
	if !validText(name, 300) || !validText(system, 80) || (team != "" && !validText(team, 80)) {
		fail(w, http.StatusBadRequest, "请检查资产名称、来源系统和责任团队。")
		return
	}
	asset, err := s.store.CreateAsset(r.Context(), workspace.ID, name, system, team)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusCreated, asset)
}

func (s *server) listIntegrations(w http.ResponseWriter, r *http.Request, _ store.User, _ store.Workspace) {
	respond(w, http.StatusOK, map[string]any{"items": integrations})
}

type manifest struct {
	Version  int                    `json:"version"`
	Provider string                 `json:"provider"`
	Source   string                 `json:"source"`
	BatchID  string                 `json:"batchId"`
	Assets   []store.CollectedAsset `json:"assets"`
}

var batchIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func (m *manifest) digest() (string, bool) {
	if m.Version != 1 || !availableProvider(m.Provider) || !validText(m.Source, 80) ||
		m.Source != strings.TrimSpace(m.Source) || !batchIDPattern.MatchString(m.BatchID) ||
		m.Assets == nil || len(m.Assets) > 1000 {
		return "", false
	}
	slices.SortFunc(m.Assets, func(a, b store.CollectedAsset) int { return cmp.Compare(a.SourceRef, b.SourceRef) })
	for i, item := range m.Assets {
		if !validText(item.SourceRef, 500) || !validText(item.Name, 300) ||
			(i > 0 && item.SourceRef == m.Assets[i-1].SourceRef) {
			return "", false
		}
	}
	normalized, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(normalized)
	return hex.EncodeToString(sum[:]), true
}

func (s *server) importAssets(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var input manifest
	if !readJSON(w, r, &input) {
		return
	}
	digest, valid := input.digest()
	if !valid {
		fail(w, http.StatusBadRequest, "清单版本、来源、批次或资产标识无效。")
		return
	}
	run, err := s.store.ImportAssets(r.Context(), workspace.ID, user.ID, input.Provider,
		input.Source, input.BatchID, digest, input.Assets)
	if err != nil {
		storeFailure(w, err)
		return
	}
	respond(w, http.StatusOK, run)
}
