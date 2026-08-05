package fieldchange

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const defaultPlanTTL = 30 * time.Minute

var ErrFieldNotFound = errors.New("field not found")

type Revisions struct {
	Schema string
	Data   int64
}

type Source interface {
	Revisions(ctx context.Context, tableID string) (Revisions, error)
	Field(ctx context.Context, tableID string, fieldID string) (*v2.FieldDefinition, error)
}

type Preflight interface {
	Check(
		ctx context.Context,
		intent v2.FieldChangeIntent,
		before *v2.FieldDefinition,
		after *v2.FieldDefinition,
		classes []v2.ChangeClass,
	) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error)
}

type PlanStore interface {
	FindActive(ctx context.Context, intentHash string, now time.Time) (*v2.FieldChangePlan, error)
	Save(
		ctx context.Context,
		intentHash string,
		now time.Time,
		plan v2.FieldChangePlan,
	) (v2.FieldChangePlan, error)
	Load(ctx context.Context, planID string) (*v2.FieldChangePlan, error)
	MarkApplied(ctx context.Context, planID string, operationID string) error
}

type Planner struct {
	source    Source
	preflight Preflight
	store     PlanStore
	allocator *v2.IdentityAllocator
	clock     func() time.Time
	random    io.Reader
	logger    *slog.Logger
	ttl       time.Duration
}

type Option func(*Planner)

func WithClock(clock func() time.Time) Option {
	return func(planner *Planner) {
		planner.clock = clock
	}
}

func WithTTL(ttl time.Duration) Option {
	return func(planner *Planner) {
		planner.ttl = ttl
	}
}

func WithPlannerLogger(logger *slog.Logger) Option {
	return func(planner *Planner) {
		if logger != nil {
			planner.logger = logger
		}
	}
}

func NewPlanner(
	source Source,
	preflight Preflight,
	store PlanStore,
	allocator *v2.IdentityAllocator,
	options ...Option,
) *Planner {
	if preflight == nil {
		preflight = NoopPreflight{}
	}
	if store == nil {
		store = NewMemoryPlanStore()
	}
	if allocator == nil {
		allocator = v2.NewIdentityAllocator(nil)
	}
	planner := &Planner{
		source: source, preflight: preflight, store: store, allocator: allocator,
		clock: time.Now, random: rand.Reader, ttl: defaultPlanTTL,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, option := range options {
		option(planner)
	}
	return planner
}

func (planner *Planner) Plan(
	ctx context.Context,
	intent v2.FieldChangeIntent,
) (v2.FieldChangePlan, error) {
	started := time.Now()
	plan, err := planner.plan(ctx, intent)
	attributes := []any{
		"event", "field_change_planned",
		"table_id", intent.TableID,
		"field_id", intent.FieldID,
		"action", intent.Action,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if err != nil {
		attributes[1] = "field_change_rejected"
		planner.logger.Warn(
			"field change plan rejected",
			append(attributes, "outcome", "rejected", "error", err)...,
		)
		return v2.FieldChangePlan{}, err
	}
	planner.logger.Info(
		"field change planned",
		append(
			attributes,
			"outcome", "planned",
			"plan_id", plan.PlanID,
			"classes", fmt.Sprint(plan.Classes),
			"records_scanned", plan.Impact.Records,
			"failures", len(plan.Impact.Failures),
			"can_apply", plan.CanApply,
		)...,
	)
	return plan, nil
}

func (planner *Planner) plan(
	ctx context.Context,
	intent v2.FieldChangeIntent,
) (v2.FieldChangePlan, error) {
	if err := validateIntentShape(intent); err != nil {
		return v2.FieldChangePlan{}, err
	}
	revisions, err := planner.source.Revisions(ctx, intent.TableID)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	if revisions.Schema != intent.ExpectedSchemaRev {
		return v2.FieldChangePlan{}, productError(
			"field.change.schema_conflict", "expectedSchemaRevision",
			"schema revision changed",
			map[string]any{"expected": intent.ExpectedSchemaRev, "actual": revisions.Schema},
		)
	}
	if intent.ExpectedDataRevision != nil && revisions.Data != *intent.ExpectedDataRevision {
		return v2.FieldChangePlan{}, productError(
			"field.change.data_conflict", "expectedDataRevision",
			"data revision changed",
			map[string]any{"expected": *intent.ExpectedDataRevision, "actual": revisions.Data},
		)
	}
	intentHash, err := canonicalHash(intent)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	now := planner.clock()
	if existing, err := planner.store.FindActive(ctx, intentHash, now); err != nil {
		return v2.FieldChangePlan{}, err
	} else if existing != nil {
		return *existing, nil
	}

	before, after, err := planner.normalize(ctx, intent, now)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	relatedChanges, err := planner.normalizeRelated(ctx, intent, before, after, now)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	classes := classify(intent.Action, before, after, intent.ConversionRule)
	if len(classes) == 0 {
		return v2.FieldChangePlan{}, productError(
			"field.change.noop", "draft",
			"field settings are unchanged", nil,
		)
	}
	impact, warnings, diagnostics, err := planner.preflight.Check(
		ctx, intent, before, after, classes,
	)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	for _, related := range relatedChanges {
		relatedIntent := intent
		relatedIntent.TableID = related.TableID
		relatedIntent.FieldID = related.FieldID
		relatedIntent.RelationPair = nil
		relatedImpact, relatedWarnings, relatedDiagnostics, relatedErr :=
			planner.preflight.Check(
				ctx, relatedIntent, related.Before, related.After, classes,
			)
		if relatedErr != nil {
			return v2.FieldChangePlan{}, relatedErr
		}
		impact = mergeImpact(impact, relatedImpact)
		warnings = append(warnings, relatedWarnings...)
		diagnostics = append(diagnostics, relatedDiagnostics...)
	}
	if impact.Records == 0 &&
		intent.Action != v2.ActionBackfill &&
		containsClass(classes, v2.ClassMigration) {
		// With no values to copy, the existing transactional schema replacement
		// is the whole operation. Keep public identity stable and do not create
		// an empty shadow-migration job.
		classes = []v2.ChangeClass{v2.ClassSchema}
	}
	if before != nil && after != nil &&
		intent.Action != v2.ActionBackfill &&
		containsClass(classes, v2.ClassMigration) &&
		after.Identity.ProviderFieldID == before.Identity.ProviderFieldID {
		// Every non-empty migration needs a distinct provider identity so the
		// executor can create a real shadow field beside the authoritative one.
		// Same-logical-type migrations (for example select option replacement)
		// otherwise alias the old field and silently copy into no schema field.
		frozen := cloneDefinition(after)
		providerFieldID, allocateErr := planner.allocator.AllocateProviderField(ctx)
		if allocateErr != nil {
			return v2.FieldChangePlan{}, allocateErr
		}
		frozen.Identity.ProviderFieldID = providerFieldID
		if frozen.Value.Presence.Mode == v2.PresenceCompanion {
			presenceProviderFieldID, presenceErr :=
				planner.allocator.AllocateProviderField(ctx)
			if presenceErr != nil {
				return v2.FieldChangePlan{}, presenceErr
			}
			frozen.Value.Presence.ProviderFieldID = presenceProviderFieldID
		}
		after = frozen
	}
	expectedDataRevision := intent.ExpectedDataRevision
	if expectedDataRevision == nil && dataSensitivePlan(intent.Action, classes) {
		frozen := revisions.Data
		expectedDataRevision = &frozen
		intent.ExpectedDataRevision = &frozen
	}
	planID, err := randomID(planner.random, "plan_", 20)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	confirmations := requiredConfirmations(intent.Action, before, after)
	if len(relatedChanges) != 0 &&
		(intent.Action == v2.ActionRetire || intent.Action == v2.ActionPurge) {
		confirmations = append(confirmations, "relationPair")
	}
	steps := planSteps(intent.Action, classes, before, after, intent.TableID)
	if len(relatedChanges) != 0 {
		steps = append(steps, v2.PlanStep{
			Kind: "applyRelationPair",
			Details: map[string]any{
				"tableId": relatedChanges[0].TableID,
				"fieldId": relatedChanges[0].FieldID,
				"atomic":  true,
			},
		})
	}
	plan := v2.FieldChangePlan{
		Contract: v2.Contract, PlanID: planID,
		ExpiresAt: planner.clock().Add(planner.ttl).UTC().Format(time.RFC3339Nano),
		Intent:    intent, Before: before, After: after, Classes: classes,
		ExpectedSchemaRev:    intent.ExpectedSchemaRev,
		ExpectedDataRevision: expectedDataRevision,
		Impact:               impact,
		Steps:                steps,
		Warnings:             warnings, Errors: diagnostics, Confirmations: confirmations,
		CreatesMigration: containsClass(classes, v2.ClassMigration),
		CanApply:         len(diagnostics) == 0,
		RelatedChanges:   relatedChanges,
	}
	planHash, err := hashPlan(plan)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	plan.PlanHash = planHash
	stored, err := planner.store.Save(ctx, intentHash, now, plan)
	if err != nil {
		return v2.FieldChangePlan{}, err
	}
	return stored, nil
}

func (planner *Planner) normalizeRelated(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
	now time.Time,
) ([]v2.RelatedFieldChange, error) {
	if intent.Action == v2.ActionCreate {
		if after == nil || after.LogicalType != v2.LogicalRelation {
			if intent.RelationPair != nil {
				return nil, productError(
					"field.contract.invalid", "relationPair",
					"relationPair is only allowed when creating a relation field", nil,
				)
			}
			return nil, nil
		}
		if intent.RelationPair == nil {
			return nil, productError(
				"field.relation.pair_required", "relationPair",
				"relation fields must create a reciprocal field", nil,
			)
		}
		pair := intent.RelationPair
		if strings.TrimSpace(pair.ReciprocalDisplayName) == "" ||
			(pair.ReciprocalCardinality != "one" &&
				pair.ReciprocalCardinality != "many") ||
			pair.SourceDisplayFieldID == "" {
			return nil, productError(
				"field.contract.invalid", "relationPair",
				"reciprocal relation settings are incomplete", nil,
			)
		}
		revisions, err := planner.source.Revisions(ctx, after.Relation.TargetTableID)
		if err != nil {
			return nil, err
		}
		reverseDraft := cloneDraft(intent.Draft)
		reverseDraft.DisplayName = strings.TrimSpace(pair.ReciprocalDisplayName)
		reverseDraft.Relation = &v2.RelationSpec{
			TargetTableID: intent.TableID,
			Cardinality:   pair.ReciprocalCardinality,
			DeletePolicy:  after.Relation.DeletePolicy,
			DisplayField:  pair.SourceDisplayFieldID,
		}
		reverse, err := planner.definitionFromDraft(
			ctx, v2.FieldIdentity{}, nil, &reverseDraft,
		)
		if err != nil {
			return nil, err
		}
		pairID, err := planner.allocator.AllocateRelationPair(ctx)
		if err != nil {
			return nil, err
		}
		after.Relation.PairID = pairID
		after.Relation.ReciprocalFieldID = reverse.Identity.FieldID
		reverse.Relation.PairID = pairID
		reverse.Relation.ReciprocalFieldID = after.Identity.FieldID
		if err := v2.Validate(*after); err != nil {
			return nil, err
		}
		if err := v2.Validate(*reverse); err != nil {
			return nil, err
		}
		return []v2.RelatedFieldChange{{
			TableID:                after.Relation.TargetTableID,
			FieldID:                reverse.Identity.FieldID,
			After:                  reverse,
			ExpectedSchemaRevision: revisions.Schema,
		}}, nil
	}
	if intent.RelationPair != nil {
		return nil, productError(
			"field.contract.invalid", "relationPair",
			"relationPair settings are only accepted during relation creation", nil,
		)
	}
	if before == nil || before.LogicalType != v2.LogicalRelation ||
		before.Relation == nil || before.Relation.PairID == "" {
		return nil, nil
	}
	if intent.Action != v2.ActionRetire && intent.Action != v2.ActionRestore &&
		intent.Action != v2.ActionPurge {
		return nil, nil
	}
	reverse, err := planner.source.Field(
		ctx, before.Relation.TargetTableID, before.Relation.ReciprocalFieldID,
	)
	if err != nil {
		return nil, productError(
			"field.relation.pair_broken", "fieldId",
			"reciprocal relation field is missing", nil,
		)
	}
	if reverse.LogicalType != v2.LogicalRelation || reverse.Relation == nil ||
		reverse.Relation.PairID != before.Relation.PairID ||
		reverse.Relation.ReciprocalFieldID != before.Identity.FieldID ||
		reverse.Relation.TargetTableID != intent.TableID {
		return nil, productError(
			"field.relation.pair_broken", "fieldId",
			"reciprocal relation metadata is inconsistent", nil,
		)
	}
	revisions, err := planner.source.Revisions(ctx, before.Relation.TargetTableID)
	if err != nil {
		return nil, err
	}
	var relatedAfter *v2.FieldDefinition
	if intent.Action != v2.ActionPurge {
		relatedAfter = cloneDefinition(reverse)
		if intent.Action == v2.ActionRetire {
			retiredAt := now.UTC().Format(time.RFC3339Nano)
			relatedAfter.Lifecycle = v2.Lifecycle{
				State: v2.LifecycleRetired, RetiredAt: &retiredAt,
			}
		} else {
			relatedAfter.Lifecycle = v2.Lifecycle{State: v2.LifecycleActive}
		}
	}
	return []v2.RelatedFieldChange{{
		TableID:                before.Relation.TargetTableID,
		FieldID:                reverse.Identity.FieldID,
		Before:                 reverse,
		After:                  relatedAfter,
		ExpectedSchemaRevision: revisions.Schema,
	}}, nil
}

func mergeImpact(left v2.Impact, right v2.Impact) v2.Impact {
	left.Records += right.Records
	left.Missing += right.Missing
	left.Ambiguous += right.Ambiguous
	left.Failures = append(left.Failures, right.Failures...)
	left.Dependencies = append(left.Dependencies, right.Dependencies...)
	return left
}

func (planner *Planner) normalize(
	ctx context.Context,
	intent v2.FieldChangeIntent,
	now time.Time,
) (*v2.FieldDefinition, *v2.FieldDefinition, error) {
	var before *v2.FieldDefinition
	if intent.Action != v2.ActionCreate {
		existing, err := planner.source.Field(ctx, intent.TableID, intent.FieldID)
		if err != nil {
			return nil, nil, err
		}
		before = existing
	}
	switch intent.Action {
	case v2.ActionCreate:
		after, err := planner.definitionFromDraft(ctx, v2.FieldIdentity{}, nil, intent.Draft)
		return nil, after, err
	case v2.ActionUpdate:
		if before == nil {
			return nil, nil, ErrFieldNotFound
		}
		if before.LogicalType != intent.Draft.LogicalType {
			return nil, nil, productError(
				"field.capability.unsupported", "draft.logicalType",
				"logical type change requires convert action", nil,
			)
		}
		after, err := planner.definitionFromDraft(ctx, before.Identity, before, intent.Draft)
		if err != nil {
			return nil, nil, err
		}
		if intent.ConversionRule != "" {
			if err := applySelectOptionDeletion(
				before, after, intent.Draft, intent.ConversionRule,
			); err != nil {
				return nil, nil, err
			}
		}
		return before, after, nil
	case v2.ActionConvert:
		if before == nil {
			return nil, nil, ErrFieldNotFound
		}
		if before.LogicalType == intent.Draft.LogicalType {
			return nil, nil, productError(
				"field.change.noop", "draft.logicalType", "conversion target is unchanged", nil,
			)
		}
		capability, capabilityErr := v2.CapabilityFor(before.LogicalType)
		if capabilityErr != nil {
			return nil, nil, capabilityErr
		}
		if !containsLogicalType(capability.ConversionTargets, intent.Draft.LogicalType) {
			return nil, nil, productError(
				"field.capability.unsupported", "draft.logicalType",
				"conversion target is not supported for this field type", nil,
			)
		}
		if intent.ConversionRule != "" &&
			!containsString(capability.ConversionRules, intent.ConversionRule) {
			return nil, nil, productError(
				"field.capability.unsupported", "conversionRule",
				"conversion rule is not supported for this field type", nil,
			)
		}
		if conversionRuleRequired(before.LogicalType, intent.Draft.LogicalType) &&
			intent.ConversionRule == "" {
			return nil, nil, productError(
				"field.conversion.rule_required", "conversionRule",
				"this conversion requires an explicit conversion rule", nil,
			)
		}
		after, err := planner.definitionFromDraft(ctx, before.Identity, before, intent.Draft)
		return before, after, err
	case v2.ActionRetire:
		if before.Lifecycle.State == v2.LifecycleRetired {
			return nil, nil, productError(
				"field.change.noop", "action", "field is already retired", nil,
			)
		}
		after := cloneDefinition(before)
		retiredAt := now.UTC().Format(time.RFC3339Nano)
		after.Lifecycle = v2.Lifecycle{State: v2.LifecycleRetired, RetiredAt: &retiredAt}
		return before, after, nil
	case v2.ActionRestore:
		if before.Lifecycle.State == v2.LifecycleActive {
			return nil, nil, productError(
				"field.change.noop", "action", "field is already active", nil,
			)
		}
		after := cloneDefinition(before)
		after.Lifecycle = v2.Lifecycle{State: v2.LifecycleActive}
		return before, after, nil
	case v2.ActionPurge:
		return before, nil, nil
	case v2.ActionBackfill:
		return before, cloneDefinition(before), nil
	default:
		return nil, nil, productError(
			"field.contract.invalid", "action", "unknown field action", nil,
		)
	}
}

func (planner *Planner) definitionFromDraft(
	ctx context.Context,
	identity v2.FieldIdentity,
	before *v2.FieldDefinition,
	draft *v2.FieldDraft,
) (*v2.FieldDefinition, error) {
	if draft == nil {
		return nil, productError("field.contract.invalid", "draft", "draft is required", nil)
	}
	if identity.FieldID == "" {
		allocated, err := planner.allocator.AllocateField(ctx)
		if err != nil {
			return nil, err
		}
		identity = allocated
	}
	converting := before != nil && (before.LogicalType != draft.LogicalType ||
		relationCardinalityChanges(before, draft))
	if converting {
		providerFieldID, err := planner.allocator.AllocateProviderField(ctx)
		if err != nil {
			return nil, err
		}
		// Product fieldId and physicalName are permanent. A conversion freezes
		// a fresh provider identity for the shadow value field.
		identity.ProviderFieldID = providerFieldID
	}
	normalizedDraft := cloneDraft(draft)
	if normalizedDraft.Select != nil {
		knownOptions := map[string]struct{}{}
		submittedOptions := map[string]struct{}{}
		if before != nil && before.Select != nil {
			for _, option := range before.Select.Options {
				knownOptions[option.OptionID] = struct{}{}
			}
		}
		for index := range normalizedDraft.Select.Options {
			optionID := normalizedDraft.Select.Options[index].OptionID
			if optionID == "" {
				allocated, err := planner.allocator.AllocateOption(ctx)
				if err != nil {
					return nil, err
				}
				normalizedDraft.Select.Options[index].OptionID = allocated
				continue
			}
			if before != nil {
				if _, exists := knownOptions[optionID]; !exists {
					return nil, productError(
						"field.contract.invalid",
						fmt.Sprintf("draft.select.options[%d].optionId", index),
						"new options must not provide an optionId",
						nil,
					)
				}
			}
			submittedOptions[optionID] = struct{}{}
		}
		if before != nil && before.Select != nil {
			for _, option := range before.Select.Options {
				if _, submitted := submittedOptions[option.OptionID]; submitted {
					continue
				}
				// Omitting an existing option is a lifecycle change, never a
				// physical deletion. Existing records keep their stable
				// optionId readable while new writes cannot select it.
				option.State = v2.OptionRetired
				normalizedDraft.Select.Options = append(
					normalizedDraft.Select.Options,
					option,
				)
			}
		}
	}
	presence := normalizedDraft.Value.Presence
	capability, err := v2.CapabilityFor(normalizedDraft.LogicalType)
	if err != nil {
		return nil, err
	}
	if capability.NeedsPresence {
		if before != nil && before.Value.Presence.Mode == v2.PresenceCompanion &&
			!converting {
			presence = before.Value.Presence
		} else {
			presence, err = planner.allocator.AllocatePresence(ctx, identity.PhysicalName)
			if err != nil {
				return nil, err
			}
			if converting &&
				before.Value.Presence.Mode == v2.PresenceCompanion {
				// The public presence physical name is stable. The migration
				// executor uses a temporary shadow name until atomic switch.
				presence.PhysicalName = before.Value.Presence.PhysicalName
			}
		}
	} else if normalizedDraft.LogicalType == v2.LogicalAutoDate ||
		normalizedDraft.LogicalType == v2.LogicalFormula ||
		normalizedDraft.LogicalType == v2.LogicalLookup {
		presence = v2.PresenceSpec{Mode: v2.PresenceComputed}
	} else {
		presence = v2.PresenceSpec{Mode: v2.PresenceNative}
	}
	normalizedDraft.Value.Presence = presence
	lifecycle := v2.Lifecycle{State: v2.LifecycleActive}
	if before != nil {
		lifecycle = before.Lifecycle
	}
	definition := &v2.FieldDefinition{
		Contract: v2.Contract, Identity: identity,
		DisplayName: normalizedDraft.DisplayName, Help: normalizedDraft.Help,
		LogicalType: normalizedDraft.LogicalType, Lifecycle: lifecycle,
		Value: normalizedDraft.Value, Constraints: normalizedDraft.Constraints,
		Storage: normalizedDraft.Storage, Display: normalizedDraft.Display,
		Select: normalizedDraft.Select, Relation: normalizedDraft.Relation,
		File: normalizedDraft.File, JSON: normalizedDraft.JSON,
		AutoDate: normalizedDraft.AutoDate, Formula: normalizedDraft.Formula,
		Lookup: normalizedDraft.Lookup,
	}
	if err := v2.Validate(*definition); err != nil {
		var contractErr *v2.ProductError
		if errors.As(err, &contractErr) &&
			strings.HasPrefix(contractErr.Path, "value.default") {
			return nil, productError(
				"field.default.invalid",
				"draft."+contractErr.Path,
				contractErr.Message,
				contractErr.Details,
			)
		}
		return nil, err
	}
	if definition.Value.Default.Enabled {
		if definition.Value.Default.Value == nil {
			if definition.LogicalType != v2.LogicalJSON ||
				(definition.JSON.RootType != "any" &&
					definition.JSON.RootType != "null") ||
				definition.Value.Required {
				return nil, productError(
					"field.default.invalid", "draft.value.default.value",
					"null default is not valid for this field", nil,
				)
			}
			return definition, nil
		}
		if _, err := fieldvalue.New().NormalizeWrite(
			ctx, *definition, fieldvalue.Insert,
			fieldvalue.Input{Supplied: false},
		); err != nil {
			return nil, productError(
				"field.default.invalid", "draft.value.default.value",
				"default does not satisfy the field type and constraints",
				map[string]any{"cause": err.Error()},
			)
		}
	}
	return definition, nil
}

func dataSensitivePlan(action v2.ChangeAction, classes []v2.ChangeClass) bool {
	if action == v2.ActionBackfill || action == v2.ActionPurge {
		return true
	}
	return containsClass(classes, v2.ClassConstraint) ||
		containsClass(classes, v2.ClassMigration) ||
		containsClass(classes, v2.ClassDanger)
}

func conversionRuleRequired(source v2.LogicalType, target v2.LogicalType) bool {
	if source == v2.LogicalMultiSelect && target == v2.LogicalSelect {
		return true
	}
	if source == v2.LogicalText && target == v2.LogicalNumber {
		return true
	}
	return (source == v2.LogicalDate || source == v2.LogicalDateTime ||
		source == v2.LogicalTime) && source != target
}

func validateIntentShape(intent v2.FieldChangeIntent) error {
	if intent.TableID == "" {
		return productError("field.contract.invalid", "tableId", "tableId is required", nil)
	}
	if intent.ExpectedSchemaRev == "" {
		return productError(
			"field.contract.invalid", "expectedSchemaRevision",
			"expectedSchemaRevision is required", nil,
		)
	}
	if intent.Actor.ID == "" || intent.Actor.Kind == "" {
		return productError("field.contract.invalid", "actor", "actor is required", nil)
	}
	if intent.Action == v2.ActionCreate {
		if intent.FieldID != "" || intent.Draft == nil {
			return productError(
				"field.contract.invalid", "fieldId",
				"create requires an empty fieldId and a draft", nil,
			)
		}
		return nil
	}
	if intent.FieldID == "" {
		return productError("field.contract.invalid", "fieldId", "fieldId is required", nil)
	}
	if (intent.Action == v2.ActionUpdate || intent.Action == v2.ActionConvert) &&
		intent.Draft == nil {
		return productError("field.contract.invalid", "draft", "draft is required", nil)
	}
	return nil
}

func classify(
	action v2.ChangeAction,
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
	conversionRule string,
) []v2.ChangeClass {
	switch action {
	case v2.ActionCreate:
		return []v2.ChangeClass{v2.ClassSchema}
	case v2.ActionRetire, v2.ActionRestore:
		return []v2.ChangeClass{v2.ClassSchema}
	case v2.ActionPurge:
		return []v2.ChangeClass{v2.ClassDanger}
	case v2.ActionConvert, v2.ActionBackfill:
		return []v2.ChangeClass{v2.ClassMigration}
	}
	classes := map[v2.ChangeClass]struct{}{}
	if before.DisplayName != after.DisplayName ||
		before.Help != after.Help ||
		!reflect.DeepEqual(before.Display, after.Display) {
		classes[v2.ClassDisplay] = struct{}{}
	}
	if before.Storage.Options.Presentable != after.Storage.Options.Presentable {
		classes[v2.ClassMetadata] = struct{}{}
	}
	if !reflect.DeepEqual(before.Value, after.Value) ||
		!reflect.DeepEqual(before.Constraints, after.Constraints) {
		classes[v2.ClassConstraint] = struct{}{}
	}
	beforeStorage := before.Storage
	afterStorage := after.Storage
	beforeStorage.Options.Presentable = false
	afterStorage.Options.Presentable = false
	if !reflect.DeepEqual(beforeStorage, afterStorage) ||
		!reflect.DeepEqual(before.Select, after.Select) ||
		!reflect.DeepEqual(before.Relation, after.Relation) ||
		!reflect.DeepEqual(before.File, after.File) ||
		!reflect.DeepEqual(before.JSON, after.JSON) {
		classes[v2.ClassSchema] = struct{}{}
	}
	if relationCascadeIntroduced(before, after) {
		classes[v2.ClassDanger] = struct{}{}
	}
	if before.Relation != nil && after.Relation != nil &&
		before.Relation.Cardinality != after.Relation.Cardinality {
		delete(classes, v2.ClassSchema)
		classes[v2.ClassMigration] = struct{}{}
	}
	if _, ok := parseSelectOptionRule(conversionRule); ok {
		delete(classes, v2.ClassSchema)
		classes[v2.ClassMigration] = struct{}{}
	}
	result := make([]v2.ChangeClass, 0, len(classes))
	for class := range classes {
		result = append(result, class)
	}
	sort.Slice(result, func(left, right int) bool {
		return classRank(result[left]) < classRank(result[right])
	})
	return result
}

func applySelectOptionDeletion(
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
	draft *v2.FieldDraft,
	rawRule string,
) error {
	rule, ok := parseSelectOptionRule(rawRule)
	if !ok {
		return productError(
			"field.conversion.rule_invalid", "conversionRule",
			"update conversion rule must describe a select option deletion", nil,
		)
	}
	if before.Select == nil || after.Select == nil || draft.Select == nil ||
		(before.LogicalType != v2.LogicalSelect &&
			before.LogicalType != v2.LogicalMultiSelect) {
		return productError(
			"field.conversion.rule_invalid", "conversionRule",
			"select option deletion rule requires a select field", nil,
		)
	}
	sourceExists := false
	for _, option := range before.Select.Options {
		if option.OptionID == rule.SourceOptionID {
			sourceExists = true
			break
		}
	}
	if !sourceExists {
		return productError(
			"field.conversion.rule_invalid", "conversionRule",
			"source option does not exist", nil,
		)
	}
	for _, option := range draft.Select.Options {
		if option.OptionID == rule.SourceOptionID {
			return productError(
				"field.conversion.rule_invalid", "conversionRule",
				"deleted option must be omitted from the draft", nil,
			)
		}
	}
	if rule.Action == "replace" {
		replacementActive := false
		for _, option := range draft.Select.Options {
			if option.OptionID == rule.ReplacementOptionID &&
				option.State == v2.OptionActive {
				replacementActive = true
				break
			}
		}
		if !replacementActive ||
			rule.ReplacementOptionID == rule.SourceOptionID {
			return productError(
				"field.conversion.rule_invalid", "conversionRule",
				"replacement option must be a different active option", nil,
			)
		}
	}
	options := after.Select.Options[:0]
	for _, option := range after.Select.Options {
		if option.OptionID != rule.SourceOptionID {
			options = append(options, option)
		}
	}
	after.Select.Options = options
	return v2.Validate(*after)
}

func classRank(class v2.ChangeClass) int {
	switch class {
	case v2.ClassDisplay:
		return 0
	case v2.ClassMetadata:
		return 1
	case v2.ClassConstraint:
		return 2
	case v2.ClassSchema:
		return 3
	case v2.ClassMigration:
		return 4
	default:
		return 5
	}
}

func requiredConfirmations(
	action v2.ChangeAction,
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
) []string {
	result := []string{}
	if action == v2.ActionPurge {
		result = append(result, "backupReceipt", "fieldName")
	}
	if after != nil && after.Relation != nil && after.Relation.DeletePolicy == "cascade" &&
		(before == nil || before.Relation == nil || before.Relation.DeletePolicy != "cascade") {
		result = append(result, "cascade")
	}
	return result
}

func planSteps(
	action v2.ChangeAction,
	classes []v2.ChangeClass,
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
	tableID string,
) []v2.PlanStep {
	result := []v2.PlanStep{{Kind: "validate", Details: map[string]any{}}}
	if containsClass(classes, v2.ClassMigration) {
		result = append(result,
			v2.PlanStep{Kind: "copy", Details: map[string]any{}},
			v2.PlanStep{Kind: "verify", Details: map[string]any{}},
			v2.PlanStep{Kind: "switch", Details: map[string]any{}},
			v2.PlanStep{Kind: "cleanup", Details: map[string]any{}},
		)
	} else {
		details := map[string]any{"action": action}
		if relationCascadeIntroduced(before, after) {
			details["danger"] = "cascade"
			details["direction"] = "targetToSource"
			details["sourceTableId"] = tableID
			details["targetTableId"] = after.Relation.TargetTableID
		}
		if action == v2.ActionPurge && before != nil {
			details["valuePhysicalName"] = before.Identity.PhysicalName
			details["valueProviderFieldId"] = before.Identity.ProviderFieldID
			details["presencePhysicalName"] =
				before.Value.Presence.PhysicalName
			details["presenceProviderFieldId"] =
				before.Value.Presence.ProviderFieldID
			details["removeIndexes"] = true
			details["removeAttachmentBlobs"] =
				before.LogicalType == v2.LogicalFile
			details["removeRelationMetadata"] =
				before.LogicalType == v2.LogicalRelation
			details["writeAuditSummary"] = true
		}
		result = append(result, v2.PlanStep{
			Kind: "apply", Details: details,
		})
	}
	return result
}

func containsClass(classes []v2.ChangeClass, wanted v2.ChangeClass) bool {
	for _, class := range classes {
		if class == wanted {
			return true
		}
	}
	return false
}

func canonicalHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func hashPlan(plan v2.FieldChangePlan) (string, error) {
	plan.PlanHash = ""
	return canonicalHash(plan)
}

func VerifyPlanHash(plan v2.FieldChangePlan) bool {
	actual, err := hashPlan(plan)
	return err == nil && actual == plan.PlanHash
}

func randomID(reader io.Reader, prefix string, length int) (string, error) {
	raw := make([]byte, length)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", err
	}
	const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"
	result := make([]byte, len(prefix)+length)
	copy(result, prefix)
	for index, value := range raw {
		result[len(prefix)+index] = alphabet[int(value)%len(alphabet)]
	}
	return string(result), nil
}

func cloneDefinition(value *v2.FieldDefinition) *v2.FieldDefinition {
	if value == nil {
		return nil
	}
	raw, _ := json.Marshal(value)
	var result v2.FieldDefinition
	_ = json.Unmarshal(raw, &result)
	return &result
}

func cloneDraft(value *v2.FieldDraft) v2.FieldDraft {
	raw, _ := json.Marshal(value)
	var result v2.FieldDraft
	_ = json.Unmarshal(raw, &result)
	return result
}

func relationCardinalityChanges(
	before *v2.FieldDefinition,
	draft *v2.FieldDraft,
) bool {
	return before != nil && draft != nil &&
		before.LogicalType == v2.LogicalRelation &&
		draft.LogicalType == v2.LogicalRelation &&
		before.Relation != nil && draft.Relation != nil &&
		before.Relation.Cardinality != draft.Relation.Cardinality
}

func relationCascadeIntroduced(
	before *v2.FieldDefinition,
	after *v2.FieldDefinition,
) bool {
	return after != nil && after.Relation != nil &&
		after.Relation.DeletePolicy == "cascade" &&
		(before == nil || before.Relation == nil ||
			before.Relation.DeletePolicy != "cascade")
}

func containsLogicalType(values []v2.LogicalType, wanted v2.LogicalType) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type NoopPreflight struct{}

func (NoopPreflight) Check(
	context.Context,
	v2.FieldChangeIntent,
	*v2.FieldDefinition,
	*v2.FieldDefinition,
	[]v2.ChangeClass,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	return v2.Impact{
		Failures: []v2.FailureSample{}, Dependencies: []v2.DependencyRef{},
	}, []v2.Diagnostic{}, []v2.Diagnostic{}, nil
}

type ProductError struct {
	Code    string
	Path    string
	Message string
	Details map[string]any
}

func (err *ProductError) Error() string {
	return err.Code + " at " + err.Path + ": " + err.Message
}

func productError(code, path, message string, details map[string]any) *ProductError {
	return &ProductError{Code: code, Path: path, Message: message, Details: details}
}
