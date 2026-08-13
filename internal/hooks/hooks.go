// Package hooks manages the user-level lifecycle hook configuration used by
// the supported runtimes. It deliberately has no CLI wiring and never starts
// a configured hook. The manager only resolves the harness-lint executable
// with LookPath and edits the runtime's JSON configuration structurally.
package hooks

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runtime identifies a runtime-specific hook configuration format.
type Runtime string

const (
	RuntimeClaude Runtime = "claude-code"
	RuntimeCodex  Runtime = "codex"
)

// Action is an operation that can be previewed with DryRun.
type Action string

const (
	ActionInstall   Action = "install"
	ActionUninstall Action = "uninstall"
)

// StatusCode is the high-level state of a runtime hook configuration.
type StatusCode string

const (
	StatusInstalled             StatusCode = "installed"
	StatusNotInstalled          StatusCode = "not installed"
	StatusPartiallyInstalled    StatusCode = "partially installed"
	StatusMalformed             StatusCode = "malformed"
	StatusConfigurationNotFound StatusCode = "configuration not found"
	StatusUnsupported           StatusCode = "unsupported"
)

// ManagedEntryState describes one event's owned entry state.
type ManagedEntryState string

const (
	ManagedEntryInstalled    ManagedEntryState = "installed"
	ManagedEntryNotInstalled ManagedEntryState = "not installed"
	ManagedEntryPartial      ManagedEntryState = "partial"
	ManagedEntryStale        ManagedEntryState = "stale"
)

// BinaryResolution records PATH lookup without exposing an execution path in
// the generated hook command. A resolved path is useful diagnostics only;
// the configuration always invokes the stable PATH name harness-lint.
type BinaryResolution struct {
	Name         string `json:"name"`
	Resolved     bool   `json:"resolved"`
	ResolvedPath string `json:"resolvedPath,omitempty"`
	Error        string `json:"error,omitempty"`
}

// ManagedEntry describes the owned state for one event group.
type ManagedEntry struct {
	Event         string            `json:"event"`
	State         ManagedEntryState `json:"state"`
	ExactHandlers int               `json:"exactHandlers"`
	Partial       int               `json:"partialHandlers"`
	Lookalikes    int               `json:"lookalikeHandlers,omitempty"`
}

// TrustReview describes the trust boundary that the manager cannot alter.
// Codex user hooks need explicit review/trust in the Codex UI before they
// execute; writing hooks.json cannot grant that trust.
type TrustReview struct {
	Required   bool   `json:"required"`
	Limitation string `json:"limitation,omitempty"`
}

// StatusReport is a read-only configuration snapshot. Status methods return a
// report for malformed and missing configurations rather than turning those
// expected states into opaque Go errors.
type StatusReport struct {
	Runtime        Runtime           `json:"runtime"`
	Code           StatusCode        `json:"status"`
	ConfigPath     string            `json:"configPath"`
	ConfigExists   bool              `json:"configExists"`
	Managed        ManagedEntryState `json:"managed"`
	ManagedEntries []ManagedEntry    `json:"managedEntries"`
	Binary         BinaryResolution  `json:"binary"`
	InlineHooks    bool              `json:"inlineHooks,omitempty"`
	TrustReview    TrustReview       `json:"trustReview"`
	Warnings       []string          `json:"warnings,omitempty"`
}

// Change is one deterministic step in an operation or dry-run plan.
type Change struct {
	Kind   string `json:"kind"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail"`
}

// OperationResult describes an install, uninstall, or dry-run operation.
// Changed is false for a no-op and always false for DryRun; WouldChange
// communicates a dry-run's intended mutation without touching the filesystem.
type OperationResult struct {
	Runtime     Runtime      `json:"runtime"`
	Action      Action       `json:"action"`
	Status      StatusReport `json:"status"`
	Changed     bool         `json:"changed"`
	WouldChange bool         `json:"wouldChange"`
	Summary     string       `json:"summary"`
	Plan        []Change     `json:"plan,omitempty"`
	BackupPath  string       `json:"backupPath,omitempty"`
}

// Options controls a manager. ConfigRoot is a runtime configuration
// directory, not a direct path to the JSON file. Tests should always provide
// a temporary ConfigRoot; an empty root is unsupported and never falls back
// to the caller's HOME.
type Options struct {
	ConfigRoot string
	LookPath   func(string) (string, error)
}

// Manager is the runtime-neutral hook configuration manager interface.
type Manager interface {
	Status(context.Context) (StatusReport, error)
	Install(context.Context) (OperationResult, error)
	Uninstall(context.Context) (OperationResult, error)
	DryRun(context.Context, Action) (OperationResult, error)
}

const (
	// BinaryName is intentionally a PATH-based name. No temporary Go test or
	// development path is ever written into a runtime configuration.
	BinaryName     = "harness-lint"
	ManagedOwner   = "harness-lint-hooks"
	ManagedVersion = "v1"

	// ManagedMarker is the versioned ownership marker passed to the stable
	// machine API. Changing this marker intentionally leaves older entries
	// visible as stale/partial rather than silently deleting them.
	ManagedMarker = ManagedOwner + "/" + ManagedVersion
	ManagedFlag   = "--managed-by"

	claudeConfigName = "settings.json"
	codexConfigName  = "hooks.json"
	hookTimeout      = 10
)

var (
	errUnsupportedRuntime = errors.New("unsupported hook runtime")
	// ErrBinaryUnresolved is returned before any install mutation when the
	// stable PATH command cannot be resolved.
	ErrBinaryUnresolved = errors.New("harness-lint is not resolvable on PATH")
	errBinaryUnresolved = ErrBinaryUnresolved
	// ErrMalformedConfiguration is returned by mutations when parsing or
	// structurally validating the existing JSON fails. Status remains useful as
	// a read-only diagnostic and reports the malformed state without an error.
	ErrMalformedConfiguration = errors.New("malformed hook configuration")
)

// New constructs a runtime-neutral manager. Runtime must be RuntimeClaude or
// RuntimeCodex; use NewClaude/NewCodex when a runtime-specific constructor is
// more readable.
func New(runtime Runtime, options Options) Manager {
	return newManager(runtime, options)
}

// NewClaude constructs the Claude Code structural manager.
func NewClaude(options Options) Manager { return newManager(RuntimeClaude, options) }

// NewCodex constructs the Codex structural manager.
func NewCodex(options Options) Manager { return newManager(RuntimeCodex, options) }

type manager struct {
	runtime    Runtime
	configRoot string
	configPath string
	lookPath   func(string) (string, error)
}

var _ Manager = (*manager)(nil)

func newManager(runtime Runtime, options Options) *manager {
	root := strings.TrimSpace(options.ConfigRoot)
	if root != "" {
		root = filepath.Clean(root)
	}
	name := ""
	switch runtime {
	case RuntimeClaude:
		name = claudeConfigName
	case RuntimeCodex:
		name = codexConfigName
	}
	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path := ""
	if root != "" && name != "" {
		path = filepath.Join(root, name)
	}
	return &manager{runtime: runtime, configRoot: root, configPath: path, lookPath: lookPath}
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
