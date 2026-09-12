package analysis

import (
	"context"
	"fmt"
)

// EventFunc receives one job-activity-log line ("VLM: trying gemini-...",
// "... failed, falling back"). Providers are given one via the context (not a
// method parameter) so the Provider interface itself doesn't need to know
// about job event logs — engine wires it in per-call via WithEvents.
type EventFunc func(level, message string)

type eventCtxKey struct{}

// WithEvents attaches an EventFunc to ctx. A nil fn is a no-op (events just
// aren't emitted) rather than a panic, so callers that don't care can skip it.
func WithEvents(ctx context.Context, fn EventFunc) context.Context {
	return context.WithValue(ctx, eventCtxKey{}, fn)
}

func emitEvent(ctx context.Context, level, format string, args ...any) {
	fn, _ := ctx.Value(eventCtxKey{}).(EventFunc)
	if fn == nil {
		return
	}
	fn(level, fmt.Sprintf(format, args...))
}
