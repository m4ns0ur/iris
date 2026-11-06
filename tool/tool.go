// Package tool defines the stable tool interface.
package tool

import (
	"context"

	"github.com/m4ns0ur/iris/provider"
)

// Tool is a callable capability exposed to the agent runtime.
type Tool interface {
	// Name is the unique tool name used by the provider.
	Name() string

	// Description is a short human-readable description.
	Description() string

	// FullDescription is used in the system prompt.
	FullDescription() string

	// Parameters describes the JSON arguments accepted by Execute.
	Parameters() provider.Parameters

	// Execute runs the tool. Implementations should honour context
	// cancellation.
	Execute(ctx context.Context, args map[string]any) (map[string]any, error)
}

// Registry provides read access to a set of tools.
type Registry interface {
	Get(name string) Tool
	Tools() []Tool
}
