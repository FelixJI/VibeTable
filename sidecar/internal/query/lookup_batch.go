package query

import (
	"fmt"
	"strings"
)

// CompileMatchBatch finds source records for several current-row predicates in
// one source scan. Nil groups never match; they represent a null operand. Every
// non-nil group uses the same typed predicates and archive policy as QueryPage.
func CompileMatchBatch(descriptor TableDescriptor, groups [][]FilterExpression, afterID string, limit int) (CompiledQuery, error) {
	if len(groups) == 0 || len(groups) > 64 || limit < 1 || limit > 256 {
		return CompiledQuery{}, productError("query.lookup.invalid_batch", "groups", "lookup supports 1–64 predicate groups and 1–256 source rows", nil)
	}
	if err := validateDescriptor(descriptor); err != nil {
		return CompiledQuery{}, err
	}
	c := &compiler{descriptor: descriptor, params: make(map[string]any), exactNull: true}
	predicates := make([]string, len(groups))
	flags := make([]string, len(groups))
	for i, filters := range groups {
		predicate := "0"
		if filters != nil {
			if len(filters) == 0 {
				return CompiledQuery{}, productError("query.lookup.empty_condition", "groups", "lookup condition cannot be empty", nil)
			}
			normalized, err := Normalize(TableQuery{Filters: filters, Limit: limit})
			if err != nil {
				return CompiledQuery{}, err
			}
			predicate, err = c.compileFilterList(normalized.Filters, fmt.Sprintf("groups[%d]", i))
			if err != nil {
				return CompiledQuery{}, err
			}
		}
		predicates[i] = "(" + predicate + ")"
		flags[i] = "CASE WHEN " + predicate + " THEN 1 ELSE 0 END"
	}
	where := "(" + strings.Join(predicates, " OR ") + ")"
	archive, err := c.compileArchive()
	if err != nil {
		return CompiledQuery{}, err
	}
	if archive != "" {
		where += " AND (" + archive + ")"
	}
	resolvedPrimary, err := c.resolve(descriptor.PrimaryKey, "primaryKey")
	if err != nil {
		return CompiledQuery{}, err
	}
	primary := resolvedPrimary.sql
	where += " AND " + primary + " > " + c.bind(afterID)
	sql := "SELECT " + primary + " AS id, json_array(" + strings.Join(flags, ",") + ") AS matches FROM " +
		quote(descriptor.PhysicalName) + " WHERE " + where + " ORDER BY " + primary + " LIMIT " + c.bind(limit)
	return CompiledQuery{SQL: sql, Params: c.params, Fields: []string{"id", "matches"}}, nil
}
