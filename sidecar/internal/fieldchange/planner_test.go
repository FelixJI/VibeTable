package fieldchange_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestConcurrentSameIntentReturnsOneFrozenPlan(t *testing.T) {
	t.Parallel()
	planner := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_1"}},
		nil, fieldchange.NewMemoryPlanStore(), nil,
	)
	draft := draftFor(v2.LogicalNumber)
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders",
		ExpectedSchemaRev: "schema_1", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	}
	const workers = 64
	results := make(chan v2.FieldChangePlan, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			plan, err := planner.Plan(context.Background(), intent)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- plan
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	var frozen *v2.FieldChangePlan
	for plan := range results {
		if frozen == nil {
			copy := plan
			frozen = &copy
			continue
		}
		if plan.PlanID != frozen.PlanID ||
			plan.PlanHash != frozen.PlanHash ||
			plan.After.Identity != frozen.After.Identity {
			t.Fatalf("concurrent intent produced divergent plans: %#v / %#v", frozen, plan)
		}
	}
}

type sourceStub struct {
	revisions        fieldchange.Revisions
	revisionsByTable map[string]fieldchange.Revisions
	fields           map[string]v2.FieldDefinition
}

type impactPreflightStub struct {
	records int64
}

func (stub impactPreflightStub) Check(
	context.Context,
	v2.FieldChangeIntent,
	*v2.FieldDefinition,
	*v2.FieldDefinition,
	[]v2.ChangeClass,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	return v2.Impact{Records: stub.records}, nil, nil, nil
}

func (source sourceStub) Revisions(
	_ context.Context,
	tableID string,
) (fieldchange.Revisions, error) {
	if revision, ok := source.revisionsByTable[tableID]; ok {
		return revision, nil
	}
	return source.revisions, nil
}

func (source sourceStub) Field(
	_ context.Context,
	_ string,
	fieldID string,
) (*v2.FieldDefinition, error) {
	field, exists := source.fields[fieldID]
	if !exists {
		return nil, fieldchange.ErrFieldNotFound
	}
	result := field
	return &result, nil
}

func TestCreatePlanAllocatesAndFreezesEveryIdentityAcrossRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	store := fieldchange.NewMemoryPlanStore()
	planner := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_7", Data: 3}},
		nil,
		store,
		nil,
		fieldchange.WithClock(func() time.Time { return now }),
	)
	draft := draftFor(v2.LogicalSelect)
	draft.Select.Options[0].OptionID = ""
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders", ExpectedSchemaRev: "schema_7",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	}
	first, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID != second.PlanID || first.PlanHash != second.PlanHash ||
		first.After.Identity != second.After.Identity ||
		first.After.Select.Options[0].OptionID != second.After.Select.Options[0].OptionID {
		t.Fatalf("retry generated new identities:\n%#v\n%#v", first, second)
	}
	if first.After.Identity.FieldID == "" ||
		first.After.Identity.PhysicalName == "" ||
		first.After.Value.Presence.ProviderFieldID == "" ||
		first.After.Select.Options[0].OptionID == "" {
		t.Fatalf("plan did not allocate complete identities: %#v", first.After)
	}
}

func TestDisplayRenameKeepsIdentityAndIsClassifiedWithoutMigration(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalNumber)
	draft := draftFrom(existing)
	draft.DisplayName = "新显示名"
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_2"},
			fields:    map[string]v2.FieldDefinition{existing.Identity.FieldID: existing},
		},
		nil, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders", FieldID: existing.Identity.FieldID,
		ExpectedSchemaRev: "schema_2", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.After.Identity != existing.Identity ||
		len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassDisplay ||
		plan.CreatesMigration {
		t.Fatalf("rename plan = %#v", plan)
	}
}

func TestOmittedSelectOptionIsRetiredInsteadOfPhysicallyDeleted(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalSelect)
	existing.Select.Options = append(existing.Select.Options, v2.SelectOption{
		OptionID: "opt_01JLEGACY1",
		Label:    "Legacy",
		State:    v2.OptionActive,
	})
	draft := draftFrom(existing)
	draft.Select = &v2.SelectSpec{
		Options: append([]v2.SelectOption(nil), existing.Select.Options[:1]...),
	}
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_2"},
			fields: map[string]v2.FieldDefinition{
				existing.Identity.FieldID: existing,
			},
		},
		nil, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders",
		FieldID: existing.Identity.FieldID, ExpectedSchemaRev: "schema_2",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.After.Select.Options) != 2 ||
		plan.After.Select.Options[1].OptionID != "opt_01JLEGACY1" ||
		plan.After.Select.Options[1].State != v2.OptionRetired {
		t.Fatalf("omitted option was not retained as retired: %#v", plan.After.Select)
	}
}

func TestUsedSelectOptionDeletionRequiresResolutionAndCreatesMigration(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalSelect)
	existing.Select.Options = append(existing.Select.Options, v2.SelectOption{
		OptionID: "opt_01JLEGACY1",
		Label:    "Legacy",
		State:    v2.OptionActive,
	})
	draft := draftFrom(existing)
	draft.Select = &v2.SelectSpec{
		Options: append([]v2.SelectOption(nil), existing.Select.Options[:1]...),
	}
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_2", Data: 7},
			fields: map[string]v2.FieldDefinition{
				existing.Identity.FieldID: existing,
			},
		},
		impactPreflightStub{records: 3}, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders",
		FieldID: existing.Identity.FieldID, ExpectedSchemaRev: "schema_2",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
		ConversionRule: "selectOption:opt_01JLEGACY1:replace:" +
			existing.Select.Options[0].OptionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.After.Select.Options) != 1 ||
		plan.After.Select.Options[0].OptionID != existing.Select.Options[0].OptionID ||
		!plan.CreatesMigration ||
		len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassMigration ||
		plan.ExpectedDataRevision == nil || *plan.ExpectedDataRevision != 7 ||
		plan.After.Identity.ProviderFieldID == existing.Identity.ProviderFieldID ||
		plan.After.Value.Presence.ProviderFieldID ==
			existing.Value.Presence.ProviderFieldID {
		t.Fatalf("select option deletion plan = %#v", plan)
	}
}

func TestCascadeUpdateIsDangerousAndDescribesDeleteDirection(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalRelation)
	existing.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_targets", Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: "fld_01JTARGET1",
	}
	draft := draftFor(v2.LogicalRelation)
	draft.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_targets", Cardinality: "one",
		DeletePolicy: "cascade", DisplayField: "fld_01JTARGET1",
	}
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_2"},
			fields:    map[string]v2.FieldDefinition{existing.Identity.FieldID: existing},
		},
		nil, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders",
		FieldID: existing.Identity.FieldID, ExpectedSchemaRev: "schema_2",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsClassForTest(plan.Classes, v2.ClassDanger) ||
		len(plan.Confirmations) != 1 || plan.Confirmations[0] != "cascade" {
		t.Fatalf("cascade danger plan = %#v", plan)
	}
	foundDirection := false
	for _, step := range plan.Steps {
		if step.Details["direction"] == "targetToSource" {
			foundDirection = true
		}
	}
	if !foundDirection {
		t.Fatalf("cascade steps omit direction: %#v", plan.Steps)
	}
}

func TestCreateRelationFreezesReciprocalFieldAndBothTableRevisions(t *testing.T) {
	t.Parallel()
	draft := draftFor(v2.LogicalRelation)
	draft.DisplayName = "客户"
	draft.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_customers", Cardinality: "many",
		DeletePolicy: "setNull", DisplayField: "fld_customer_name",
	}
	planner := fieldchange.NewPlanner(sourceStub{
		revisionsByTable: map[string]fieldchange.Revisions{
			"tbl_orders":    {Schema: "schema_2"},
			"tbl_customers": {Schema: "schema_5"},
		},
	}, nil, nil, nil)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders",
		ExpectedSchemaRev: "schema_2", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
		RelationPair: &v2.RelationPairDraft{
			ReciprocalDisplayName: "订单",
			ReciprocalCardinality: "many",
			SourceDisplayFieldID:  "fld_order_number",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.After == nil || plan.After.Relation == nil ||
		plan.After.Relation.PairID == "" ||
		len(plan.RelatedChanges) != 1 {
		t.Fatalf("paired relation plan is incomplete: %#v", plan)
	}
	related := plan.RelatedChanges[0]
	if related.TableID != "tbl_customers" ||
		related.ExpectedSchemaRevision != "schema_5" || related.After == nil ||
		related.After.DisplayName != "订单" ||
		related.After.Relation.TargetTableID != "tbl_orders" ||
		related.After.Relation.DisplayField != "fld_order_number" ||
		related.After.Relation.PairID != plan.After.Relation.PairID ||
		related.After.Relation.ReciprocalFieldID != plan.After.Identity.FieldID ||
		plan.After.Relation.ReciprocalFieldID != related.After.Identity.FieldID {
		t.Fatalf("reciprocal relation was not frozen symmetrically: %#v", plan)
	}
}

func TestRetireRelationRequiresAtomicReciprocalConfirmation(t *testing.T) {
	t.Parallel()
	forward := definitionFor(v2.LogicalRelation)
	forward.Identity.FieldID = "fld_orders_customer"
	forward.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_customers", Cardinality: "one",
		DeletePolicy: "setNull", DisplayField: "fld_customer_name",
		PairID: "relp_orders_customers", ReciprocalFieldID: "fld_customer_orders",
	}
	reverse := definitionFor(v2.LogicalRelation)
	reverse.Identity.FieldID = "fld_customer_orders"
	reverse.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_orders", Cardinality: "many",
		DeletePolicy: "setNull", DisplayField: "fld_order_number",
		PairID: "relp_orders_customers", ReciprocalFieldID: "fld_orders_customer",
	}
	planner := fieldchange.NewPlanner(sourceStub{
		revisionsByTable: map[string]fieldchange.Revisions{
			"tbl_orders": {Schema: "schema_3"}, "tbl_customers": {Schema: "schema_8"},
		},
		fields: map[string]v2.FieldDefinition{
			forward.Identity.FieldID: forward, reverse.Identity.FieldID: reverse,
		},
	}, nil, nil, nil)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionRetire, TableID: "tbl_orders",
		FieldID: forward.Identity.FieldID, ExpectedSchemaRev: "schema_3",
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.RelatedChanges) != 1 || plan.RelatedChanges[0].After == nil ||
		plan.RelatedChanges[0].After.Lifecycle.State != v2.LifecycleRetired ||
		!containsStringForTest(plan.Confirmations, "relationPair") {
		t.Fatalf("retire did not protect reciprocal relation: %#v", plan)
	}
}

func TestConstraintPlanFreezesCurrentDataRevision(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalNumber)
	draft := draftFrom(existing)
	minimum := float64(1)
	draft.Constraints.Range.Min = &minimum
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_2", Data: 41},
			fields: map[string]v2.FieldDefinition{
				existing.Identity.FieldID: existing,
			},
		},
		impactPreflight{}, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders",
		FieldID: existing.Identity.FieldID, ExpectedSchemaRev: "schema_2",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ExpectedDataRevision == nil || *plan.ExpectedDataRevision != 41 ||
		plan.Intent.ExpectedDataRevision == nil ||
		*plan.Intent.ExpectedDataRevision != 41 {
		t.Fatalf("data revision was not frozen: %#v", plan)
	}
}

func TestNoopUpdateAndRestoreAreRejected(t *testing.T) {
	t.Parallel()
	for _, action := range []v2.ChangeAction{
		v2.ActionUpdate,
		v2.ActionRestore,
	} {
		action := action
		t.Run(string(action), func(t *testing.T) {
			field := definitionFor(v2.LogicalNumber)
			var draft *v2.FieldDraft
			if action == v2.ActionUpdate {
				value := draftFrom(field)
				draft = &value
			}
			planner := fieldchange.NewPlanner(
				sourceStub{
					revisions: fieldchange.Revisions{Schema: "schema_2"},
					fields: map[string]v2.FieldDefinition{
						field.Identity.FieldID: field,
					},
				}, nil, nil, nil,
			)
			_, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
				Action: action, TableID: "tbl_orders",
				FieldID:           field.Identity.FieldID,
				ExpectedSchemaRev: "schema_2", Draft: draft,
				Actor: v2.Actor{ID: "user_local", Kind: "user"},
			})
			var productErr *fieldchange.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != "field.change.noop" {
				t.Fatalf("noop error = %#v", err)
			}
		})
	}
}

func TestEnabledDefaultsAreValidatedAgainstTheFieldKernel(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		logicalType v2.LogicalType
		value       any
		wantCode    string
	}{
		{
			name:        "invalid number",
			logicalType: v2.LogicalNumber,
			value:       "not-a-number",
			wantCode:    "field.default.invalid",
		},
		{
			name:        "json null",
			logicalType: v2.LogicalJSON,
			value:       nil,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			draft := draftFor(test.logicalType)
			draft.Value.Default.Enabled = true
			draft.Value.Default.Value = test.value
			planner := fieldchange.NewPlanner(
				sourceStub{
					revisions: fieldchange.Revisions{Schema: "schema_1"},
				}, nil, nil, nil,
			)
			plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
				Action: v2.ActionCreate, TableID: "tbl_orders",
				ExpectedSchemaRev: "schema_1", Draft: &draft,
				Actor: v2.Actor{ID: "user_local", Kind: "user"},
			})
			if test.wantCode == "" {
				if err != nil || !plan.After.Value.Default.Enabled ||
					plan.After.Value.Default.Value != nil {
					t.Fatalf("JSON null default = %#v, err=%v", plan, err)
				}
				return
			}
			var productErr *fieldchange.ProductError
			if !errors.As(err, &productErr) ||
				productErr.Code != test.wantCode {
				t.Fatalf("default validation error = %#v", err)
			}
		})
	}
}

func TestNonEmptyConvertIsMigrationAndStaleRevisionIsRejected(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalText)
	draft := draftFor(v2.LogicalEmail)
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_9", Data: 4},
			fields:    map[string]v2.FieldDefinition{existing.Identity.FieldID: existing},
		},
		impactPreflight{records: 4}, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionConvert, TableID: "tbl_orders", FieldID: existing.Identity.FieldID,
		ExpectedSchemaRev: "schema_9", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CreatesMigration ||
		len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassMigration {
		t.Fatalf("convert plan = %#v", plan)
	}
	if plan.After.Identity.FieldID != existing.Identity.FieldID ||
		plan.After.Identity.PhysicalName != existing.Identity.PhysicalName ||
		plan.After.Identity.ProviderFieldID == existing.Identity.ProviderFieldID ||
		plan.After.Value.Presence.ProviderFieldID ==
			existing.Value.Presence.ProviderFieldID {
		t.Fatalf("conversion identities were not frozen correctly: %#v", plan.After)
	}
	_, err = planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionConvert, TableID: "tbl_orders", FieldID: existing.Identity.FieldID,
		ExpectedSchemaRev: "schema_8", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	var productErr *fieldchange.ProductError
	if !errors.As(err, &productErr) || productErr.Code != "field.change.schema_conflict" {
		t.Fatalf("stale plan error = %#v", err)
	}
}

func TestEmptyConvertIsDirectSchemaChange(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalText)
	draft := draftFor(v2.LogicalEmail)
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_9"},
			fields:    map[string]v2.FieldDefinition{existing.Identity.FieldID: existing},
		},
		impactPreflight{}, nil, nil,
	)
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionConvert, TableID: "tbl_orders", FieldID: existing.Identity.FieldID,
		ExpectedSchemaRev: "schema_9", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CreatesMigration ||
		len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassSchema {
		t.Fatalf("empty convert plan = %#v", plan)
	}
}

func TestConvertRejectsUnsupportedTargetAndRuleBeforeAllocatingAPlan(t *testing.T) {
	t.Parallel()
	existing := definitionFor(v2.LogicalNumber)
	planner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_1"},
			fields: map[string]v2.FieldDefinition{
				existing.Identity.FieldID: existing,
			},
		},
		nil, nil, nil,
	)
	dateDraft := draftFor(v2.LogicalDate)
	_, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionConvert, TableID: "tbl_orders",
		FieldID: existing.Identity.FieldID, ExpectedSchemaRev: "schema_1",
		Draft: &dateDraft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	var productErr *fieldchange.ProductError
	if !errors.As(err, &productErr) ||
		productErr.Code != "field.capability.unsupported" ||
		productErr.Path != "draft.logicalType" {
		t.Fatalf("unsupported target error = %#v", err)
	}

	text := definitionFor(v2.LogicalText)
	numberDraft := draftFor(v2.LogicalNumber)
	rulePlanner := fieldchange.NewPlanner(
		sourceStub{
			revisions: fieldchange.Revisions{Schema: "schema_1"},
			fields: map[string]v2.FieldDefinition{
				text.Identity.FieldID: text,
			},
		},
		nil, nil, nil,
	)
	_, err = rulePlanner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionConvert, TableID: "tbl_orders",
		FieldID: text.Identity.FieldID, ExpectedSchemaRev: "schema_1",
		Draft: &numberDraft, ConversionRule: "guess",
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if !errors.As(err, &productErr) ||
		productErr.Code != "field.capability.unsupported" ||
		productErr.Path != "conversionRule" {
		t.Fatalf("unsupported rule error = %#v", err)
	}
}

func TestCreateSelectAllocatesEveryOmittedOptionIdentity(t *testing.T) {
	t.Parallel()
	planner := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_1"}},
		nil, nil, nil,
	)
	draft := draftFor(v2.LogicalSelect)
	recommended, err := v2.RecommendedDefaults(v2.LogicalSelect)
	if err != nil {
		t.Fatal(err)
	}
	draft.Constraints = recommended.Constraints
	draft.Select.Options = []v2.SelectOption{
		{Label: "Draft", Color: "#64748b", Order: 10, State: v2.OptionActive},
		{Label: "Active", Color: "#16a34a", Order: 20, State: v2.OptionActive},
	}
	plan, err := planner.Plan(context.Background(), v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders",
		ExpectedSchemaRev: "schema_1", Draft: &draft,
		Actor: v2.Actor{ID: "user_local", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	options := plan.After.Select.Options
	if len(options) != 2 ||
		options[0].OptionID == "" ||
		options[1].OptionID == "" ||
		options[0].OptionID == options[1].OptionID {
		t.Fatalf("allocated options = %#v", options)
	}
}

func TestExpiredPlanAllocatesAFreshFrozenPlan(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	planner := fieldchange.NewPlanner(
		sourceStub{revisions: fieldchange.Revisions{Schema: "schema_1"}},
		nil, nil, nil,
		fieldchange.WithClock(func() time.Time { return now }),
		fieldchange.WithTTL(time.Minute),
	)
	draft := draftFor(v2.LogicalNumber)
	intent := v2.FieldChangeIntent{
		Action: v2.ActionCreate, TableID: "tbl_orders", ExpectedSchemaRev: "schema_1",
		Draft: &draft, Actor: v2.Actor{ID: "user_local", Kind: "user"},
	}
	first, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	second, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID == second.PlanID || first.After.Identity == second.After.Identity {
		t.Fatal("expired plan was incorrectly reused")
	}
}

func definitionFor(logicalType v2.LogicalType) v2.FieldDefinition {
	draft := draftFor(logicalType)
	var formula *v2.FormulaSpec
	if draft.Formula != nil {
		formula = &v2.FormulaSpec{
			Language:   draft.Formula.Language,
			Source:     draft.Formula.Source,
			ResultType: v2.LogicalText,
		}
	}
	draft.Value.Presence = v2.PresenceSpec{
		Mode: v2.PresenceCompanion, ProviderFieldID: "pb_01JPRESEN",
		PhysicalName: "__vt_has_f_01jfieldx",
	}
	if logicalType == v2.LogicalJSON {
		draft.Value.Presence = v2.PresenceSpec{Mode: v2.PresenceNative}
	}
	return v2.FieldDefinition{
		Contract: v2.Contract,
		Identity: v2.FieldIdentity{
			FieldID: "fld_01JFIELDX", PhysicalName: "f_01jfieldx",
			ProviderFieldID: "pb_01JFIELDX",
		},
		DisplayName: draft.DisplayName, Help: draft.Help, LogicalType: draft.LogicalType,
		Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value:     draft.Value, Constraints: draft.Constraints, Storage: draft.Storage,
		Display: draft.Display, Select: draft.Select, Relation: draft.Relation,
		File: draft.File, JSON: draft.JSON, AutoDate: draft.AutoDate,
		Formula: formula, Lookup: draft.Lookup,
	}
}

func draftFor(logicalType v2.LogicalType) v2.FieldDraft {
	recommended, err := v2.RecommendedDefaults(logicalType)
	if err != nil {
		panic(err)
	}
	draft := v2.FieldDraft{
		DisplayName: "字段", LogicalType: logicalType,
		Value: recommended.Value, Constraints: recommended.Constraints,
		Storage: recommended.Storage, Display: recommended.Display,
		File: recommended.File, JSON: recommended.JSON,
	}
	if logicalType == v2.LogicalSelect || logicalType == v2.LogicalMultiSelect {
		draft.Select = &v2.SelectSpec{Options: []v2.SelectOption{{
			OptionID: "opt_01JACTIVE1", Label: "有效", State: v2.OptionActive,
		}}}
		if logicalType == v2.LogicalSelect {
			one := 1
			draft.Constraints.Selection.Max = &one
		}
	}
	return draft
}

type impactPreflight struct {
	records int64
}

func containsClassForTest(classes []v2.ChangeClass, wanted v2.ChangeClass) bool {
	for _, class := range classes {
		if class == wanted {
			return true
		}
	}
	return false
}

func containsStringForTest(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (stub impactPreflight) Check(
	context.Context,
	v2.FieldChangeIntent,
	*v2.FieldDefinition,
	*v2.FieldDefinition,
	[]v2.ChangeClass,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	return v2.Impact{
		Records: stub.records, Failures: []v2.FailureSample{},
		Dependencies: []v2.DependencyRef{},
	}, []v2.Diagnostic{}, []v2.Diagnostic{}, nil
}

func draftFrom(definition v2.FieldDefinition) v2.FieldDraft {
	var formula *v2.FormulaDraftSpec
	if definition.Formula != nil {
		formula = &v2.FormulaDraftSpec{
			Language: definition.Formula.Language,
			Source:   definition.Formula.Source,
		}
	}
	return v2.FieldDraft{
		DisplayName: definition.DisplayName, Help: definition.Help,
		LogicalType: definition.LogicalType, Value: definition.Value,
		Constraints: definition.Constraints, Storage: definition.Storage,
		Display: definition.Display, Select: definition.Select,
		Relation: definition.Relation, File: definition.File, JSON: definition.JSON,
		AutoDate: definition.AutoDate, Formula: formula, Lookup: definition.Lookup,
	}
}
