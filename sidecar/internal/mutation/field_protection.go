package mutation

import (
	"context"

	"github.com/pocketbase/pocketbase/core"
	"github.com/vibetable/vibetable/sidecar/internal/fieldprotection"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
	"github.com/vibetable/vibetable/sidecar/internal/schemaexecution"
)

// Persistent field protection (#591): the kernel refuses every write to a
// field the workspace manager protected unless the write belongs to the
// owning plugin installation's own transaction. The registry is loaded once
// per business transaction so a concurrent enable/release is serialized by
// the host transaction, and a corrupt registry fails closed.

type protectedFieldScope struct {
	app      core.App
	registry *fieldprotection.Registry
	writer   *fieldprotection.Owner
	err      error
}

type protectedFieldKey struct{}

// WithProtectedFieldWriter binds an authenticated plugin surface to the
// business transaction. Trusted host callers only: the owner must come from a
// surface whose installation the host already verified, never from JSON. The
// kernel still verifies that this owner is the registered owner of every
// protected field it writes.
func WithProtectedFieldWriter(ctx context.Context, app core.App, writer *fieldprotection.Owner) context.Context {
	return context.WithValue(ctx, protectedFieldKey{}, &protectedFieldScope{app: app, writer: writer})
}

// planProtectedFieldWriter returns the single writer shared by all plan
// requests; mixed writers are not a legitimate transaction.
func planProtectedFieldWriter(requests []Request) (*fieldprotection.Owner, error) {
	if len(requests) == 0 {
		return nil, nil
	}
	writer := requests[0].ProtectedFieldWriter
	for _, request := range requests[1:] {
		next := request.ProtectedFieldWriter
		if (writer == nil) != (next == nil) || writer != nil && *writer != *next {
			return nil, mutationError("transaction.plan_invalid", nil, "plan requests must share one protected field writer", nil, false)
		}
	}
	return writer, nil
}

func protectedFields(ctx context.Context, app core.App) (*fieldprotection.Registry, *fieldprotection.Owner, error) {
	scope, _ := ctx.Value(protectedFieldKey{}).(*protectedFieldScope)
	if scope != nil && scope.app == app {
		if scope.registry == nil && scope.err == nil {
			scope.registry, scope.err = fieldprotection.Load(ctx, app)
		}
		if scope.err != nil {
			return nil, scope.writer, protectionUnavailable(scope.err)
		}
		return scope.registry, scope.writer, nil
	}
	registry, err := fieldprotection.Load(ctx, app)
	if err != nil {
		return nil, nil, protectionUnavailable(err)
	}
	return registry, nil, nil
}

func protectionUnavailable(err error) error {
	return mutationError("mutation.field_protection.unavailable", nil,
		"field protection state could not be loaded", nil, true)
}

func protectedFieldError(entry fieldprotection.Entry, field v2.FieldDefinition) error {
	return mutationError("mutation.field_protected", stringPointer(field.Identity.PhysicalName),
		"field is protected by a plugin", map[string]any{
			"tableId": entry.TableID, "fieldId": entry.FieldID,
			"pluginId": entry.PluginID, "installationId": entry.InstallationID,
		}, false)
}

// checkProtectedWrite rejects a changed protected value. Inserts are only
// refused when the caller explicitly supplied the field; schema defaults stay
// possible so protected fields never make a table un-creatable. An exact
// no-op is always allowed.
func checkProtectedWrite(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
	before, after map[string]any,
	input Operation,
) error {
	registry, writer, err := protectedFields(ctx, app)
	if err != nil || registry.Count() == 0 {
		return err
	}
	for _, field := range definition.Snapshot.Fields {
		entry, protected := registry.Protected(definition.Snapshot.TableID, field.Identity.FieldID)
		if !protected || fieldprotection.Allows(entry, writer) {
			continue
		}
		physical := field.Identity.PhysicalName
		if before == nil {
			if !explicitlySupplied(input, field) || after[physical] == nil {
				continue
			}
		} else if productValuesEqual(before[physical], after[physical]) {
			continue
		}
		return protectedFieldError(entry, field)
	}
	return nil
}

// checkProtectedDelete refuses to delete a row that still carries a
// protected value; releasing protection is the explicit manager action.
func checkProtectedDelete(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
	before map[string]any,
) error {
	registry, writer, err := protectedFields(ctx, app)
	if err != nil || registry.Count() == 0 {
		return err
	}
	for _, field := range definition.Snapshot.Fields {
		entry, protected := registry.Protected(definition.Snapshot.TableID, field.Identity.FieldID)
		if !protected || fieldprotection.Allows(entry, writer) || before[field.Identity.PhysicalName] == nil {
			continue
		}
		return protectedFieldError(entry, field)
	}
	return nil
}

// RefuseProtectedField reports, before any byte is staged or written, that a
// field accepts writes from its registered owner only. It shares the
// per-transaction registry cache and the bound writer with the write checks,
// so preview, staging and apply answer identically.
func RefuseProtectedField(ctx context.Context, app core.App, definition schemaexecution.Table, identity string) error {
	registry, writer, err := protectedFields(ctx, app)
	if err != nil || registry.Count() == 0 {
		return err
	}
	field, found := definition.Field(identity)
	if !found {
		return nil
	}
	entry, protected := registry.Protected(definition.Snapshot.TableID, field.Identity.FieldID)
	if protected && !fieldprotection.Allows(entry, writer) {
		return protectedFieldError(entry, field)
	}
	return nil
}

// checkProtectedAttachment refuses attachment changes on a protected file
// field before any file is staged.
func checkProtectedAttachment(
	ctx context.Context,
	app core.App,
	definition schemaexecution.Table,
	identity string,
) error {
	return RefuseProtectedField(ctx, app, definition, identity)
}

// checkProtectedReciprocal refuses a derived write to a protected reciprocal
// relation field in the target table; only its owner may change it.
func checkProtectedReciprocal(
	ctx context.Context,
	app core.App,
	target schemaexecution.Table,
	reciprocal v2.FieldDefinition,
) error {
	registry, writer, err := protectedFields(ctx, app)
	if err != nil {
		return err
	}
	entry, protected := registry.Protected(target.Snapshot.TableID, reciprocal.Identity.FieldID)
	if protected && !fieldprotection.Allows(entry, writer) {
		return protectedFieldError(entry, reciprocal)
	}
	return nil
}

func explicitlySupplied(input Operation, field v2.FieldDefinition) bool {
	for _, values := range []map[string]any{input.Values, input.RawValues} {
		_, byID := values[field.Identity.FieldID]
		_, byName := values[field.Identity.PhysicalName]
		if byID || byName {
			return true
		}
	}
	return false
}
