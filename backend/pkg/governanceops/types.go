// Package governanceops models evidence-backed governance work. A reviewed record
// is a human governance decision, not proof of source execution or legal compliance.
package governanceops

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid governance operation")
var ErrEvidence = errors.New("governance operation requires evidence")
var ErrTransition = errors.New("invalid governance transition")

const Boundary = "办理完成表示独立人工复核已留痕；引用证据保留原始来源属性，不自动证明源端删除、同意传播、监管收件、签名有效或法律合规。工作日期限须核对适用的版本化日历。请勿填入原始个人标识、密码或密钥。"

var Kinds = []string{"rights_request", "consent", "vendor", "audit", "incident", "labeling"}
var Statuses = []string{"draft", "in_progress", "in_review", "needs_work", "completed", "withdrawal_pending", "withdrawn", "cancelled"}
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Receipt struct {
	Purpose    string `json:"purpose"`
	EvidenceID int64  `json:"evidenceId"`
	System     string `json:"system,omitempty"`
}

type Input struct {
	Revision            int64     `json:"revision,omitempty"`
	Kind                string    `json:"kind"`
	Title               string    `json:"title"`
	Owner               string    `json:"owner"`
	ActivityID          int64     `json:"activityId,omitempty"`
	CheckIDs            []int64   `json:"checkIds"`
	EvidenceIDs         []int64   `json:"evidenceIds"`
	RegulationIDs       []string  `json:"regulationIds"`
	Applicability       string    `json:"applicability"`
	ApplicabilityReason string    `json:"applicabilityReason"`
	Details             Details   `json:"details"`
	Receipts            []Receipt `json:"receipts"`
}

// Details has a strict per-kind JSON whitelist. All fields also undergo length,
// enum and timestamp validation; unknown fields are never stored as metadata.
type Details struct {
	RequestType           string     `json:"requestType,omitempty"`
	SubjectRef            string     `json:"subjectRef,omitempty"`
	ReceivedAt            *time.Time `json:"receivedAt,omitempty"`
	IdentityStatus        string     `json:"identityStatus,omitempty"`
	IdentityEvidenceID    int64      `json:"identityEvidenceId,omitempty"`
	ResponseDueAt         *time.Time `json:"responseDueAt,omitempty"`
	Systems               []string   `json:"systems,omitempty"`
	Outcome               string     `json:"outcome,omitempty"`
	Resolution            string     `json:"resolution,omitempty"`
	RetentionException    string     `json:"retentionException,omitempty"`
	Purpose               string     `json:"purpose,omitempty"`
	LawfulBasis           string     `json:"lawfulBasis,omitempty"`
	NoticeVersion         string     `json:"noticeVersion,omitempty"`
	GrantedAt             *time.Time `json:"grantedAt,omitempty"`
	WithdrawalAt          *time.Time `json:"withdrawalAt,omitempty"`
	Name                  string     `json:"name,omitempty"`
	Relationship          string     `json:"relationship,omitempty"`
	ProcessingScope       string     `json:"processingScope,omitempty"`
	ContractRef           string     `json:"contractRef,omitempty"`
	ContractExpiresAt     *time.Time `json:"contractExpiresAt,omitempty"`
	Subprocessors         []string   `json:"subprocessors,omitempty"`
	CrossBorder           string     `json:"crossBorder,omitempty"`
	ReviewDueAt           *time.Time `json:"reviewDueAt,omitempty"`
	Scope                 string     `json:"scope,omitempty"`
	ProcessorPeople       *int64     `json:"processorPeople,omitempty"`
	ScaleEvidenceID       int64      `json:"scaleEvidenceId,omitempty"`
	Auditor               string     `json:"auditor,omitempty"`
	AuditorGroup          string     `json:"auditorGroup,omitempty"`
	LeadAuditor           string     `json:"leadAuditor,omitempty"`
	IndependenceNote      string     `json:"independenceNote,omitempty"`
	PreviousAuditorGroups []string   `json:"previousAuditorGroups,omitempty"`
	CompletedAt           *time.Time `json:"completedAt,omitempty"`
	PlannedAt             *time.Time `json:"plannedAt,omitempty"`
	ReportRef             string     `json:"reportRef,omitempty"`
	MinorData             bool       `json:"minorData,omitempty"`
	Level                 string     `json:"level,omitempty"`
	OperatorType          string     `json:"operatorType,omitempty"`
	DetectedAt            *time.Time `json:"detectedAt,omitempty"`
	ResponseEndedAt       *time.Time `json:"responseEndedAt,omitempty"`
	IndustryRule          string     `json:"industryRule,omitempty"`
	Channels              []string   `json:"channels,omitempty"`
	ServiceScope          string     `json:"serviceScope,omitempty"`
	Modality              string     `json:"modality,omitempty"`
	ArtifactDigest        string     `json:"artifactDigest,omitempty"`
	StandardVersion       string     `json:"standardVersion,omitempty"`
	ExplicitStatus        string     `json:"explicitStatus,omitempty"`
	ImplicitStatus        string     `json:"implicitStatus,omitempty"`
	ExportStatus          string     `json:"exportStatus,omitempty"`
	ProviderCode          string     `json:"providerCode,omitempty"`
	ContentID             string     `json:"contentId,omitempty"`
	VerificationMethod    string     `json:"verificationMethod,omitempty"`
	provided              []string
}

func (d *Details) UnmarshalJSON(b []byte) error {
	type plain Details
	var out plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return ErrInvalid
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil || keys == nil {
		return ErrInvalid
	}
	*d = Details(out)
	for k := range keys {
		d.provided = append(d.provided, k)
	}
	return nil
}

func Decode(b []byte) (Input, error) {
	var in Input
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, ErrInvalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return in, ErrInvalid
	}
	return in, in.Validate(time.Now())
}

var detailKeys = map[string]string{
	"rights_request": "requestType subjectRef receivedAt identityStatus identityEvidenceId responseDueAt systems outcome resolution retentionException",
	"consent":        "subjectRef purpose lawfulBasis noticeVersion grantedAt withdrawalAt systems",
	"vendor":         "name relationship processingScope contractRef contractExpiresAt subprocessors crossBorder reviewDueAt",
	"audit":          "scope processorPeople scaleEvidenceId auditor auditorGroup leadAuditor independenceNote previousAuditorGroups completedAt plannedAt reportRef minorData",
	"incident":       "level operatorType detectedAt responseEndedAt industryRule channels",
	"labeling":       "serviceScope modality artifactDigest standardVersion explicitStatus implicitStatus exportStatus providerCode contentId verificationMethod",
}

func text(s string, max int, required bool) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && (!required || strings.TrimSpace(s) != "") && !strings.ContainsAny(s, "\x00\r")
}
func list(v []string, max int) bool {
	if len(v) > max {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v {
		if !text(s, 160, true) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func IDs(v []int64) bool {
	if len(v) > 50 {
		return false
	}
	seen := map[int64]bool{}
	for _, id := range v {
		if id < 1 || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (in Input) Validate(now time.Time) error {
	if !slices.Contains(Kinds, in.Kind) || !text(in.Title, 120, true) || !text(in.Owner, 100, true) || in.ActivityID < 0 || in.Revision < 0 ||
		!IDs(in.CheckIDs) || !IDs(in.EvidenceIDs) || !list(in.RegulationIDs, 20) || !slices.Contains([]string{"applicable", "not_applicable", "unknown"}, in.Applicability) || !text(in.ApplicabilityReason, 600, true) || len(in.Receipts) > 50 {
		return ErrInvalid
	}
	for _, id := range in.RegulationIDs {
		if _, ok := RegulationByID(id); !ok {
			return ErrInvalid
		}
	}
	d := in.Details
	keys := d.provided
	if keys == nil {
		b, _ := json.Marshal(d)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(b, &m)
		for k := range m {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		if !slices.Contains(strings.Fields(detailKeys[in.Kind]), k) {
			return ErrInvalid
		}
	}
	for _, s := range []string{d.Outcome, d.RetentionException, d.Purpose, d.ProcessingScope, d.Scope, d.IndependenceNote, d.IndustryRule} {
		if !text(s, 600, false) {
			return ErrInvalid
		}
	}
	for _, s := range []string{d.LawfulBasis, d.NoticeVersion, d.Name, d.ContractRef, d.Auditor, d.AuditorGroup, d.LeadAuditor, d.ReportRef, d.StandardVersion, d.ProviderCode, d.ContentID} {
		if !text(s, 160, false) {
			return ErrInvalid
		}
	}
	for _, a := range [][]string{d.Systems, d.Subprocessors, d.Channels} {
		if !list(a, 20) {
			return ErrInvalid
		}
	}
	// Engagement history is most-recent first and must preserve repeated groups.
	if len(d.PreviousAuditorGroups) > 20 {
		return ErrInvalid
	}
	for _, group := range d.PreviousAuditorGroups {
		if !text(group, 160, true) {
			return ErrInvalid
		}
	}
	for _, t := range []*time.Time{d.ReceivedAt, d.ResponseDueAt, d.GrantedAt, d.WithdrawalAt, d.ContractExpiresAt, d.ReviewDueAt, d.CompletedAt, d.PlannedAt, d.DetectedAt, d.ResponseEndedAt} {
		if t != nil && (t.IsZero() || t.Year() < 2000 || t.Year() > 2200) {
			return ErrInvalid
		}
	}
	for _, t := range []*time.Time{d.ReceivedAt, d.GrantedAt, d.WithdrawalAt, d.CompletedAt, d.DetectedAt, d.ResponseEndedAt} {
		if t != nil && t.After(now.Add(5*time.Minute)) {
			return ErrInvalid
		}
	}
	if d.IdentityEvidenceID < 0 || d.ScaleEvidenceID < 0 || (d.ProcessorPeople != nil && (*d.ProcessorPeople < 0 || *d.ProcessorPeople > 10_000_000_000)) {
		return ErrInvalid
	}
	allowedReceipts := map[string][]string{
		"rights_request": {"identity_verification", "response_delivery", "rights_execution"},
		"consent":        {"consent_grant", "withdrawal_propagation"},
		"vendor":         {"contract"}, "audit": {"audit_report"},
		"incident": {"initial_report", "final_report"}, "labeling": {"labeling_validation"},
	}
	seenReceipts := map[Receipt]bool{}
	for _, r := range in.Receipts {
		if r.EvidenceID < 1 || !text(r.System, 160, false) || !slices.Contains([]string{"identity_verification", "response_delivery", "rights_execution", "consent_grant", "withdrawal_propagation", "contract", "audit_report", "initial_report", "final_report", "labeling_validation"}, r.Purpose) {
			return ErrInvalid
		}
		if !slices.Contains(allowedReceipts[in.Kind], r.Purpose) || seenReceipts[r] {
			return ErrInvalid
		}
		seenReceipts[r] = true
		if (r.Purpose == "withdrawal_propagation" || r.Purpose == "rights_execution") && !slices.Contains(d.Systems, r.System) {
			return ErrInvalid
		}
		if (r.Purpose == "initial_report" || r.Purpose == "final_report") && !slices.Contains(d.Channels, r.System) {
			return ErrInvalid
		}
	}
	switch in.Kind {
	case "rights_request":
		if !digest.MatchString(d.SubjectRef) || d.ReceivedAt == nil || !slices.Contains([]string{"access", "copy", "correct", "delete", "restrict", "withdraw_consent"}, d.RequestType) || !slices.Contains([]string{"pending", "verified", "failed"}, d.IdentityStatus) {
			return ErrInvalid
		}
		if d.Resolution != "" && !slices.Contains([]string{"fulfilled", "partial", "refused"}, d.Resolution) {
			return ErrInvalid
		}
		if d.ResponseDueAt != nil && d.ResponseDueAt.Before(*d.ReceivedAt) {
			return ErrInvalid
		}
	case "consent":
		if !digest.MatchString(d.SubjectRef) || !text(d.Purpose, 600, true) || !text(d.LawfulBasis, 160, true) || !text(d.NoticeVersion, 160, true) {
			return ErrInvalid
		}
		if d.GrantedAt != nil && d.WithdrawalAt != nil && d.WithdrawalAt.Before(*d.GrantedAt) {
			return ErrInvalid
		}
	case "vendor":
		if !text(d.Name, 160, true) || !text(d.ProcessingScope, 600, true) || !slices.Contains([]string{"entrusted", "independent", "joint", "unknown"}, d.Relationship) || !slices.Contains([]string{"yes", "no", "unknown"}, d.CrossBorder) {
			return ErrInvalid
		}
	case "audit":
		if !text(d.Scope, 600, true) {
			return ErrInvalid
		}
	case "incident":
		if !slices.Contains([]string{"unknown", "general", "significant", "major", "severe"}, d.Level) || !slices.Contains([]string{"unknown", "cii", "central", "other"}, d.OperatorType) || d.DetectedAt == nil {
			return ErrInvalid
		}
		if d.ResponseEndedAt != nil && d.ResponseEndedAt.Before(*d.DetectedAt) {
			return ErrInvalid
		}
	case "labeling":
		if !slices.Contains([]string{"internal", "public", "unknown"}, d.ServiceScope) || !slices.Contains([]string{"text", "image", "audio", "video", "virtual_scene"}, d.Modality) || (d.ArtifactDigest != "" && !digest.MatchString(d.ArtifactDigest)) || !slices.Contains([]string{"manual", "metadata", "unsupported"}, d.VerificationMethod) {
			return ErrInvalid
		}
		for _, s := range []string{d.ExplicitStatus, d.ImplicitStatus, d.ExportStatus} {
			if !slices.Contains([]string{"unknown", "present", "absent", "not_applicable"}, s) {
				return ErrInvalid
			}
		}
	}
	return nil
}

type Action struct {
	Revision int64  `json:"revision"`
	Action   string `json:"action"`
	Note     string `json:"note"`
}

func (a Action) Valid() bool {
	return a.Revision > 0 && text(a.Note, 600, true) && slices.Contains([]string{"start", "submit", "approve", "request_changes", "cancel", "withdraw"}, a.Action)
}

func NextStatus(in Input, status, action string, withdrawing bool, now time.Time) (string, error) {
	switch action {
	case "start":
		if slices.Contains([]string{"draft", "needs_work"}, status) {
			return "in_progress", nil
		}
	case "cancel":
		if slices.Contains([]string{"draft", "in_progress", "needs_work"}, status) {
			return "cancelled", nil
		}
	case "withdraw":
		if in.Kind == "consent" && in.Applicability == "applicable" && status == "completed" {
			return "withdrawal_pending", nil
		}
	case "request_changes":
		if status == "in_review" {
			return "needs_work", nil
		}
	case "submit":
		if slices.Contains([]string{"in_progress", "needs_work", "withdrawal_pending"}, status) {
			if err := CompletionReady(in, withdrawing, now); err != nil {
				return "", err
			}
			return "in_review", nil
		}
	case "approve":
		if status == "in_review" {
			if err := CompletionReady(in, withdrawing, now); err != nil {
				return "", err
			}
			if withdrawing {
				return "withdrawn", nil
			}
			return "completed", nil
		}
	}
	return "", ErrTransition
}

func hasReceipt(in Input, purpose, system string) bool {
	return slices.ContainsFunc(in.Receipts, func(r Receipt) bool { return r.Purpose == purpose && (system == "" || r.System == system) })
}
func CompletionReady(in Input, withdrawing bool, now time.Time) error {
	if in.Applicability == "unknown" || len(in.RegulationIDs) == 0 || len(in.EvidenceIDs) == 0 {
		return ErrEvidence
	}
	if in.Applicability == "not_applicable" {
		return nil
	}
	d := in.Details
	switch in.Kind {
	case "rights_request":
		identityReceipt := slices.ContainsFunc(in.Receipts, func(r Receipt) bool {
			return r.Purpose == "identity_verification" && r.EvidenceID == d.IdentityEvidenceID
		})
		identityReviewed := d.IdentityStatus == "verified" || (d.IdentityStatus == "failed" && d.Resolution == "refused")
		if !identityReviewed || d.IdentityEvidenceID < 1 || d.Outcome == "" || !identityReceipt || !hasReceipt(in, "response_delivery", "") || d.Resolution == "" {
			return ErrEvidence
		}
		if (d.Resolution == "refused" || d.Resolution == "partial") && d.RetentionException == "" {
			return ErrEvidence
		}
		if d.Resolution != "refused" && slices.Contains([]string{"delete", "correct", "restrict", "withdraw_consent"}, d.RequestType) {
			if len(d.Systems) == 0 {
				return ErrEvidence
			}
			for _, sys := range d.Systems {
				if !hasReceipt(in, "rights_execution", sys) {
					return ErrEvidence
				}
			}
		}
	case "consent":
		if withdrawing {
			if d.WithdrawalAt == nil || len(d.Systems) == 0 {
				return ErrEvidence
			}
			for _, sys := range d.Systems {
				if !hasReceipt(in, "withdrawal_propagation", sys) {
					return ErrEvidence
				}
			}
		} else if d.GrantedAt == nil || !hasReceipt(in, "consent_grant", "") {
			return ErrEvidence
		}
	case "vendor":
		if d.Relationship == "unknown" || d.CrossBorder == "unknown" || d.ContractRef == "" || !hasReceipt(in, "contract", "") || len(in.CheckIDs) == 0 || (d.ContractExpiresAt != nil && !d.ContractExpiresAt.After(now)) {
			return ErrEvidence
		}
	case "audit":
		if d.Auditor == "" || d.AuditorGroup == "" || d.LeadAuditor == "" || d.IndependenceNote == "" || d.ReportRef == "" || d.CompletedAt == nil || !hasReceipt(in, "audit_report", "") {
			return ErrEvidence
		}
		if d.ProcessorPeople != nil && d.ScaleEvidenceID < 1 {
			return ErrEvidence
		}
	case "incident":
		if d.Level == "unknown" || d.OperatorType == "unknown" || d.ResponseEndedAt == nil || len(d.Channels) == 0 {
			return ErrEvidence
		}
		if d.Level != "general" {
			for _, ch := range d.Channels {
				if !hasReceipt(in, "initial_report", ch) || !hasReceipt(in, "final_report", ch) {
					return ErrEvidence
				}
			}
		}
	case "labeling":
		if d.ServiceScope == "unknown" || d.ArtifactDigest == "" || d.StandardVersion == "" || d.VerificationMethod == "unsupported" || d.ProviderCode == "" || d.ContentID == "" || !hasReceipt(in, "labeling_validation", "") {
			return ErrEvidence
		}
		if d.ExplicitStatus != "present" || d.ImplicitStatus != "present" || d.ExportStatus != "present" {
			return ErrEvidence
		}
	}
	return nil
}
