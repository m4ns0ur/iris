package tool

import "context"

// Environment is the subset of the execution boundary exposed to tools. It
// lets custom tools resolve paths through the same workspace policy as the
// built-in file tools.
type Environment interface {
	Workdir() string
	ResolveRead(path string) (string, error)
	ResolveWrite(path string) (string, error)
}

type environmentKey struct{}

// WithEnvironment attaches the active execution environment to a tool context.
func WithEnvironment(ctx context.Context, environment Environment) context.Context {
	return context.WithValue(ctx, environmentKey{}, environment)
}

// FromContext retrieves the active execution environment.
func FromContext(ctx context.Context) (Environment, bool) {
	environment, ok := ctx.Value(environmentKey{}).(Environment)
	return environment, ok && environment != nil
}
