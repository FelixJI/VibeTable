package sourceimport

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibetable/vibetable/sidecar/internal/fieldvalue"
	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

const (
	maxAttachments = 10000
	// The attachment Manager stages at most 256MiB/1000 files globally. This
	// migration never promises beyond that capability: selected transfers stay
	// within half of the global budget, single files within the staged limit,
	// and the existing Stage channel rejects zero-byte uploads.
	maxSelectedAttachmentFiles = 500
	maxAttachmentBytes         = 32 << 20
	maxTotalAttachmentBytes    = 128 << 20
	maxFileValuesPerRecord     = 100
)

type addDiagnostic func(string, string, string, string, string, bool)

func validatePlanValues(ctx context.Context, plan *Plan, rows map[Key]bool, add addDiagnostic) error {
	kernel := fieldvalue.New()
	for _, table := range plan.Tables {
		ordinaryValues := make(map[string]map[string]any, len(table.Records))
		for _, row := range table.Records {
			ordinaryValues[row.ID] = map[string]any{}
		}
		for _, field := range table.Fields {
			if err := ctx.Err(); err != nil {
				return err
			}
			if field.Policy == PolicySkip {
				continue
			}
			definition, err := previewDefinition(ctx, field)
			if err != nil {
				// Cancellation must surface as an error even when it first looks
				// like a field failure; it is never recorded as a diagnostic.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
				add("source_import.field_invalid", table.SourceID, field.Source.ID, "", err.Error(), true)
				continue
			}
			for _, row := range table.Records {
				if err := ctx.Err(); err != nil {
					return err
				}
				value, supplied := row.Values[field.Source.ID]
				if field.Policy == PolicyNative && field.Source.Kind == string(v2.LogicalRelation) {
					refs, refErr := relationReferences(field.Source, value)
					if refErr != nil {
						add("source_import.relation_value", table.SourceID, field.Source.ID, row.ID, refErr.Error(), true)
						continue
					}
					if field.Source.Required && len(refs) == 0 {
						add("source_import.required", table.SourceID, field.Source.ID, row.ID, "来源必填关系为空", true)
					}
					seen := map[string]bool{}
					for _, ref := range refs {
						if seen[ref] || !rows[Key{TableID: field.Source.Relation.TargetTableID, RecordID: ref}] {
							add("source_import.relation_target", table.SourceID, field.Source.ID, row.ID, "关联记录不存在或重复；不能静默置空", true)
						}
						seen[ref] = true
					}
					continue
				}
				if field.Policy == PolicyNative && field.Source.Kind == string(v2.LogicalFile) {
					continue
				}
				canonical, normalizeErr := CanonicalValue(field, definition, value)
				if normalizeErr == nil {
					_, normalizeErr = kernel.NormalizeWrite(ctx, definition, fieldvalue.Insert, fieldvalue.Input{Supplied: supplied, Value: canonical})
				}
				if normalizeErr != nil {
					add("source_import.value_invalid", table.SourceID, field.Source.ID, row.ID, normalizeErr.Error(), true)
				} else if supplied {
					ordinaryValues[row.ID][definition.Identity.FieldID] = canonical
				}
			}
		}
		for _, row := range table.Records {
			encoded, err := json.Marshal(ordinaryValues[row.ID])
			// Reserve the bounded mutation envelope and stable request IDs.
			// A row cannot be split across atomic insert operations.
			if err != nil || len(encoded) > batchBytes-4096 {
				add("source_import.record.capacity", table.SourceID, "", row.ID, "单条记录超过迁移批次容量，请缩小字段范围后重新预检", true)
			}
		}
	}
	return nil
}

// CanonicalValue translates stable option identities and approved snapshots;
// the FieldValueKernel remains the authority for all writable value semantics.
// Cloud numbers never pass through CSV display-string parsing.
func CanonicalValue(field FieldPlan, definition v2.FieldDefinition, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if field.Policy == PolicySnapshot {
		switch definition.LogicalType {
		case v2.LogicalText:
			if text, ok := value.(string); ok {
				return text, nil
			}
			raw, err := json.Marshal(value)
			return string(raw), err
		case v2.LogicalNumber:
			if !exactNumber(value) {
				return nil, fmt.Errorf("数值精度超过本地可保真范围，请选择精确文本快照")
			}
			return value, nil
		case v2.LogicalSelect:
			// A select snapshot is only mapped through the explicit source
			// option identity table; labels are never identities.
			id, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("单选快照值必须携带来源选项 ID")
			}
			return optionIdentity(field.Source, definition, id)
		case v2.LogicalMultiSelect:
			ids, err := stringList(value)
			if err != nil {
				return nil, fmt.Errorf("多选快照值必须携带来源选项 ID 数组")
			}
			mapped := make([]string, 0, len(ids))
			for _, id := range ids {
				local, err := optionIdentity(field.Source, definition, id)
				if err != nil {
					return nil, err
				}
				mapped = append(mapped, local)
			}
			return mapped, nil
		}
		return value, nil
	}
	switch definition.LogicalType {
	case v2.LogicalNumber:
		if !exactNumber(value) {
			return nil, fmt.Errorf("数值精度超过本地可保真范围，请选择精确文本快照")
		}
	case v2.LogicalSelect:
		id, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("单选值必须携带来源选项 ID")
		}
		return optionIdentity(field.Source, definition, id)
	case v2.LogicalMultiSelect:
		ids, err := stringList(value)
		if err != nil {
			return nil, fmt.Errorf("多选值必须携带来源选项 ID 数组")
		}
		result := make([]string, 0, len(ids))
		for _, id := range ids {
			local, err := optionIdentity(field.Source, definition, id)
			if err != nil {
				return nil, err
			}
			result = append(result, local)
		}
		return result, nil
	}
	return value, nil
}

func optionIdentity(source Field, definition v2.FieldDefinition, id string) (string, error) {
	if definition.Select == nil || len(source.Options) != len(definition.Select.Options) {
		return "", fmt.Errorf("选项身份映射不完整")
	}
	for index, option := range source.Options {
		if option.ID == id {
			return definition.Select.Options[index].OptionID, nil
		}
	}
	return "", fmt.Errorf("来源选项 ID 不存在，不能按同名标签猜测")
}

func stringList(value any) ([]string, error) {
	switch values := value.(type) {
	case []string:
		return append([]string{}, values...), nil
	case []any:
		result := make([]string, len(values))
		for i, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("身份数组包含非字符串")
			}
			result[i] = text
		}
		return result, nil
	default:
		return nil, fmt.Errorf("身份值不是数组")
	}
}

func relationReferences(field Field, value any) ([]string, error) {
	if value == nil {
		return []string{}, nil
	}
	if field.Relation == nil {
		return nil, fmt.Errorf("关联定义缺失")
	}
	if field.Relation.Cardinality == "one" {
		id, ok := value.(string)
		if !ok || !validID(id) {
			return nil, fmt.Errorf("单值关联必须携带来源记录 ID")
		}
		return []string{id}, nil
	}
	return stringList(value)
}

func validateRelations(plan *Plan, add addDiagnostic) {
	tables := map[string]TablePlan{}
	records := map[Key]Record{}
	fields := map[Key]FieldPlan{}
	for _, table := range plan.Tables {
		tables[table.SourceID] = table
		for _, row := range table.Records {
			records[Key{TableID: table.SourceID, RecordID: row.ID}] = row
		}
		for _, field := range table.Fields {
			fields[Key{TableID: table.SourceID, FieldID: field.Source.ID}] = field
		}
	}
	for _, table := range plan.Tables {
		primary, primaryExists := fields[Key{TableID: table.SourceID, FieldID: table.PrimaryFieldID}]
		// The local display authority (fieldchange.isDisplayField) excludes
		// relation, formula, lookup and autoDate primaries; file primaries are
		// supported. Relation display fields map the source primary, so every
		// selected table's primary must be locally display-eligible.
		if !primaryExists || primary.Policy == PolicySkip ||
			primary.Draft.LogicalType == v2.LogicalRelation ||
			primary.Draft.LogicalType == v2.LogicalFormula ||
			primary.Draft.LogicalType == v2.LogicalLookup ||
			primary.Draft.LogicalType == v2.LogicalAutoDate {
			add("source_import.primary_field", table.SourceID, table.PrimaryFieldID, "", "主显示字段必须是可迁移的普通值字段（关系、公式、查找、自动日期不可作为主显示）", true)
		}
		for _, field := range table.Fields {
			if field.Policy != PolicyNative || field.Source.Relation == nil {
				continue
			}
			relation := field.Source.Relation
			if _, ok := tables[relation.TargetTableID]; !ok {
				continue
			}
			if relation.TargetFieldID == "" {
				continue
			}
			reverse, ok := fields[Key{TableID: relation.TargetTableID, FieldID: relation.TargetFieldID}]
			if !ok || reverse.Policy != PolicyNative || reverse.Source.Relation == nil ||
				reverse.Source.Relation.TargetTableID != table.SourceID ||
				reverse.Source.Relation.TargetFieldID != field.Source.ID {
				add("source_import.relation_pair", table.SourceID, field.Source.ID, "", "来源双向关系两端不一致，请选择一致策略后重新预检", true)
				continue
			}
			for _, row := range table.Records {
				refs, err := relationReferences(field.Source, row.Values[field.Source.ID])
				if err != nil {
					continue
				}
				for _, id := range refs {
					other, exists := records[Key{TableID: relation.TargetTableID, RecordID: id}]
					if !exists {
						continue
					}
					backRefs, err := relationReferences(reverse.Source, other.Values[reverse.Source.ID])
					if err != nil {
						continue
					}
					found := false
					for _, back := range backRefs {
						if back == row.ID {
							found = true
							break
						}
					}
					if !found {
						add("source_import.relation_edges", table.SourceID, field.Source.ID, row.ID, "来源双向边集不一致；不能用后写空值删除已迁移关系", true)
					}
				}
			}
		}
	}
}

func attachmentIndex(attachments []Attachment) map[Key]Attachment {
	index := make(map[Key]Attachment, len(attachments))
	for _, attachment := range attachments {
		index[Key{TableID: attachment.TableID, FieldID: attachment.FieldID,
			RecordID: attachment.RecordID, ObjectID: attachment.ID}] = attachment
	}
	return index
}

// deriveFileLimits freezes attachment-derived file draft limits before any
// definition or value validation, so the reviewed and executed drafts are the
// same object instead of defaults widened after the fact.
func deriveFileLimits(attachments []Attachment, plan *Plan, add addDiagnostic) {
	index := attachmentIndex(attachments)
	for ti := range plan.Tables {
		table := &plan.Tables[ti]
		for fi := range table.Fields {
			field := &table.Fields[fi]
			if field.Policy != PolicyNative || field.Source.Kind != string(v2.LogicalFile) ||
				field.Draft.File == nil {
				continue
			}
			for _, row := range table.Records {
				value := row.Values[field.Source.ID]
				if value == nil {
					continue
				}
				ids, err := stringList(value)
				if err != nil {
					continue
				}
				if len(ids) > field.Draft.File.MaxFiles {
					field.Draft.File.MaxFiles = len(ids)
				}
				for _, id := range ids {
					attachment, ok := index[Key{TableID: table.SourceID, FieldID: field.Source.ID,
						RecordID: row.ID, ObjectID: id}]
					if !ok || attachment.Size <= 0 || attachment.Size > maxAttachmentBytes {
						continue
					}
					if attachment.Size > field.Draft.File.MaxBytesPerFile {
						field.Draft.File.MaxBytesPerFile = attachment.Size
					}
				}
			}
			if field.Draft.File.MaxFiles > maxFileValuesPerRecord {
				add("source_import.attachment_capacity", table.SourceID, field.Source.ID, "", "单条记录附件数量超过容量", true)
			}
		}
	}
}

func validateAttachments(attachments []Attachment, plan *Plan, add addDiagnostic) {
	if len(attachments) > maxAttachments {
		add("source_import.attachment_capacity", "", "", "", "附件数量超过本次容量", true)
		return
	}
	metadata := map[Key]Attachment{}
	duplicates := map[Key]bool{}
	for _, attachment := range attachments {
		key := Key{TableID: attachment.TableID, FieldID: attachment.FieldID,
			RecordID: attachment.RecordID, ObjectID: attachment.ID}
		if _, exists := metadata[key]; exists && !duplicates[key] {
			duplicates[key] = true
			add("source_import.attachment_identity", key.TableID, key.FieldID, key.RecordID, "附件复合 ID 重复", true)
		}
		metadata[key] = attachment
	}
	var totalBytes int64
	for ti := range plan.Tables {
		table := &plan.Tables[ti]
		for fi := range table.Fields {
			field := &table.Fields[fi]
			if field.Policy != PolicyNative || field.Source.Kind != string(v2.LogicalFile) {
				continue
			}
			for _, row := range table.Records {
				value := row.Values[field.Source.ID]
				if value == nil {
					continue
				}
				ids, err := stringList(value)
				if err != nil {
					add("source_import.attachment_value", table.SourceID, field.Source.ID, row.ID, "文件字段必须携带附件来源 ID 数组", true)
					continue
				}
				seen := map[string]bool{}
				for _, id := range ids {
					key := Key{TableID: table.SourceID, FieldID: field.Source.ID, RecordID: row.ID, ObjectID: id}
					attachment, ok := metadata[key]
					if !ok || seen[id] || !attachmentMetadataSafe(attachment) {
						add("source_import.attachment_metadata", table.SourceID, field.Source.ID, row.ID,
							"附件元数据缺失、重复或不安全（名称、类型或容量），不能视为已迁移", true)
						continue
					}
					seen[id] = true
					totalBytes += attachment.Size
					plan.Attachments = append(plan.Attachments, attachment)
				}
			}
		}
	}
	if len(plan.Attachments) > maxSelectedAttachmentFiles {
		add("source_import.attachment_capacity", "", "", "",
			fmt.Sprintf("本次所选附件 %d 个超过 %d 上限；不会提高既有暂存限额，请缩小任务范围后重新预检",
				len(plan.Attachments), maxSelectedAttachmentFiles), true)
	}
	if totalBytes > maxTotalAttachmentBytes {
		add("source_import.attachment_capacity", "", "", "",
			fmt.Sprintf("本次所选附件总大小 %d 字节超过 %d 字节上限；不会提高既有暂存限额，请缩小任务范围后重新预检",
				totalBytes, maxTotalAttachmentBytes), true)
	}
}

// attachmentMetadataSafe enforces the existing controlled upload contract:
// concrete basenames without separators, "." or "..", control characters or
// oversized names, bounded sizes, and a concrete MIME type. StageOwned
// rejects zero-byte uploads, so non-positive sizes never preview as
// migratable. Source bytes are transferred later by the Host; preflight
// introduces no new hash here.
func attachmentMetadataSafe(attachment Attachment) bool {
	if !validID(attachment.ID) || attachment.Size <= 0 || attachment.Size > maxAttachmentBytes {
		return false
	}
	name := attachment.Name
	if name == "" || strings.TrimSpace(name) == "" {
		return false
	}
	safeName := path.Base(strings.ReplaceAll(name, "\\", "/"))
	if safeName != name || safeName == "." || safeName == ".." || safeName == "/" ||
		len(safeName) > 255 || !utf8.ValidString(safeName) ||
		strings.IndexFunc(safeName, unicode.IsControl) >= 0 {
		return false
	}
	return validMIME(attachment.MIME)
}

// validMIME mirrors the attachment manager's type matching shape: a concrete
// case-insensitive type/subtype pair, optionally with parameters. Wildcards
// are only valid in field policies, never in source attachment metadata.
func validMIME(value string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	slash := strings.Index(base, "/")
	if slash <= 0 || slash == len(base)-1 {
		return false
	}
	return validMIMEToken(base[:slash]) && validMIMEToken(base[slash+1:])
}

func validMIMEToken(token string) bool {
	if token == "" {
		return false
	}
	for _, symbol := range token {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= '0' && symbol <= '9':
		case strings.ContainsRune("!#$&^_.+-", symbol):
		default:
			return false
		}
	}
	return true
}
