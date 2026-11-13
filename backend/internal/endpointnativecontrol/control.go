// Package endpointnativecontrol contains fixed, reversible Windows control
// primitives. It does not schedule leases, install services, reboot, refresh
// Group Policy, verify functional prevention or accept arbitrary native paths.
package endpointnativecontrol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

const (
	USBWrite     = "windows_usb_removable_write"
	PrintPause   = "windows_print_queue_pause"
	RDPClipboard = "windows_rdp_clipboard_policy"
)

var (
	ErrTarget      = errors.New("invalid fixed native control target or snapshot")
	ErrUnsupported = errors.New("native control unavailable on this operating system or edition")
	ErrPermission  = errors.New("native control requires administrator or printer administration permission")
	ErrManaged     = errors.New("native control refused: another device policy authority is present or cannot be excluded")
	ErrConflict    = errors.New("native control state changed or already belongs to another administrator")
	ErrUnavailable = errors.New("native control API unavailable or incomplete")
	ErrReadback    = errors.New("native control write could not be confirmed by configuration readback")
)

// Target is local configuration, not an HTTP DTO. PrinterName is used only for
// a fixed installed local queue. References/digests come from the caller's
// anonymous configuration projection; this package never derives unsalted
// queue-name references or exports local names.
type Target struct {
	Executor     string `json:"executor"`
	TargetRef    string `json:"targetRef"`
	ConfigDigest string `json:"configDigest"`
	PrinterName  string `json:"printerName,omitempty"`
}

// State contains only one fixed registry DWORD or a configured queue's paused
// bit and configuration identity. ChangeStamp is a local registry key revision;
// it is deliberately excluded from StateDigest and used for stricter restore
// conflict detection. Neither snapshots nor Targets should be uploaded.
type State struct {
	Present        bool   `json:"present"`
	Value          uint32 `json:"value,omitempty"`
	Paused         bool   `json:"paused,omitempty"`
	IdentityDigest string `json:"identityDigest,omitempty"`
	ChangeStamp    string `json:"changeStamp,omitempty"`
}

type Snapshot struct {
	Version      int    `json:"version"`
	Executor     string `json:"executor"`
	TargetRef    string `json:"targetRef"`
	ConfigDigest string `json:"configDigest"`
	Phase        string `json:"phase"`
	StateDigest  string `json:"stateDigest"`
	State        State  `json:"state"`
}

type Readback struct {
	Snapshot           Snapshot `json:"snapshot"`
	Configured         bool     `json:"configured"`
	FunctionalVerified bool     `json:"functionalVerified"`
	RequiredAction     string   `json:"requiredAction"`
	Reason             string   `json:"reason"`
}

type Manager struct {
	locks   map[string]*sync.Mutex
	targets map[string]Target
	read    func(context.Context, Target) (State, error)
	change  func(context.Context, Target, State, State) (State, error)
}

func TargetScope(executor string) string {
	switch executor {
	case USBWrite:
		return "removable_storage_write"
	case PrintPause:
		return "configured_print_queues"
	case RDPClipboard:
		return "rdp_clipboard_redirection"
	}
	return ""
}

func New(targets []Target) (*Manager, error) {
	if len(targets) < 1 || len(targets) > 34 {
		return nil, ErrTarget
	}
	m := &Manager{targets: map[string]Target{}, locks: map[string]*sync.Mutex{}, read: platformRead, change: platformChange}
	names := map[string]bool{}
	kinds := map[string]bool{}
	for _, t := range targets {
		if TargetScope(t.Executor) == "" || !digest(t.TargetRef) || !digest(t.ConfigDigest) {
			return nil, ErrTarget
		}
		if _, ok := m.targets[t.TargetRef]; ok {
			return nil, ErrTarget
		}
		if t.Executor == PrintPause {
			// Reject UNC/server/Xcv/Job handle syntaxes; the Windows backend also
			// checks this name against locally installed queue attributes.
			if len(t.PrinterName) < 1 || len(t.PrinterName) > 256 || strings.TrimSpace(t.PrinterName) != t.PrinterName || strings.ContainsAny(t.PrinterName, "\\/,\x00\r\n\t") {
				return nil, ErrTarget
			}
			name := strings.ToLower(t.PrinterName)
			if names[name] {
				return nil, ErrTarget
			}
			names[name] = true
		} else {
			if t.PrinterName != "" || kinds[t.Executor] {
				return nil, ErrTarget
			}
			kinds[t.Executor] = true
		}
		m.targets[t.TargetRef] = t
		m.locks[t.TargetRef] = &sync.Mutex{}
	}
	if len(names) > 32 {
		return nil, ErrTarget
	}
	return m, nil
}

func digest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func snapshot(t Target, state State, phase string) Snapshot {
	s := Snapshot{Version: 1, Executor: t.Executor, TargetRef: t.TargetRef, ConfigDigest: t.ConfigDigest, Phase: phase, State: state}
	s.StateDigest = stateDigest(s)
	return s
}

func stateDigest(s Snapshot) string {
	s.Phase = ""
	s.StateDigest = ""
	s.State.ChangeStamp = ""
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func valid(s Snapshot) bool {
	if s.Version != 1 || TargetScope(s.Executor) == "" || !digest(s.TargetRef) || !digest(s.ConfigDigest) || s.StateDigest != stateDigest(s) {
		return false
	}
	if s.Executor == PrintPause {
		return s.State.Present && s.State.Value == 0 && digest(s.State.IdentityDigest) && s.State.ChangeStamp == ""
	}
	if s.State.Paused || s.State.IdentityDigest != "" || s.State.Value > 1 || !s.State.Present && s.State.Value != 0 {
		return false
	}
	if s.State.ChangeStamp != "" {
		if len(s.State.ChangeStamp) != 16 {
			return false
		}
		if _, err := hex.DecodeString(s.State.ChangeStamp); err != nil {
			return false
		}
	}
	return true
}

// Expected is pure. Persist the original and this desired snapshot before
// Apply, but never treat desired as proof that this process performed a write.
func Expected(before Snapshot) (Snapshot, error) {
	if !valid(before) || before.Phase != "captured" {
		return Snapshot{}, ErrTarget
	}
	next := before
	next.Phase = "desired"
	if before.Executor == PrintPause {
		if before.State.Paused {
			return Snapshot{}, ErrConflict
		}
		next.State.Paused = true
	} else {
		// Even an existing explicit zero belongs to another policy authority.
		// This controller never takes over an existing configured value.
		if before.State.Present {
			return Snapshot{}, ErrConflict
		}
		next.State.Present = true
		next.State.Value = 1
		next.State.ChangeStamp = ""
	}
	next.StateDigest = stateDigest(next)
	return next, nil
}

func (m *Manager) bound(s Snapshot) (Target, error) {
	t, ok := m.targets[s.TargetRef]
	if !ok || !valid(s) || t.Executor != s.Executor || t.ConfigDigest != s.ConfigDigest {
		return Target{}, ErrTarget
	}
	return t, nil
}

func (m *Manager) Capture(ctx context.Context, ref string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	t, ok := m.targets[ref]
	if !ok {
		return Snapshot{}, ErrTarget
	}
	m.locks[ref].Lock()
	defer m.locks[ref].Unlock()
	state, err := m.read(ctx, t)
	if err != nil {
		return Snapshot{}, err
	}
	s := snapshot(t, state, "captured")
	if !valid(s) {
		return Snapshot{}, ErrReadback
	}
	return s, nil
}

func (m *Manager) Readback(ctx context.Context, ref string) (Readback, error) {
	if err := ctx.Err(); err != nil {
		return failed(err), err
	}
	t, ok := m.targets[ref]
	if !ok {
		return failed(ErrTarget), ErrTarget
	}
	m.locks[ref].Lock()
	defer m.locks[ref].Unlock()
	state, err := m.read(ctx, t)
	if err != nil {
		return failed(err), err
	}
	s := snapshot(t, state, "readback")
	if !valid(s) {
		return failed(ErrReadback), ErrReadback
	}
	return result(s), nil
}

func sameCurrent(a, b State) bool { return a == b }

func (m *Manager) Apply(ctx context.Context, before Snapshot) (Readback, error) {
	if err := ctx.Err(); err != nil {
		return failed(err), err
	}
	t, err := m.bound(before)
	if err != nil {
		return failed(err), err
	}
	m.locks[before.TargetRef].Lock()
	defer m.locks[before.TargetRef].Unlock()
	expected, err := Expected(before)
	if err != nil {
		return failed(err), err
	}
	current, err := m.read(ctx, t)
	if err != nil {
		return failed(err), err
	}
	if !sameCurrent(current, before.State) {
		return failed(ErrConflict), ErrConflict
	}
	next, err := m.change(ctx, t, current, expected.State)
	if err != nil {
		return failed(err), err
	}
	applied := snapshot(t, next, "applied")
	if !valid(applied) || applied.StateDigest != expected.StateDigest || t.Executor != PrintPause && applied.State.ChangeStamp == "" {
		return failed(ErrReadback), ErrReadback
	}
	return result(applied), nil
}

// Restore requires an actual applied readback sealed by the caller after Apply
// returned. A crash with only an intent/desired snapshot is ambiguous and must
// be reviewed; matching a paused bit or DWORD alone is not ownership proof.
func (m *Manager) Restore(ctx context.Context, before, applied Snapshot) (Readback, error) {
	if err := ctx.Err(); err != nil {
		return failed(err), err
	}
	t, err := m.bound(before)
	if err != nil {
		return failed(err), err
	}
	m.locks[before.TargetRef].Lock()
	defer m.locks[before.TargetRef].Unlock()
	if _, err = m.bound(applied); err != nil {
		return failed(err), err
	}
	expected, err := Expected(before)
	if err != nil {
		return failed(err), err
	}
	if applied.Phase != "applied" || expected.StateDigest != applied.StateDigest || t.Executor != PrintPause && applied.State.ChangeStamp == "" {
		return failed(ErrConflict), ErrConflict
	}
	current, err := m.read(ctx, t)
	if err != nil {
		return failed(err), err
	}
	observed := snapshot(t, current, "restored")
	if observed.StateDigest == before.StateDigest {
		return result(observed), nil
	} // already restored, no write
	if !sameCurrent(current, applied.State) {
		return failed(ErrConflict), ErrConflict
	}
	next, err := m.change(ctx, t, current, before.State)
	if err != nil {
		return failed(err), err
	}
	restored := snapshot(t, next, "restored")
	if !valid(restored) || restored.StateDigest != before.StateDigest {
		return failed(ErrReadback), ErrReadback
	}
	return result(restored), nil
}

func result(s Snapshot) Readback {
	r := Readback{Snapshot: s, Configured: s.State.Present && s.State.Value == 1, RequiredAction: "restart_and_verify", Reason: "仅回读固定策略配置；需管理员安排生效步骤并验证功能，未证明实际阻断或恢复。"}
	if s.Executor == PrintPause {
		r.Configured = s.State.Paused
		r.RequiredAction = "verify_queue_scheduling"
		r.Reason = "仅回读指定本地队列暂停位；当前在印任务可能继续，仍可提交任务，未证明所有打印已阻断。"
	}
	if s.Executor == RDPClipboard {
		r.RequiredAction = "reconnect_or_restart_and_verify"
		r.Reason = "仅回读RDP剪贴板重定向配置；需验证会话生效，本地应用剪贴板不受此控制。"
	}
	return r
}

func failed(err error) Readback {
	return Readback{RequiredAction: "administrator_review", Reason: Reason(err)}
}

func Reason(err error) string {
	switch {
	case errors.Is(err, ErrManaged):
		return "设备已加入AD域、受MDM管理或无法确认策略归属，拒绝覆盖组织管理配置。"
	case errors.Is(err, ErrPermission):
		return "当前进程缺少管理员或本地打印队列管理权限，未接管控制。"
	case errors.Is(err, ErrConflict):
		return "原配置已存在、状态发生变化或缺少已应用凭据，保留其他管理员配置并转人工处理。"
	case errors.Is(err, ErrUnsupported):
		return "当前系统、版本类型或平台不支持此固定控制器。"
	case errors.Is(err, ErrReadback):
		return "配置写入结果无法确认，不能认定已阻断；请保留租约记录并人工核对。"
	case errors.Is(err, ErrTarget):
		return "固定目标、配置摘要或恢复快照不匹配，未执行操作。"
	default:
		return "原生接口不可用或返回不完整，未验证实际控制效果。"
	}
}
