package app

import "context"

type businessWriteGate func(
	context.Context,
	string,
	string,
	func(context.Context) error,
) error

func runBusinessWrite(
	ctx context.Context,
	gates []businessWriteGate,
	kind string,
	identity string,
	apply func(context.Context) error,
) error {
	if len(gates) == 0 || gates[0] == nil {
		return apply(ctx)
	}
	return gates[0](ctx, kind, identity, apply)
}

func runIdempotentBusinessWrite(
	ctx context.Context,
	gates []businessWriteGate,
	kind string,
	identity string,
	apply func(context.Context) error,
) error {
	if len(gates) < 2 || gates[1] == nil {
		return runBusinessWrite(ctx, gates, kind, identity, apply)
	}
	return gates[1](ctx, kind, identity, apply)
}
