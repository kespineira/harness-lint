// Package domain contains runtime-neutral values shared by adapters and
// analysis. It deliberately contains no persistence or runtime-specific code.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Runtime identifies the harness that produced an observation.
type Runtime string

const (
	RuntimeUnknown Runtime = "unknown"
	RuntimeClaude  Runtime = "claude-code"
	RuntimeCodex   Runtime = "codex"
	RuntimeCursor  Runtime = "cursor"

	// RuntimeClaudeCode is the descriptive alias for RuntimeClaude. Keep the
	// shorter name for callers that use the runtime's product name.
	RuntimeClaudeCode Runtime = RuntimeClaude
)

func (r Runtime) Valid() bool {
	switch r {
	case RuntimeClaude, RuntimeCodex, RuntimeCursor:
		return true
	default:
		return false
	}
}

// CapabilityType classifies an installed or observed capability. MCP servers
// and MCP tools intentionally have separate values because they have distinct
// discovery and usage semantics.
type CapabilityType string

const (
	CapabilityUnknown         CapabilityType = "unknown"
	CapabilitySkill           CapabilityType = "skill"
	CapabilityMCPServer       CapabilityType = "mcp_server"
	CapabilityMCPTool         CapabilityType = "mcp_tool"
	CapabilityAgent           CapabilityType = "agent"
	CapabilityTool            CapabilityType = "tool"
	CapabilityHook            CapabilityType = "hook"
	CapabilityInstructionFile CapabilityType = "instruction_file"

	// Commands and plugins remain distinct inventory categories because some
	// runtimes expose them as installed definitions rather than tools.
	CapabilityCommand CapabilityType = "command"
	CapabilityPlugin  CapabilityType = "plugin"

	// CapabilityMCP is retained as a source-compatible alias for the old
	// unsplit value. It now means an MCP server; the persisted value is still
	// mcp_server, so MCP servers and tools cannot be collapsed.
	CapabilityMCP CapabilityType = CapabilityMCPServer
)

func (t CapabilityType) Valid() bool {
	switch t {
	case CapabilitySkill, CapabilityMCPServer, CapabilityMCPTool, CapabilityAgent, CapabilityTool, CapabilityHook, CapabilityInstructionFile, CapabilityCommand, CapabilityPlugin:
		return true
	default:
		return false
	}
}

// Scope describes where a capability is installed or advertised.
type Scope string

const (
	ScopeUnknown Scope = "unknown"
	ScopeGlobal  Scope = "global"
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeSession Scope = "session"
)

func (s Scope) Valid() bool {
	switch s {
	case ScopeGlobal, ScopeUser, ScopeProject, ScopeSession:
		return true
	default:
		return false
	}
}

// EventType is intentionally limited to usage observations. Installed
// inventory is represented by Capability and is not an event type.
type EventType string

const (
	EventAdvertised EventType = "advertised"
	EventLoaded     EventType = "loaded"
	EventInvoked    EventType = "invoked"
)

func (t EventType) Valid() bool {
	switch t {
	case EventAdvertised, EventLoaded, EventInvoked:
		return true
	default:
		return false
	}
}

// Confidence describes how a measurement was obtained.
type Confidence string

const (
	ConfidenceExact     Confidence = "exact"
	ConfidenceObserved  Confidence = "observed"
	ConfidenceEstimated Confidence = "estimated"
	ConfidenceUnknown   Confidence = "unknown"
)

func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceExact, ConfidenceObserved, ConfidenceEstimated, ConfidenceUnknown:
		return true
	default:
		return false
	}
}

// Measurement carries an advertised token quantity together with its
// provenance. It does not represent an exact runtime context cost. Unknown
// measurements use Value zero and ConfidenceUnknown.
type Measurement struct {
	Value      int64
	Confidence Confidence
	Basis      string
}

// Validate checks the measurement's value and provenance. Unknown values are
// represented by zero; all known values must explain how they were obtained.
func (m Measurement) Validate() error {
	if m.Value < 0 {
		return errors.New("measurement value cannot be negative")
	}
	if !m.Confidence.Valid() {
		return fmt.Errorf("invalid measurement confidence %q", m.Confidence)
	}
	if m.Confidence == ConfidenceUnknown {
		if m.Value != 0 {
			return errors.New("unknown measurement value must be zero")
		}
		return nil
	}
	if strings.TrimSpace(m.Basis) == "" {
		return errors.New("measurement basis is required")
	}
	return nil
}

func (m Measurement) Valid() bool {
	return m.Validate() == nil
}

// EnabledState describes whether a capability is enabled in its source
// configuration. Unknown is distinct from disabled and is persisted as such.
type EnabledState string

const (
	EnabledStateEnabled  EnabledState = "enabled"
	EnabledStateDisabled EnabledState = "disabled"
	EnabledStateUnknown  EnabledState = "unknown"

	// Short aliases follow the naming convention used by the other domain
	// enums and keep call sites concise without changing the wire values.
	EnabledEnabled  = EnabledStateEnabled
	EnabledDisabled = EnabledStateDisabled
	EnabledUnknown  = EnabledStateUnknown
)

func (s EnabledState) Valid() bool {
	switch s {
	case EnabledStateEnabled, EnabledStateDisabled, EnabledStateUnknown:
		return true
	default:
		return false
	}
}

// AdvertisementState describes current, configured, or observed exposure of
// an installed capability. Fully advertised means the name and metadata are
// exposed, name-only means only the name is exposed, and not advertised also
// covers runtime modes such as user-invocable-only or off. This is exposure
// evidence, not proof that every model request received the capability.
type AdvertisementState string

const (
	AdvertisementStateUnknown         AdvertisementState = "unknown"
	AdvertisementStateFullyAdvertised AdvertisementState = "fully_advertised"
	AdvertisementStateNameOnly        AdvertisementState = "name_only"
	AdvertisementStateNotAdvertised   AdvertisementState = "not_advertised"
)

func (s AdvertisementState) Valid() bool {
	switch s {
	case AdvertisementStateUnknown, AdvertisementStateFullyAdvertised, AdvertisementStateNameOnly, AdvertisementStateNotAdvertised:
		return true
	default:
		return false
	}
}

// Capability is an installed inventory item discovered by an adapter.
type Capability struct {
	Runtime        Runtime
	Type           CapabilityType
	Name           string
	Scope          Scope
	Source         string
	Enabled        EnabledState
	Advertisement  AdvertisementState
	Hash           string
	MetadataTokens Measurement
	BodyTokens     Measurement
	FirstSeen      time.Time
	LastSeen       time.Time
}

func (c Capability) Validate() error {
	if !c.Runtime.Valid() {
		return fmt.Errorf("invalid runtime %q", c.Runtime)
	}
	if !c.Type.Valid() {
		return fmt.Errorf("invalid capability type %q", c.Type)
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("capability name is required")
	}
	if !c.Scope.Valid() {
		return fmt.Errorf("invalid scope %q", c.Scope)
	}
	if !c.Enabled.Valid() {
		return fmt.Errorf("invalid enabled state %q", c.Enabled)
	}
	if !c.Advertisement.Valid() {
		return fmt.Errorf("invalid advertisement state %q", c.Advertisement)
	}
	measurements := []struct {
		name  string
		value Measurement
	}{
		{name: "advertised metadata tokens", value: c.MetadataTokens},
		{name: "advertised body tokens", value: c.BodyTokens},
	}
	for _, measurement := range measurements {
		if err := measurement.value.Validate(); err != nil {
			return fmt.Errorf("invalid %s measurement: %w", measurement.name, err)
		}
	}
	if c.FirstSeen.IsZero() != c.LastSeen.IsZero() {
		return errors.New("first and last seen must both be set or both be zero")
	}
	if !c.FirstSeen.IsZero() && c.LastSeen.Before(c.FirstSeen) {
		return errors.New("last seen precedes first seen")
	}
	return nil
}

// Severity is used by discovery findings and is not persisted in the MVP store.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

func (s Severity) Valid() bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityError
}

// Finding is a metadata-only discovery diagnostic.
type Finding struct {
	Runtime        Runtime
	Code           string
	Message        string
	Severity       Severity
	CapabilityType CapabilityType
	CapabilityName string
	Confidence     Confidence
}

func (f Finding) Validate() error {
	if !f.Runtime.Valid() {
		return fmt.Errorf("invalid runtime %q", f.Runtime)
	}
	if strings.TrimSpace(f.Code) == "" || strings.TrimSpace(f.Message) == "" {
		return errors.New("finding code and message are required")
	}
	if !f.Severity.Valid() || !f.Confidence.Valid() {
		return errors.New("invalid finding severity or confidence")
	}
	if f.CapabilityType != CapabilityUnknown && !f.CapabilityType.Valid() {
		return fmt.Errorf("invalid finding capability type %q", f.CapabilityType)
	}
	if f.CapabilityType.Valid() && strings.TrimSpace(f.CapabilityName) == "" {
		return errors.New("finding capability name is required for a known capability type")
	}
	return nil
}

// Discovery is the result of an adapter inventory scan.
type Discovery struct {
	Capabilities []Capability
	Findings     []Finding
}

// Validate checks all values returned by an adapter before they enter the
// persistence or analysis layers.
func (d Discovery) Validate() error {
	for _, capability := range d.Capabilities {
		if err := capability.Validate(); err != nil {
			return fmt.Errorf("validate capability %q: %w", capability.Name, err)
		}
	}
	for _, finding := range d.Findings {
		if err := finding.Validate(); err != nil {
			return fmt.Errorf("validate finding %q: %w", finding.Code, err)
		}
	}
	return nil
}

// UsageEvent is metadata-only usage imported from a runtime. SessionID and
// ProjectID must be pre-normalized stable identifiers or one-way hashes before
// they reach this package; raw paths and conversation data are not accepted by
// the store's data model.
type UsageEvent struct {
	Timestamp      time.Time
	Runtime        Runtime
	SessionID      string
	ProjectID      string
	CapabilityType CapabilityType
	CapabilityName string
	EventType      EventType
	Fingerprint    string
}

func (e UsageEvent) Validate() error {
	if e.Timestamp.IsZero() {
		return errors.New("usage event timestamp is required")
	}
	if !e.Runtime.Valid() {
		return fmt.Errorf("invalid runtime %q", e.Runtime)
	}
	if strings.TrimSpace(e.SessionID) == "" || strings.TrimSpace(e.ProjectID) == "" {
		return errors.New("usage event session and project identifiers are required")
	}
	if !e.CapabilityType.Valid() {
		return fmt.Errorf("invalid usage capability type %q", e.CapabilityType)
	}
	if strings.TrimSpace(e.CapabilityName) == "" {
		return errors.New("usage capability name is required")
	}
	if !e.EventType.Valid() {
		return fmt.Errorf("invalid event type %q", e.EventType)
	}
	return nil
}

// FingerprintForUsageEvent returns a stable identity derived only from the
// metadata fields that define an event. A supplied fingerprint is preserved by
// Store, but adapters can use this function to create one deterministically.
func FingerprintForUsageEvent(e UsageEvent) (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	canonical := strings.Join([]string{
		e.Timestamp.UTC().Format(time.RFC3339Nano), string(e.Runtime), e.SessionID,
		e.ProjectID, string(e.CapabilityType), e.CapabilityName, string(e.EventType),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeTimestamp provides one canonical UTC representation for persistence.
func NormalizeTimestamp(t time.Time) time.Time { return t.UTC() }
