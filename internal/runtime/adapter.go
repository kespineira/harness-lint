// Package runtime defines the boundary implemented by runtime-specific
// adapters. Adapters own configuration and file paths; the core only receives
// normalized domain values.
package runtime

import (
	"context"
	"time"

	"github.com/kespineira/harness-lint/internal/domain"
)

// Adapter discovers installed inventory and imports metadata-only usage. The
// since boundary is inclusive; adapters should normalize returned timestamps
// to UTC and must not return prompt, response, conversation, or tool payloads.
type Adapter interface {
	Runtime() domain.Runtime
	Discover(ctx context.Context) (domain.Discovery, error)
	ImportUsage(ctx context.Context, since time.Time) ([]domain.UsageEvent, error)
}
