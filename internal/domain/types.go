// Package domain contains runtime-neutral values shared by adapters and
// analysis. It deliberately contains no persistence or runtime-specific code.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
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

// Provenance identifies the evidence path that produced a usage event. The
// values are deliberately extensible so a future producer can add a new
// evidence path without changing the event shape.
type Provenance string

const (
	ProvenanceHook       Provenance = "hook"
	ProvenanceTranscript Provenance = "transcript"
	ProvenanceImport     Provenance = "import"
)

func (p Provenance) Valid() bool {
	switch p {
	case ProvenanceHook, ProvenanceTranscript, ProvenanceImport:
		return true
	default:
		return false
	}
}

// InvocationOrigin records how an invocation was selected. Unknown is the
// honest value when an evidence source cannot distinguish model selection from
// an explicit user request.
type InvocationOrigin string

const (
	InvocationOriginUnknown       InvocationOrigin = "unknown"
	InvocationOriginModelSelected InvocationOrigin = "model_selected"
	InvocationOriginUserExplicit  InvocationOrigin = "user_explicit"
)

func (o InvocationOrigin) Valid() bool {
	switch o {
	case InvocationOriginUnknown, InvocationOriginModelSelected, InvocationOriginUserExplicit:
		return true
	default:
		return false
	}
}

// CurrentUsageEventSchemaVersion is the schema version of the normalized
// ingestion contract. It is separate from the SQLite schema version so an
// event can declare the contract it was produced against.
const CurrentUsageEventSchemaVersion = 1

// UsageEventSchemaVersion is a concise alias for callers that do not need to
// distinguish the current value from the event type's name.
const UsageEventSchemaVersion = CurrentUsageEventSchemaVersion

// NormalizeSourceIdentity returns the one-way hash of an opaque, safe runtime
// identity used for delivery deduplication. Empty identities remain empty so
// callers can deliberately use the conservative fallback fingerprint.
func NormalizeSourceIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) == sha256.Size*2 {
		if _, err := hex.DecodeString(value); err == nil {
			return strings.ToLower(value)
		}
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
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

// Measurement carries a size quantity together with its provenance. Adapters
// use it for metadata or body exposure estimates; it does not represent an
// exact runtime context cost. Unknown measurements use Value zero and
// ConfidenceUnknown.
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
		{name: "metadata tokens", value: c.MetadataTokens},
		{name: "body tokens", value: c.BodyTokens},
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

// UsageEvent is a metadata-only usage observation imported from a runtime.
// ObservedAt is always the local receive/import time. SourceTimestamp is only
// populated when the source declares a trustworthy occurrence time; callers
// must never substitute it for ObservedAt.
type UsageEvent struct {
	ObservedAt       time.Time
	SourceTimestamp  *time.Time
	Runtime          Runtime
	SessionID        string
	ProjectID        string
	CapabilityType   CapabilityType
	CapabilityName   string
	EventType        EventType
	Provenance       Provenance
	InvocationOrigin InvocationOrigin
	SchemaVersion    int
	SourceIdentity   string
	Fingerprint      string
}

func (e UsageEvent) Validate() error {
	if e.ObservedAt.IsZero() {
		return errors.New("usage event observed-at time is required")
	}
	if e.SourceTimestamp != nil && e.SourceTimestamp.IsZero() {
		return errors.New("usage event source timestamp cannot be zero")
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
	if !e.Provenance.Valid() {
		return fmt.Errorf("invalid usage provenance %q", e.Provenance)
	}
	if !e.InvocationOrigin.Valid() {
		return fmt.Errorf("invalid invocation origin %q", e.InvocationOrigin)
	}
	if e.SchemaVersion <= 0 {
		return errors.New("usage event schema version must be positive")
	}
	return nil
}

// EffectiveActivityTime returns the single timestamp used by stale/history
// analysis: a trustworthy source occurrence time when present, otherwise the
// local observed time. It never mutates either timestamp.
func (e UsageEvent) EffectiveActivityTime() time.Time {
	if e.SourceTimestamp != nil && !e.SourceTimestamp.IsZero() {
		return e.SourceTimestamp.UTC()
	}
	return e.ObservedAt.UTC()
}

// NormalizeIdentifier returns a stable one-way representation suitable for
// persistence. Existing SHA-256 hex values are treated as already normalized;
// all other values are hashed after trimming whitespace.
func NormalizeIdentifier(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("identifier is required")
	}
	if len(value) == sha256.Size*2 {
		if _, err := hex.DecodeString(value); err == nil {
			return strings.ToLower(value), nil
		}
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeUsageEvent returns the persistence-safe form of an event. Session
// and project identifiers are one-way hashed here as a defensive boundary even
// though runtime adapters already normalize them before constructing events.
func NormalizeUsageEvent(e UsageEvent) (UsageEvent, error) {
	if err := e.Validate(); err != nil {
		return UsageEvent{}, err
	}
	var err error
	e.ObservedAt = e.ObservedAt.UTC()
	if e.SourceTimestamp != nil {
		sourceTimestamp := e.SourceTimestamp.UTC()
		e.SourceTimestamp = &sourceTimestamp
	}
	e.SessionID, err = NormalizeIdentifier(e.SessionID)
	if err != nil {
		return UsageEvent{}, fmt.Errorf("normalize usage session identifier: %w", err)
	}
	e.ProjectID, err = NormalizeIdentifier(e.ProjectID)
	if err != nil {
		return UsageEvent{}, fmt.Errorf("normalize usage project identifier: %w", err)
	}
	e.SourceIdentity = NormalizeSourceIdentity(e.SourceIdentity)
	return e, nil
}

// FingerprintForUsageEvent returns a stable identity derived only from safe
// event metadata. Stable source identities drive deduplication; without one,
// a trustworthy source timestamp anchors rescans, otherwise local observation
// time anchors the conservative fallback so repeated calls are not collapsed
// merely because their runtime, session, and capability match. Prompts,
// responses, tool payloads, commands, file contents, and raw paths are never
// fingerprint inputs.
func FingerprintForUsageEvent(e UsageEvent) (string, error) {
	normalized, err := NormalizeUsageEvent(e)
	if err != nil {
		return "", err
	}
	canonicalParts := []string{
		"harness-lint-usage-event",
		strconv.Itoa(normalized.SchemaVersion),
		string(normalized.Runtime),
		string(normalized.CapabilityType),
		normalized.CapabilityName,
		string(normalized.EventType),
	}
	if normalized.SourceIdentity != "" {
		// Provenance is deliberately omitted here: a transcript and a direct
		// delivery capture with the same stable runtime identity describe the
		// same delivery and must not inflate counts.
		canonicalParts = append(canonicalParts, "source", normalized.SourceIdentity, normalized.SessionID, normalized.ProjectID)
	} else if normalized.SourceTimestamp != nil && !normalized.SourceTimestamp.IsZero() {
		// A trustworthy source time is enough to identify a transcript event
		// across rescans with different local observation times. Keep the
		// provenance and invocation origin in scope for conservative fallback
		// behavior when the source provides no stable delivery identity.
		canonicalParts = append(canonicalParts,
			"fallback-source",
			normalized.SourceTimestamp.Format(time.RFC3339Nano),
			normalized.SessionID,
			normalized.ProjectID,
			string(normalized.Provenance),
			string(normalized.InvocationOrigin),
		)
	} else {
		canonicalParts = append(canonicalParts,
			"fallback-observed",
			normalized.ObservedAt.Format(time.RFC3339Nano),
			normalized.SessionID,
			normalized.ProjectID,
			string(normalized.Provenance),
			string(normalized.InvocationOrigin),
		)
	}
	canonical := strings.Join(canonicalParts, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeTimestamp provides one canonical UTC representation for persistence.
func NormalizeTimestamp(t time.Time) time.Time { return t.UTC() }
