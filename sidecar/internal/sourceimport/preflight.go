package sourceimport

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	MaxSnapshotBytes = 32 << 20
	MaxTables        = 100
	MaxRecords       = 50000
	MaxFields        = 512
	MaxDiagnostics   = 100
	// PocketBase JSON columns default to 1MiB. The provenance budget keeps
	// headroom for the durable Result envelope around the field summaries.
	MaxProvenanceBytes = 512 << 10
)

// diagnosticCollector keeps every blocking decision even when the visible
// diagnostic list is capped: CanApply and the truncation notice always carry
// the complete blocking facts, never a silently shortened review.
type diagnosticCollector struct {
	plan       *Plan
	total      int
	blocking   int
	truncated  bool
	noticed    bool
	noticeSlot int
}

func (collector *diagnosticCollector) add(
	code, table, field, record, message string, blocking bool,
) {
	collector.total++
	if blocking {
		collector.blocking++
		collector.plan.CanApply = false
	}
	diagnostics := &collector.plan.Diagnostics
	switch {
	case len(*diagnostics) >= MaxDiagnostics:
		collector.truncated = true
	case len(*diagnostics) == MaxDiagnostics-1:
		collector.truncated = true
		if !collector.noticed {
			collector.noticed = true
			collector.noticeSlot = len(*diagnostics)
			*diagnostics = append(*diagnostics, Diagnostic{
				Code:    "source_import.diagnostics_truncated",
				Message: "来源诊断超过展示上限，已截断；阻断性结论已全部计入",
			})
		}
	default:
		*diagnostics = append(
			*diagnostics,
			Diagnostic{code, table, field, record, message, blocking},
		)
	}
}

func (collector *diagnosticCollector) finalize() {
	if !collector.truncated || !collector.noticed {
		return
	}
	kept := collector.noticeSlot
	omitted := collector.total - kept
	if omitted < 0 {
		omitted = 0
	}
	collector.plan.Diagnostics[collector.noticeSlot] = Diagnostic{
		Code: "source_import.diagnostics_truncated",
		Message: fmt.Sprintf(
			"来源诊断共 %d 条，仅保留前 %d 条，另有 %d 条被省略；其中阻断性诊断 %d 条的结论已全部计入，请缩小迁移范围后重新预检",
			collector.total, kept, omitted, collector.blocking,
		),
	}
}

// Preview is entirely read-only. Existing names are read by the authority
// adapter; neither target tables nor rows are created to validate a source.
func Preview(ctx context.Context, source Snapshot, options Options, existingNames []string) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	raw, err := json.Marshal(source)
	if err != nil || len(raw) > MaxSnapshotBytes {
		return Plan{}, &Error{Code: "source_import.source_invalid", Message: "来源数据无法解析或超过本次迁移容量"}
	}
	// Freeze a detached copy, retaining JSON numbers instead of coercing them
	// to float64. Provider buffers cannot mutate a reviewed plan afterwards.
	var frozen Snapshot
	if err := v2.StrictDecode(raw, &frozen); err != nil {
		return Plan{}, err
	}
	source = frozen
	if source.Contract != Contract || !validID(source.Provider) || !validID(source.ContainerID) ||
		strings.TrimSpace(source.DisplayName) == "" || utf8.RuneCountInString(source.DisplayName) > 256 ||
		!validWindow(source.ReadWindow) || len(source.Tables) == 0 || len(source.Tables) > MaxTables {
		return Plan{}, &Error{Code: "source_import.source_invalid", Message: "来源身份、读取窗口或表数量无效"}
	}
	plan := Plan{Contract: Contract, Provider: source.Provider, ContainerID: source.ContainerID,
		DisplayName: source.DisplayName, Version: source.Version, ReadWindow: source.ReadWindow,
		Tables: []TablePlan{}, Attachments: []Attachment{}, Fields: []FieldSummary{},
		Diagnostics: []Diagnostic{}, CanApply: true}
	collector := &diagnosticCollector{plan: &plan}
	add := collector.add
	if source.ReadWindow.Consistency == "window" {
		add("source_import.read_window", "", "", "", "来源按分页读取；本次结果仅绑定实际读取窗口，不保证时点一致快照", false)
	}
	selected := map[string]bool{}
	for _, id := range options.SelectedTableIDs {
		if selected[id] {
			add("source_import.duplicate_selection", id, "", "", "来源表重复选择", true)
		}
		selected[id] = true
	}
	if len(selected) == 0 {
		return Plan{}, &Error{Code: "source_import.selection_empty", Message: "请选择来源表"}
	}
	targetNames := map[string]string{}
	for _, target := range options.TargetNames {
		if _, exists := targetNames[target.TableID]; exists {
			add("source_import.duplicate_target_name", target.TableID, "", "", "目标命名重复", true)
		}
		if !selected[target.TableID] {
			add("source_import.target_name_unknown", target.TableID, "", "", "目标命名引用未选择的来源表", true)
		}
		targetNames[target.TableID] = strings.TrimSpace(target.Name)
	}
	decisions := map[Key]Decision{}
	for _, decision := range options.Decisions {
		key := Key{TableID: decision.TableID, FieldID: decision.FieldID}
		if _, exists := decisions[key]; exists {
			add("source_import.duplicate_decision", decision.TableID, decision.FieldID, "", "字段策略重复", true)
		}
		if !selected[decision.TableID] {
			add("source_import.decision_unknown", decision.TableID, decision.FieldID, "", "字段策略引用未选择的来源表或字段", true)
		}
		decisions[key] = decision
	}
	byTable := map[string]Table{}
	rowIDs := map[Key]bool{}
	total := 0
	// Source order is provider trivia: every validation pass runs over the
	// sorted projection so identical sources in different orders preflight to
	// byte-identical plans and diagnostics.
	sourceTables := append([]Table{}, source.Tables...)
	sort.Slice(sourceTables, func(i, j int) bool { return sourceTables[i].ID < sourceTables[j].ID })
	for _, table := range sourceTables {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		if !validID(table.ID) || len(table.Fields) == 0 || len(table.Fields) > MaxFields {
			add("source_import.table_invalid", table.ID, "", "", "来源表身份或字段数量无效", true)
		}
		if _, exists := byTable[table.ID]; exists {
			add("source_import.duplicate_identity", table.ID, "", "", "来源表 ID 重复", true)
		}
		byTable[table.ID] = table
		if !selected[table.ID] {
			continue
		}
		rows := append([]Record{}, table.Records...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
		total += len(rows)
		for _, row := range rows {
			key := Key{TableID: table.ID, RecordID: row.ID}
			if !validID(row.ID) || rowIDs[key] || row.Values == nil {
				add("source_import.record_identity", table.ID, "", row.ID, "来源记录 ID 缺失、重复或值对象无效", true)
			}
			rowIDs[key] = true
		}
	}
	if total > MaxRecords {
		return Plan{}, &Error{Code: "source_import.capacity", Message: "本次迁移记录数超过容量，请缩小选表范围"}
	}
	names := map[string]bool{}
	for _, name := range existingNames {
		names[strings.TrimSpace(name)] = true
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return Plan{}, err
		}
		table, exists := byTable[id]
		if !exists {
			add("source_import.table_missing", id, "", "", "所选来源表不存在", true)
			continue
		}
		name, explicit := targetNames[id]
		if !explicit {
			name = strings.TrimSpace(table.Name)
		}
		if name == "" || utf8.RuneCountInString(name) > 256 || names[name] {
			add("source_import.target_name", id, "", "", "目标名称为空或重名，请明确重命名后重新预检", true)
		}
		names[name] = true
		tp := TablePlan{SourceID: id, Name: name, Version: table.Version, PrimaryFieldID: table.PrimaryFieldID,
			Fields: []FieldPlan{}, Records: append([]Record{}, table.Records...)}
		sort.Slice(tp.Records, func(i, j int) bool { return tp.Records[i].ID < tp.Records[j].ID })
		seenFields := map[string]bool{}
		orderedFields := append([]Field{}, table.Fields...)
		sort.Slice(orderedFields, func(i, j int) bool { return orderedFields[i].ID < orderedFields[j].ID })
		for _, field := range orderedFields {
			if !validID(field.ID) || seenFields[field.ID] {
				add("source_import.duplicate_identity", id, field.ID, "", "来源字段 ID 缺失或重复", true)
			}
			seenFields[field.ID] = true
			decision, specified := decisions[Key{TableID: id, FieldID: field.ID}]
			if !specified {
				decision = Decision{TableID: id, FieldID: field.ID, Policy: PolicyNative}
			}
			fp, draftErr := planField(field, decision)
			if draftErr != nil {
				add("source_import.field_strategy", id, field.ID, "", draftErr.Error(), true)
				continue
			}
			if fp.Policy == PolicySkip || fp.Policy == PolicySnapshot {
				add("source_import."+fp.Policy, id, field.ID, "", "使用已确认的字段降级策略；不标记为原生计算迁移", false)
			}
			if fp.Policy == PolicyNative && field.Kind == string(v2.LogicalRelation) {
				if field.Relation == nil || !selected[field.Relation.TargetTableID] {
					add("source_import.relation_selection", id, field.ID, "", "关联目标未选择，请补选或明确确认快照/跳过策略", true)
				} else if field.Relation.TargetFieldID == "" && !options.ConfirmReverse {
					add("source_import.reverse_confirmation", id, field.ID, "", "本地关系需要新增反向字段，请确认模型变化", true)
				}
			}
			tp.Fields = append(tp.Fields, fp)
		}
		unknown := make([]string, 0, len(decisions))
		for key := range decisions {
			if key.TableID == id && !seenFields[key.FieldID] {
				unknown = append(unknown, key.FieldID)
			}
		}
		sort.Strings(unknown)
		for _, fieldID := range unknown {
			add("source_import.decision_unknown", id, fieldID, "", "字段策略引用不存在的字段", true)
		}
		for _, row := range tp.Records {
			extra := make([]string, 0, len(row.Values))
			for key := range row.Values {
				if !seenFields[key] {
					extra = append(extra, key)
				}
			}
			sort.Strings(extra)
			for _, key := range extra {
				add("source_import.value_unknown", id, key, row.ID, "记录包含未声明字段", true)
			}
		}
		plan.Tables = append(plan.Tables, tp)
	}
	// The provenance summaries must fit the durable Result JSON column. An
	// oversized summary blocks instead of silently truncating expressions.
	plan.Fields = buildFieldSummaries(&plan)
	provenance, err := json.Marshal(plan.Fields)
	if err != nil {
		return Plan{}, &Error{Code: "source_import.source_invalid", Message: "来源字段溯源摘要无法序列化"}
	}
	if len(provenance) > MaxProvenanceBytes {
		add("source_import.provenance_capacity", "", "", "",
			fmt.Sprintf("字段溯源摘要 %d 字节超过 %d 预算；不会截断来源表达式，请缩小迁移范围或对大表达式字段确认跳过后重新预检",
				len(provenance), MaxProvenanceBytes), true)
	}
	// Same-table column name collisions (including the reciprocal fields this
	// migration introduces) need an explicit user rename before anything runs.
	checkFieldNames(&plan, decisions, add)
	// Attachment-derived file limits must freeze before any definition or
	// value validation so the validated draft is exactly the executed draft
	// instead of defaults widened after the review.
	deriveFileLimits(source.Attachments, &plan, add)
	if err := validatePlanValues(ctx, &plan, rowIDs, add); err != nil {
		return Plan{}, err
	}
	validateRelations(&plan, add)
	validateAttachments(source.Attachments, &plan, add)
	if len(plan.Attachments) > 0 {
		add("source_import.attachment_pending", "", "", "",
			"附件仅完成元数据预检；确认执行前所有所选附件字节必须经 Host 受控暂存成功上传，执行仅使用稳定句柄并取消时仅清理本任务暂存，传输成功前不得视为已迁移", false)
	}
	collector.finalize()
	return plan, nil
}

func validID(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n")
}

// reciprocalDefaultName is the fixed local display name for the reverse
// field created when a one-sided source relation has no source-side reverse.
const reciprocalDefaultName = "迁移反向关联"

// checkFieldNames blocks same-table column name collisions, including the
// reciprocal fields preflight introduces for one-sided source relations.
// Renaming is always an explicit Decision.TargetName; labels never guess
// identities and source columns are never silently renamed.
func checkFieldNames(plan *Plan, decisions map[Key]Decision, add addDiagnostic) {
	planTables := make(map[string]bool, len(plan.Tables))
	for _, table := range plan.Tables {
		planTables[table.SourceID] = true
	}
	claims := map[string]map[string][]string{}
	claim := func(tableID, name, owner string) {
		if claims[tableID] == nil {
			claims[tableID] = map[string][]string{}
		}
		claims[tableID][name] = append(claims[tableID][name], owner)
	}
	for _, table := range plan.Tables {
		for _, field := range table.Fields {
			if field.Policy == PolicySkip {
				continue
			}
			decision := decisions[Key{TableID: table.SourceID, FieldID: field.Source.ID}]
			renamed := strings.TrimSpace(decision.TargetName)
			oneSided := field.Policy == PolicyNative &&
				field.Source.Kind == string(v2.LogicalRelation) &&
				field.Source.Relation != nil && field.Source.Relation.TargetFieldID == ""
			name := strings.TrimSpace(field.Source.Name)
			if !oneSided && renamed != "" {
				name = renamed
			}
			if name != "" {
				claim(table.SourceID, name, "field/"+field.Source.ID)
			}
			if oneSided && planTables[field.Source.Relation.TargetTableID] {
				reciprocal := renamed
				if reciprocal == "" {
					reciprocal = reciprocalDefaultName
				}
				claim(field.Source.Relation.TargetTableID, reciprocal,
					"reciprocal/"+table.SourceID+"/"+field.Source.ID)
			}
		}
	}
	type nameConflict struct {
		tableID string
		name    string
		owners  []string
	}
	conflicts := make([]nameConflict, 0)
	for tableID, byName := range claims {
		for name, owners := range byName {
			if len(owners) > 1 {
				conflicts = append(conflicts, nameConflict{tableID: tableID, name: name, owners: owners})
			}
		}
	}
	sort.Slice(conflicts, func(i, j int) bool {
		if conflicts[i].tableID != conflicts[j].tableID {
			return conflicts[i].tableID < conflicts[j].tableID
		}
		return conflicts[i].name < conflicts[j].name
	})
	for _, conflict := range conflicts {
		add("source_import.field_name_conflict", conflict.tableID, "", "",
			fmt.Sprintf("目标列名 %q 在同一张表内被 %v 重复使用；请通过 Decision.targetName 显式重命名，不会按标签猜测或自动改名",
				conflict.name, conflict.owners), true)
	}
}

// buildFieldSummaries freezes the durable Result provenance shape: one entry
// per selected, planned source field keyed by composite source identity.
func buildFieldSummaries(plan *Plan) []FieldSummary {
	summaries := make([]FieldSummary, 0, len(plan.Tables))
	for _, table := range plan.Tables {
		for _, field := range table.Fields {
			summaries = append(summaries, FieldSummary{
				Source: Key{Provider: plan.Provider, ContainerID: plan.ContainerID,
					TableID: table.SourceID, FieldID: field.Source.ID},
				Kind:       field.Source.Kind,
				Policy:     field.Policy,
				Definition: field.Source.Definition,
			})
		}
	}
	return summaries
}

func planField(field Field, decision Decision) (FieldPlan, error) {
	fp := FieldPlan{Source: field, Policy: decision.Policy}
	if decision.Policy == PolicySkip {
		if !decision.Confirmed {
			return fp, fmt.Errorf("跳过字段必须明确确认")
		}
		return fp, nil
	}
	if decision.Policy != PolicyNative && decision.Policy != PolicySnapshot {
		return fp, fmt.Errorf("字段策略阻断或不受支持")
	}
	kind := v2.LogicalType(field.Kind)
	if decision.Policy == PolicySnapshot {
		if !decision.Confirmed {
			return fp, fmt.Errorf("值快照必须明确确认")
		}
		kind = decision.TargetKind
		if kind == "" {
			kind = field.ValueKind
		}
		if kind == "" {
			kind = v2.LogicalJSON
		}
	} else if field.Kind == "person" || field.Kind == "system" || field.Kind == "unknown" ||
		kind == v2.LogicalFormula || kind == v2.LogicalLookup || kind == v2.LogicalAutoNumber || kind == v2.LogicalAutoDate {
		return fp, fmt.Errorf("本版本未验证该来源类型的可执行转换，请确认值快照或跳过")
	}
	if kind == v2.LogicalFormula || kind == v2.LogicalLookup || kind == v2.LogicalAutoNumber || kind == v2.LogicalAutoDate ||
		(decision.Policy == PolicySnapshot && (kind == v2.LogicalRelation || kind == v2.LogicalFile)) {
		return fp, fmt.Errorf("快照目标必须是普通可写值字段")
	}
	r, err := v2.RecommendedDefaults(kind)
	if err != nil {
		return fp, err
	}
	fp.Draft = v2.FieldDraft{DisplayName: field.Name, LogicalType: kind, Value: r.Value,
		Constraints: r.Constraints, Storage: r.Storage, Display: r.Display, File: r.File, JSON: r.JSON}
	fp.Draft.Value.Required = field.Required
	if renamed := strings.TrimSpace(decision.TargetName); renamed != "" {
		if kind == v2.LogicalRelation && field.Relation != nil && field.Relation.TargetFieldID == "" {
			if utf8.RuneCountInString(renamed) > 256 || strings.ContainsAny(renamed, "\x00\r\n") {
				return fp, fmt.Errorf("反向字段名称无效")
			}
			fp.ReciprocalName = renamed
		} else {
			fp.Draft.DisplayName = renamed
		}
	}
	if kind == v2.LogicalRelation {
		if field.Relation == nil || (field.Relation.Cardinality != "one" && field.Relation.Cardinality != "many") {
			return fp, fmt.Errorf("来源关系目标或基数无效")
		}
		fp.Draft.Relation = &v2.RelationSpec{TargetTableID: field.Relation.TargetTableID,
			Cardinality: field.Relation.Cardinality, DeletePolicy: "setNull", DisplayField: "pending"}
		fp.Deferred = field.Required
		fp.Draft.Value.Required = false
	}
	if kind == v2.LogicalFile {
		if field.Required {
			return fp, fmt.Errorf("本地文件字段不支持插入时必填，必须确认可选文件策略")
		}
	}
	if kind == v2.LogicalSelect || kind == v2.LogicalMultiSelect {
		// A select target is only supported with an explicit source option
		// identity mapping; labels are display data, never identities.
		if decision.Policy == PolicySnapshot && len(field.Options) == 0 {
			return fp, fmt.Errorf("快照到选择字段必须声明来源选项映射")
		}
		seen := map[string]bool{}
		fp.Draft.Select = &v2.SelectSpec{Options: []v2.SelectOption{}}
		for index, option := range field.Options {
			if !validID(option.ID) || seen[option.ID] {
				return fp, fmt.Errorf("来源选项 ID 缺失或重复")
			}
			seen[option.ID] = true
			fp.Draft.Select.Options = append(fp.Draft.Select.Options, v2.SelectOption{Label: option.Label,
				Color: option.Color, Order: index, State: v2.OptionActive})
		}
	}
	if kind == v2.LogicalNumber && field.NumberFormat != nil {
		f := field.NumberFormat
		fp.Draft.Storage.Options.OnlyInt = f.OnlyInt
		fp.Draft.Display.DisplayScale, fp.Draft.Display.ScaleMode = f.DisplayScale, f.ScaleMode
		fp.Draft.Display.TrimTrailingZeros, fp.Draft.Display.UseGrouping = f.TrimTrailingZeros, f.UseGrouping
		fp.Draft.Display.Currency, fp.Draft.Display.PercentStorage, fp.Draft.Display.Unit = f.Currency, f.PercentStorage, f.Unit
	}
	if field.Timezone != "" {
		fp.Draft.Display.Timezone = field.Timezone
	}
	return fp, nil
}

func previewDefinition(ctx context.Context, field FieldPlan) (v2.FieldDefinition, error) {
	a := v2.NewIdentityAllocator(nil)
	id, err := a.AllocateField(ctx)
	if err != nil {
		return v2.FieldDefinition{}, err
	}
	d := field.Draft
	definition := v2.FieldDefinition{Contract: v2.Contract, Identity: id, DisplayName: d.DisplayName,
		Help: d.Help, LogicalType: d.LogicalType, Lifecycle: v2.Lifecycle{State: v2.LifecycleActive},
		Value: d.Value, Constraints: d.Constraints, Storage: d.Storage, Display: d.Display,
		File: d.File, JSON: d.JSON, Relation: d.Relation}
	if d.Value.Presence.Mode == v2.PresenceCompanion {
		definition.Value.Presence, err = a.AllocatePresence(ctx, id.PhysicalName)
		if err != nil {
			return definition, err
		}
	}
	if d.Select != nil {
		definition.Select = &v2.SelectSpec{Options: append([]v2.SelectOption{}, d.Select.Options...)}
		for i := range definition.Select.Options {
			definition.Select.Options[i].OptionID, err = a.AllocateOption(ctx)
			if err != nil {
				return definition, err
			}
		}
	}
	return definition, v2.Validate(definition)
}

func exactNumber(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return true
	}
	f, err := strconv.ParseFloat(number.String(), 64)
	if err != nil {
		return false
	}
	before, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return false
	}
	after, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return ok && before.Cmp(after) == 0
}
