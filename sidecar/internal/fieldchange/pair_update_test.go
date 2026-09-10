package fieldchange_test

import (
	"context"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/fieldchange"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func pairUpdateFixture() (sourceStub, v2.FieldChangeIntent) {
	forward := definitionFor(v2.LogicalRelation)
	forward.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_customers", Cardinality: "many", DeletePolicy: "setNull",
		DisplayField: "fld_customer_name", PairID: "relp_orders_customers",
		ReciprocalFieldID: "fld_customer_orders",
	}
	reverse := definitionFor(v2.LogicalRelation)
	reverse.Identity = v2.FieldIdentity{
		FieldID: "fld_customer_orders", PhysicalName: "f_customer_orders",
		ProviderFieldID: "pb_customer_orders",
	}
	reverse.Value.Presence.PhysicalName = "__vt_has_f_customer_orders"
	reverse.Value.Presence.ProviderFieldID = "pb_customer_orders_presence"
	reverse.Relation = &v2.RelationSpec{
		TargetTableID: "tbl_orders", Cardinality: "many", DeletePolicy: "setNull",
		DisplayField: "fld_order_number", PairID: "relp_orders_customers",
		ReciprocalFieldID: forward.Identity.FieldID,
	}
	return sourceStub{
		revisionsByTable: map[string]fieldchange.Revisions{
			"tbl_orders":    {Schema: "schema_3", Data: 11},
			"tbl_customers": {Schema: "schema_8", Data: 29},
		},
		fields: map[string]v2.FieldDefinition{
			forward.Identity.FieldID: forward, reverse.Identity.FieldID: reverse,
		},
	}, v2.FieldChangeIntent{
		Action: v2.ActionUpdate, TableID: "tbl_orders", FieldID: forward.Identity.FieldID,
		ExpectedSchemaRev: "schema_3", Actor: v2.Actor{ID: "user_local", Kind: "user"},
	}
}

func TestPairPatchRejectsMixedOrInvalidSettings(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"draft", "createDraft", "conversionRule", "action", "cardinality", "name", "displayField", "cascade", "empty"} {
		t.Run(kind, func(t *testing.T) {
			source, intent := pairUpdateFixture()
			name, invalid := "Updated", ""
			intent.RelationPairPatch = &v2.RelationPairPatch{SourceDisplayName: &name}
			switch kind {
			case "draft":
				draft := draftFrom(source.fields[intent.FieldID])
				intent.Draft = &draft
			case "createDraft":
				intent.RelationPair = &v2.RelationPairDraft{}
			case "conversionRule":
				intent.ConversionRule = "first"
			case "action":
				intent.Action = v2.ActionRetire
			case "cardinality":
				intent.RelationPairPatch.ReciprocalCardinality = &invalid
			case "name":
				intent.RelationPairPatch.ReciprocalDisplayName = &invalid
			case "displayField":
				intent.RelationPairPatch.ReciprocalDisplayFieldID = &invalid
			case "cascade":
				invalid = "cascade"
				intent.RelationPairPatch.DeletePolicy = &invalid
			case "empty":
				intent.RelationPairPatch = &v2.RelationPairPatch{}
			}
			if _, err := fieldchange.NewPlanner(source, nil, nil, nil).Plan(context.Background(), intent); err == nil {
				t.Fatal("invalid pair patch accepted")
			}
		})
	}
}

type endpointPreflight struct {
	t    *testing.T
	seen int
}

func (check *endpointPreflight) Check(
	_ context.Context, intent v2.FieldChangeIntent, before, after *v2.FieldDefinition,
	classes []v2.ChangeClass,
) (v2.Impact, []v2.Diagnostic, []v2.Diagnostic, error) {
	check.seen++
	if intent.TableID == "tbl_orders" && len(classes) != 0 {
		check.t.Fatalf("unchanged source was classified as changed: %#v", classes)
	}
	if intent.TableID == "tbl_customers" &&
		(len(classes) != 1 || classes[0] != v2.ClassSchema || before.Relation.Cardinality != "many" || after.Relation.Cardinality != "one") {
		check.t.Fatalf("reciprocal was not independently classified: %#v", classes)
	}
	return v2.Impact{}, nil, nil, nil
}

func TestPairPatchPreflightsBothEndpointDefinitionsIndependently(t *testing.T) {
	t.Parallel()
	source, intent := pairUpdateFixture()
	one := "one"
	intent.RelationPairPatch = &v2.RelationPairPatch{ReciprocalCardinality: &one}
	check := &endpointPreflight{t: t}
	plan, err := fieldchange.NewPlanner(source, check, nil, nil).Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if check.seen != 2 || len(plan.Classes) != 1 || plan.Classes[0] != v2.ClassSchema {
		t.Fatalf("missing endpoint preflight or aggregate class: %d / %#v", check.seen, plan.Classes)
	}
}

func TestLegacyRelationUpdateCannotReplaceTargetOrAttachPairIdentity(t *testing.T) {
	t.Parallel()
	for _, attribute := range []string{"target", "pair", "reciprocal"} {
		t.Run(attribute, func(t *testing.T) {
			source, intent := pairUpdateFixture()
			before := source.fields[intent.FieldID]
			before.Relation.PairID = ""
			before.Relation.ReciprocalFieldID = ""
			source.fields[intent.FieldID] = before
			draft := draftFrom(before)
			relation := *before.Relation
			draft.Relation = &relation
			switch attribute {
			case "target":
				draft.Relation.TargetTableID = "tbl_replacement"
			case "pair":
				draft.Relation.PairID = "relp_forged"
				draft.Relation.ReciprocalFieldID = "fld_forged"
			case "reciprocal":
				draft.Relation.ReciprocalFieldID = "fld_forged"
			}
			intent.Draft = &draft
			if _, err := fieldchange.NewPlanner(source, nil, nil, nil).Plan(context.Background(), intent); err == nil {
				t.Fatalf("legacy relation allowed immutable %s change", attribute)
			}
		})
	}
}

func TestPairPatchCanChangeOnlyReciprocalCardinalityWithoutMigration(t *testing.T) {
	t.Parallel()
	source, intent := pairUpdateFixture()
	one := "one"
	intent.RelationPairPatch = &v2.RelationPairPatch{ReciprocalCardinality: &one}
	planner := fieldchange.NewPlanner(source, impactPreflightStub{records: 3}, nil, nil)
	plan, err := planner.Plan(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply || plan.CreatesMigration || len(plan.RelatedChanges) != 1 {
		t.Fatalf("pair patch must be one synchronous plan: %#v", plan)
	}
	related := plan.RelatedChanges[0]
	if plan.After.Identity != plan.Before.Identity ||
		related.After.Identity != related.Before.Identity ||
		plan.After.Relation.Cardinality != "many" || related.After.Relation.Cardinality != "one" ||
		plan.After.Relation.PairID != "relp_orders_customers" ||
		related.After.Relation.PairID != "relp_orders_customers" {
		t.Fatalf("pair identity or endpoint settings changed incorrectly: %#v", plan)
	}
	if plan.ExpectedDataRevision == nil || *plan.ExpectedDataRevision != 11 ||
		related.ExpectedDataRevision == nil || *related.ExpectedDataRevision != 29 ||
		related.ExpectedSchemaRevision != "schema_8" {
		t.Fatalf("both table revisions must be frozen: %#v", plan)
	}
}
