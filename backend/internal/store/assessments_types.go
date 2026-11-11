package store

import "time"

// Assessment keeps the adopted template revision and activity revision immutable.
type Assessment struct {
	ID                 int64     `json:"id"`
	Kind               string    `json:"kind"`
	Title              string    `json:"title"`
	Status             string    `json:"status"`
	ActivityID         int64     `json:"activityId"`
	TemplateRevisionID int64     `json:"templateRevisionId"`
	CreatorID          int64     `json:"creatorId"`
	UpdatedAt          time.Time `json:"updatedAt"`
	DueAt              time.Time `json:"dueAt"`
	Stale              bool      `json:"stale"`
}

type AssessmentQuestion struct {
	Applicability string `json:"applicability"`
	BasisURL      string `json:"basisUrl"`
	Obligation    string `json:"obligation"`
	Required      bool   `json:"required"`
	FactKey       string `json:"factKey,omitempty"`

	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Kind            string   `json:"kind"`
	Options         []string `json:"options,omitempty"`
	Basis           string   `json:"basis"`
	Evidence        string   `json:"evidence"`
	Acceptance      string   `json:"acceptance"`
	RequirementCode string   `json:"requirementCode,omitempty"`
}

type AssessmentFact struct {
	Key        string     `json:"key"`
	State      string     `json:"state"`
	Value      string     `json:"value,omitempty"`
	Source     string     `json:"source"`
	Record     string     `json:"record,omitempty"`
	ObservedAt *time.Time `json:"observedAt,omitempty"`
}
type AssessmentAnswer struct {
	Key         string   `json:"key"`
	Value       string   `json:"value,omitempty"`
	Values      []string `json:"values,omitempty"`
	Number      *float64 `json:"number,omitempty"`
	EvidenceIDs []int64  `json:"evidenceIds,omitempty"`
	Note        string   `json:"note,omitempty"`
}
type AssessmentTemplate struct {
	ID         int64                `json:"id"`
	RevisionID *int64               `json:"revisionId,omitempty"`
	Key        string               `json:"key"`
	Title      string               `json:"title"`
	Kind       string               `json:"kind"`
	State      string               `json:"state"`
	Revision   int64                `json:"revision"`
	BuiltIn    bool                 `json:"builtIn"`
	Questions  []AssessmentQuestion `json:"questions"`
	Template   *Template            `json:"template,omitempty"`
}
type AssessmentTemplateInput struct {
	Key            string               `json:"key"`
	Title          string               `json:"title"`
	Kind           string               `json:"kind"`
	Revision       int64                `json:"revision"`
	BaseRevisionID *int64               `json:"baseRevisionId,omitempty"`
	Questions      []AssessmentQuestion `json:"questions"`
}
type AssessmentInput struct {
	ActivityID         int64     `json:"activityId"`
	TemplateRevisionID int64     `json:"templateRevisionId"`
	Title              string    `json:"title"`
	DueAt              time.Time `json:"dueAt"`
	RefreshMinutes     int       `json:"refreshMinutes"`
}
type AssessmentAnswersInput struct {
	Revision int64              `json:"revision"`
	Answers  []AssessmentAnswer `json:"answers"`
	CheckIDs []int64            `json:"checkIds"`
}
type AssessmentReviewInput struct {
	Revision int64  `json:"revision"`
	Action   string `json:"action"`
	Note     string `json:"note"`
}
type AssessmentGap struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}
type AssessmentEvidence struct {
	ID         int64     `json:"id"`
	CheckID    int64     `json:"checkId"`
	Kind       string    `json:"kind"`
	Source     string    `json:"source"`
	Record     string    `json:"record"`
	Summary    string    `json:"summary"`
	ObservedAt time.Time `json:"observedAt"`
}
type AssessmentCheck struct {
	ID              int64  `json:"id"`
	RequirementCode string `json:"requirementCode"`
	Status          string `json:"status"`
	OwnerTeam       string `json:"ownerTeam"`
}
type AssessmentEvent struct {
	ID        int64     `json:"id"`
	Action    string    `json:"action"`
	Actor     string    `json:"actor"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"createdAt"`
}
type AssessmentApplicability struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
	Source string `json:"source"`
	Record string `json:"record,omitempty"`
}
type AssessmentDetail struct {
	AutomaticApplicability AssessmentApplicability `json:"automaticApplicability"`
	EvidenceTruncated      bool                    `json:"evidenceTruncated,omitempty"`
	Assessment
	Revision            int64                `json:"revision"`
	Creator             string               `json:"creator"`
	Reviewer            string               `json:"reviewer,omitempty"`
	ReviewNote          string               `json:"reviewNote"`
	Template            AssessmentTemplate   `json:"template"`
	Activity            Activity             `json:"activity"`
	Facts               []AssessmentFact     `json:"facts"`
	Answers             []AssessmentAnswer   `json:"answers"`
	Evidence            []AssessmentEvidence `json:"evidence"`
	Checks              []AssessmentCheck    `json:"checks"`
	Gaps                []AssessmentGap      `json:"gaps"`
	Risks               []AssessmentGap      `json:"risks"`
	Events              []AssessmentEvent    `json:"events"`
	Applicability       string               `json:"applicability"`
	ReassessmentPending bool                 `json:"reassessmentPending"`
	FactsRefreshedAt    time.Time            `json:"factsRefreshedAt"`
	RefreshMinutes      int                  `json:"refreshMinutes"`
	CanEdit             bool                 `json:"canEdit"`
	CanReview           bool                 `json:"canReview"`
	CanApprove          bool                 `json:"canApprove"`
	Basis               string               `json:"basis"`
}
