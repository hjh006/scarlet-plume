package store

import "time"

type EndpointCapability struct {
	Channel      string   `json:"channel"`
	Scope        string   `json:"scope"`
	Supported    bool     `json:"supported"`
	Modes        []string `json:"modes"`
	Executor     string   `json:"executor"`
	Reason       string   `json:"reason"`
	TargetScope  string   `json:"targetScope,omitempty"`
	TargetRefs   []string `json:"targetRefs,omitempty"`
	ConfigDigest string   `json:"configDigest,omitempty"`
	Privilege    string   `json:"privilege,omitempty"`
}
type EndpointHeartbeatInput struct {
	DeviceRef    string               `json:"deviceRef"`
	OS           string               `json:"os"`
	AgentVersion string               `json:"agentVersion"`
	ObservedAt   time.Time            `json:"observedAt"`
	Capabilities []EndpointCapability `json:"capabilities"`
}
type EndpointDevice struct {
	EndpointHeartbeatInput
	SourceID   int64     `json:"sourceId"`
	SourceName string    `json:"sourceName"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	Online     bool      `json:"online"`
	Current    bool      `json:"current"`
}
type EndpointPolicyInput struct {
	TargetScope     string   `json:"targetScope,omitempty"`
	TargetRefs      []string `json:"targetRefs,omitempty"`
	ConfigDigest    string   `json:"configDigest,omitempty"`
	SourceID        int64    `json:"sourceId"`
	DeviceRef       string   `json:"deviceRef"`
	Channel         string   `json:"channel"`
	Scope           string   `json:"scope"`
	Mode            string   `json:"mode"`
	Categories      []string `json:"categories"`
	Reason          string   `json:"reason"`
	DurationMinutes int      `json:"durationMinutes"`
	Revision        int64    `json:"revision"`
}
type EndpointPolicy struct {
	EndpointPolicyInput
	ID               int64                `json:"id"`
	OS               string               `json:"os"`
	Executor         string               `json:"executor"`
	SourceName       string               `json:"sourceName"`
	Creator          string               `json:"creator"`
	AgentVersion     string               `json:"agentVersion"`
	Capability       EndpointCapability   `json:"capability"`
	CreatorID        int64                `json:"-"`
	CreatedAt        time.Time            `json:"createdAt"`
	ExpiresAt        time.Time            `json:"expiresAt"`
	Digest           string               `json:"digest"`
	CapabilityDigest string               `json:"capabilityDigest"`
	Status           string               `json:"status"`
	Approver         string               `json:"approver,omitempty"`
	ApprovedAt       *time.Time           `json:"approvedAt,omitempty"`
	DecisionNote     string               `json:"decisionNote,omitempty"`
	EndedAt          *time.Time           `json:"endedAt,omitempty"`
	SessionCurrent   *bool                `json:"sessionCurrent,omitempty"`
	CurrentReason    string               `json:"currentReason,omitempty"`
	Current          bool                 `json:"current"`
	DeploymentState  string               `json:"deploymentState"`
	CanReview        bool                 `json:"canReview"`
	NativeLease      *EndpointNativeLease `json:"nativeLease,omitempty"`
	LatestReceipt    *EndpointReceipt     `json:"latestReceipt,omitempty"`
}
type EndpointPolicyDetail struct {
	EndpointPolicy
	Receipts               []EndpointReceipt      `json:"receipts"`
	SessionEvents          []EndpointSessionEvent `json:"sessionEvents"`
	SessionEventsTotal     int64                  `json:"sessionEventsTotal"`
	SessionEventsTruncated bool                   `json:"sessionEventsTruncated"`
	SessionCoverageMissing []string               `json:"sessionCoverageMissing"`
}
type EndpointPolicyBundle struct {
	DeviceRef      string                  `json:"deviceRef"`
	IssuedAt       time.Time               `json:"issuedAt"`
	Policies       []EndpointPolicy        `json:"policies"`
	NativeCommands []EndpointNativeCommand `json:"nativeCommands"`
}
type EndpointReceiptInput struct {
	Phase              string    `json:"phase,omitempty"`
	LeaseRef           string    `json:"leaseRef,omitempty"`
	OperationRef       string    `json:"operationRef,omitempty"`
	Action             string    `json:"action,omitempty"`
	TargetScope        string    `json:"targetScope,omitempty"`
	TargetRefs         []string  `json:"targetRefs,omitempty"`
	ConfigDigest       string    `json:"configDigest,omitempty"`
	PriorStateDigest   string    `json:"priorStateDigest,omitempty"`
	StateDigest        string    `json:"stateDigest,omitempty"`
	VerificationMethod string    `json:"verificationMethod,omitempty"`
	VerificationRef    string    `json:"verificationRef,omitempty"`
	ReceiptRef         string    `json:"receiptRef"`
	DeviceRef          string    `json:"deviceRef"`
	PolicyID           int64     `json:"policyId"`
	PolicyRevision     int64     `json:"policyRevision"`
	PolicyDigest       string    `json:"policyDigest"`
	Channel            string    `json:"channel"`
	Scope              string    `json:"scope"`
	Executor           string    `json:"executor"`
	Status             string    `json:"status"`
	Code               string    `json:"code"`
	ObservedAt         time.Time `json:"observedAt"`
}
type EndpointReceipt struct {
	EndpointReceiptInput
	ID             int64     `json:"id"`
	SourceID       int64     `json:"sourceId"`
	ReceivedAt     time.Time `json:"receivedAt"`
	SnapshotDigest string    `json:"snapshotDigest"`
}
type EndpointEvidenceSnapshot struct {
	SessionEvents          []EndpointSessionEvent    `json:"sessionEvents,omitempty"`
	SessionEventsTotal     int64                     `json:"sessionEventsTotal,omitempty"`
	SessionEventsTruncated bool                      `json:"sessionEventsTruncated,omitempty"`
	SessionCoverageMissing []string                  `json:"sessionCoverageMissing,omitempty"`
	Version                int                       `json:"version"`
	Policy                 EndpointPolicy            `json:"policy"`
	Device                 EndpointDevice            `json:"device"`
	Receipts               []EndpointReceipt         `json:"receipts"`
	Events                 []OperationsSecurityEvent `json:"events"`
	Truncated              bool                      `json:"truncated"`
	Boundaries             []string                  `json:"boundaries"`
}
type EndpointEvidenceExport struct {
	ID             int64                    `json:"id"`
	PolicyID       int64                    `json:"policyId"`
	CreatedAt      time.Time                `json:"createdAt"`
	Actor          string                   `json:"actor"`
	SnapshotDigest string                   `json:"snapshotDigest"`
	Snapshot       EndpointEvidenceSnapshot `json:"snapshot"`
}

// EndpointNativeLease records source attestations for one frozen native policy.
// configured/readback do not establish functional enforcement.
type EndpointNativeLease struct {
	LeaseRef             string     `json:"leaseRef"`
	ApplyOperationRef    string     `json:"applyOperationRef"`
	RollbackOperationRef string     `json:"rollbackOperationRef"`
	State                string     `json:"state"`
	IssuedAt             time.Time  `json:"issuedAt"`
	ExpiresAt            time.Time  `json:"expiresAt"`
	PriorStateDigest     string     `json:"priorStateDigest,omitempty"`
	PlannedStateDigest   string     `json:"plannedStateDigest,omitempty"`
	PreparedAt           *time.Time `json:"preparedAt,omitempty"`
	AppliedStateDigest   string     `json:"appliedStateDigest,omitempty"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}
type EndpointNativeCommand struct {
	LeaseRef     string         `json:"leaseRef"`
	OperationRef string         `json:"operationRef"`
	Action       string         `json:"action"`
	Reason       string         `json:"reason"`
	Policy       EndpointPolicy `json:"policy"`
}

// EndpointSessionEventInput contains only bounded source attestations from one
// interactive clipboard session. No clipboard content or OS identity is stored.
type EndpointSessionEventInput struct {
	EventRef           string    `json:"eventRef"`
	OperationRef       string    `json:"operationRef"`
	DeviceRef          string    `json:"deviceRef"`
	SessionRef         string    `json:"sessionRef"`
	PolicyID           int64     `json:"policyId"`
	PolicyRevision     int64     `json:"policyRevision"`
	PolicyDigest       string    `json:"policyDigest"`
	Scope              string    `json:"scope"`
	Executor           string    `json:"executor"`
	ConfigDigest       string    `json:"configDigest"`
	Status             string    `json:"status"`
	Code               string    `json:"code"`
	ObservedAt         time.Time `json:"observedAt"`
	ResponseAt         time.Time `json:"responseAt"`
	Capture            string    `json:"capture"`
	Categories         []string  `json:"categories"`
	SequenceRef        string    `json:"sequenceRef,omitempty"`
	CurrentSequenceRef string    `json:"currentSequenceRef,omitempty"`
	ContentRef         string    `json:"contentRef,omitempty"`
	ClearAttempted     bool      `json:"clearAttempted"`
	SequenceMatched    bool      `json:"sequenceMatched"`
	ClipboardEmpty     bool      `json:"clipboardEmpty"`
}
type EndpointSessionEvent struct {
	EndpointSessionEventInput
	Historical      bool      `json:"historical"`
	CurrentResponse bool      `json:"currentResponse"`
	ID              int64     `json:"id"`
	SourceID        int64     `json:"sourceId"`
	ReceivedAt      time.Time `json:"receivedAt"`
	SnapshotDigest  string    `json:"snapshotDigest"`
}
