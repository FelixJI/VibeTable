package mutation

import "context"

type businessReplayScopeKey struct{}

// WithBusinessReplay identifies an enclosing business operation whose entire
// durable effect is this single mutation. Only a verified kernel replay can
// abort that exact kind/key; unrelated nested writes retain their proof duty.
// This is an internal Go composition contract, never a renderer parameter.
func WithBusinessReplay(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, businessReplayScopeKey{}, kind)
}
