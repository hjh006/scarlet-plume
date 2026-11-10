package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"github.com/hjh006/scarlet-plume/backend/internal/store"
)

type feishuManifest struct {
	Version      int       `json:"version"`
	TargetDigest string    `json:"targetDigest"`
	ObservedAt   time.Time `json:"observedAt"`
	Present      *bool     `json:"present"`
	Permission   string    `json:"permission,omitempty"`
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (m feishuManifest) evidence(now time.Time) (store.EvidenceInput, error) {
	if m.Version != 1 || !sha256Hex.MatchString(m.TargetDigest) ||
		m.ObservedAt.IsZero() || m.ObservedAt.After(now) || m.Present == nil {
		return store.EvidenceInput{}, store.ErrInvalid
	}
	summary := "上传清单未发现匹配的直接协作者 open_id 条目。"
	if *m.Present {
		switch m.Permission {
		case "view", "edit", "full_access":
		default:
			return store.EvidenceInput{}, store.ErrInvalid
		}
		summary = "上传清单发现匹配的直接协作者 open_id 条目，权限为 " + m.Permission + "。"
	} else if m.Permission != "" {
		return store.EvidenceInput{}, store.ErrInvalid
	}
	m.ObservedAt = m.ObservedAt.UTC()
	data, err := json.Marshal(m)
	if err != nil {
		return store.EvidenceInput{}, store.ErrInvalid
	}
	digest := sha256.Sum256(data)
	snapshot := hex.EncodeToString(digest[:])
	return store.EvidenceInput{
		SourceSystem: "飞书权限清单（客户侧上传）",
		SourceRecord: snapshot,
		Summary:      summary,
		ObservedAt:   m.ObservedAt,
		Feishu: &store.FeishuObservation{
			TargetDigest: m.TargetDigest, SnapshotDigest: snapshot,
			MemberPresent: *m.Present, Permission: m.Permission,
		},
	}, nil
}

func (s *server) addFeishuEvidence(w http.ResponseWriter, r *http.Request, user store.User, workspace store.Workspace) {
	var manifest feishuManifest
	id, ok := checkID(w, r)
	if !ok || !readJSON(w, r, &manifest) {
		return
	}
	input, err := manifest.evidence(time.Now())
	if err != nil {
		checkFailure(w, err)
		return
	}
	checkMutationResult(w, s.store.AddEvidence(r.Context(), workspace.ID, user.ID, id, input))
}
