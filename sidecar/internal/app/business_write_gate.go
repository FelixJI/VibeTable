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
