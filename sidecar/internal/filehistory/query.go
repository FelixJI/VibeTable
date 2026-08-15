package filehistory

import (
	"container/heap"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

var (
	ErrDocumentQueryInvalid = errors.New("file_history.query_invalid")
	ErrDocumentCursorStale  = errors.New("file_history.cursor_stale")
)

type DocumentFilter struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

type DocumentSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

type DocumentQueryRequest struct {
	Logic   string           `json:"logic"`
	Filters []DocumentFilter `json:"filters"`
	Sort    []DocumentSort   `json:"sort"`
	Limit   int              `json:"limit"`
	Cursor  *string          `json:"cursor"`
}

type FileDocumentSummary struct {
	ContractVersion            string         `json:"contractVersion"`
	DocumentID                 string         `json:"documentId"`
	RelativePath               string         `json:"relativePath"`
	DisplayName                string         `json:"displayName"`
	Extension                  string         `json:"extension"`
	MimeType                   string         `json:"mimeType"`
	SizeBytes                  int64          `json:"sizeBytes"`
	EffectiveRevisionID        string         `json:"effectiveRevisionId"`
	EffectiveRevisionCreatedAt time.Time      `json:"effectiveRevisionCreatedAt"`
	FormalVersion              *uint64        `json:"formalVersion"`
	Status                     DocumentStatus `json:"status"`
}

type DocumentQueryResult struct {
	Documents        []FileDocumentSummary `json:"documents"`
	NextCursor       *string               `json:"nextCursor"`
	TopologyRevision uint64                `json:"topologyRevision"`
}

type documentCursor struct {
	TopologyRevision uint64              `json:"topologyRevision"`
	Fingerprint      string              `json:"fingerprint"`
	After            FileDocumentSummary `json:"after"`
	Checksum         string              `json:"checksum"`
}

// QueryDocuments returns a bounded, revision-bound projection of FileHistory.
// The immutable revision tree remains private to this module; callers receive
// only fields whose semantics are defined by the effective revision.
func (service *Service) QueryDocuments(request DocumentQueryRequest) (DocumentQueryResult, error) {
	if err := validateDocumentQuery(request); err != nil {
		return DocumentQueryResult{}, err
	}
	fingerprint, err := documentQueryFingerprint(request)
	if err != nil {
		return DocumentQueryResult{}, ErrDocumentQueryInvalid
	}

	var after *FileDocumentSummary
	var cursor documentCursor
	if request.Cursor != nil {
		var decodeErr error
		cursor, decodeErr = decodeDocumentCursor(*request.Cursor)
		if decodeErr != nil || cursor.Fingerprint != fingerprint {
			return DocumentQueryResult{}, ErrDocumentQueryInvalid
		}
		after = &cursor.After
	}

	orders := normalizedDocumentSort(request.Sort)
	candidates := &documentSummaryHeap{
		orders: orders,
		items:  make([]FileDocumentSummary, 0, request.Limit+1),
	}
	service.mu.RLock()
	topologyRevision := service.headMutationRevision
	if after != nil && cursor.TopologyRevision != topologyRevision {
		service.mu.RUnlock()
		return DocumentQueryResult{}, ErrDocumentCursorStale
	}
	for _, document := range service.documents {
		summary, projectionErr := summarizeDocument(document)
		if projectionErr != nil {
			service.mu.RUnlock()
			return DocumentQueryResult{}, projectionErr
		}
		if !matchesDocumentFilters(summary, request.Logic, request.Filters) ||
			(after != nil && compareDocumentSummaries(summary, *after, orders) <= 0) {
			continue
		}
		if candidates.Len() < request.Limit+1 {
			heap.Push(candidates, summary)
			continue
		}
		if compareDocumentSummaries(summary, candidates.items[0], orders) < 0 {
			heap.Pop(candidates)
			heap.Push(candidates, summary)
		}
	}
	service.mu.RUnlock()

	summaries := candidates.items
	sortDocumentSummaries(summaries, orders)
	hasMore := len(summaries) > request.Limit
	if hasMore {
		summaries = summaries[:request.Limit]
	}
	var next *string
	if hasMore {
		encoded, encodeErr := encodeDocumentCursor(documentCursor{
			TopologyRevision: topologyRevision,
			Fingerprint:      fingerprint,
			After:            summaries[len(summaries)-1],
		})
		if encodeErr != nil {
			return DocumentQueryResult{}, encodeErr
		}
		next = &encoded
	}
	page := cloneDocumentSummaries(summaries)
	return DocumentQueryResult{
		Documents:        page,
		NextCursor:       next,
		TopologyRevision: topologyRevision,
	}, nil
}

type documentSummaryHeap struct {
	orders []DocumentSort
	items  []FileDocumentSummary
}

func (summaries documentSummaryHeap) Len() int { return len(summaries.items) }

func (summaries documentSummaryHeap) Less(left, right int) bool {
	return compareDocumentSummaries(
		summaries.items[left], summaries.items[right], summaries.orders,
	) > 0
}

func (summaries documentSummaryHeap) Swap(left, right int) {
	summaries.items[left], summaries.items[right] = summaries.items[right], summaries.items[left]
}

func (summaries *documentSummaryHeap) Push(value any) {
	summaries.items = append(summaries.items, value.(FileDocumentSummary))
}

func (summaries *documentSummaryHeap) Pop() any {
	last := len(summaries.items) - 1
	value := summaries.items[last]
	summaries.items = summaries.items[:last]
	return value
}

func validateDocumentQuery(request DocumentQueryRequest) error {
	if (request.Logic != "and" && request.Logic != "or") ||
		request.Limit < 1 || request.Limit > 500 ||
		len(request.Filters) > 20 || len(request.Sort) > 3 {
		return ErrDocumentQueryInvalid
	}
	for _, filter := range request.Filters {
		if !validDocumentFilter(filter) {
			return ErrDocumentQueryInvalid
		}
	}
	for _, order := range request.Sort {
		if !documentField(order.Field) || (order.Direction != "asc" && order.Direction != "desc") {
			return ErrDocumentQueryInvalid
		}
	}
	return nil
}

func validDocumentFilter(filter DocumentFilter) bool {
	switch filter.Field {
	case "documentId", "displayName", "relativePath", "extension", "mimeType":
		return stringPredicate(filter.Operator, filter.Value)
	case "status":
		if !stringPredicate(filter.Operator, filter.Value) || filter.Operator == "contains" {
			return false
		}
		for _, value := range stringValues(filter.Value) {
			if value != "active" && value != "deleted" {
				return false
			}
		}
		return true
	case "sizeBytes":
		return numberPredicate(filter.Operator, filter.Value)
	case "effectiveRevisionCreatedAt":
		return timePredicate(filter.Operator, filter.Value)
	default:
		return false
	}
}

func documentField(field string) bool {
	switch field {
	case "documentId", "displayName", "relativePath", "extension", "mimeType", "sizeBytes", "effectiveRevisionCreatedAt", "formalVersion", "status":
		return true
	default:
		return false
	}
}

func stringPredicate(operator string, value any) bool {
	switch operator {
	case "eq", "contains":
		_, ok := value.(string)
		return ok
	case "in":
		return len(stringValues(value)) > 0
	default:
		return false
	}
}

func numberPredicate(operator string, value any) bool {
	switch operator {
	case "eq", "gt", "gte", "lt", "lte":
		_, ok := numberValue(value)
		return ok
	case "between":
		values := anySlice(value)
		if len(values) != 2 {
			return false
		}
		_, left := numberValue(values[0])
		_, right := numberValue(values[1])
		return left && right
	default:
		return false
	}
}

func timePredicate(operator string, value any) bool {
	switch operator {
	case "eq", "before", "after":
		_, ok := timeValue(value)
		return ok
	case "between":
		values := anySlice(value)
		if len(values) != 2 {
			return false
		}
		_, left := timeValue(values[0])
		_, right := timeValue(values[1])
		return left && right
	default:
		return false
	}
}

func summarizeDocument(document Document) (FileDocumentSummary, error) {
	var effective *Revision
	for index := range document.Revisions {
		if document.Revisions[index].RevisionID == document.EffectiveRevisionID {
			effective = &document.Revisions[index]
			break
		}
	}
	if effective == nil {
		return FileDocumentSummary{}, ErrStateCorrupt
	}
	displayName := path.Base(strings.ReplaceAll(document.RelativePath, `\`, "/"))
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(displayName)), ".")
	return FileDocumentSummary{
		ContractVersion:            contractVersion,
		DocumentID:                 document.DocumentID,
		RelativePath:               document.RelativePath,
		DisplayName:                displayName,
		Extension:                  extension,
		MimeType:                   effective.MimeType,
		SizeBytes:                  effective.Size,
		EffectiveRevisionID:        effective.RevisionID,
		EffectiveRevisionCreatedAt: effective.CreatedAt,
		FormalVersion:              effective.FormalVersion,
		Status:                     document.Status,
	}, nil
}

func matchesDocumentFilters(summary FileDocumentSummary, logic string, filters []DocumentFilter) bool {
	if len(filters) == 0 {
		return true
	}
	matched := logic == "and"
	for _, filter := range filters {
		current := matchesDocumentFilter(summary, filter)
		if logic == "and" && !current {
			return false
		}
		if logic == "or" && current {
			return true
		}
	}
	return matched
}

func matchesDocumentFilter(summary FileDocumentSummary, filter DocumentFilter) bool {
	switch filter.Field {
	case "documentId":
		return compareString(summary.DocumentID, filter)
	case "displayName":
		return compareString(summary.DisplayName, filter)
	case "relativePath":
		return compareString(summary.RelativePath, filter)
	case "extension":
		return compareString(summary.Extension, filter)
	case "mimeType":
		return compareString(summary.MimeType, filter)
	case "status":
		return compareString(string(summary.Status), filter)
	case "sizeBytes":
		return compareNumber(float64(summary.SizeBytes), filter)
	case "effectiveRevisionCreatedAt":
		return compareTime(summary.EffectiveRevisionCreatedAt, filter)
	default:
		return false
	}
}

func compareString(actual string, filter DocumentFilter) bool {
	actual = strings.ToLower(actual)
	values := stringValues(filter.Value)
	switch filter.Operator {
	case "eq":
		return actual == strings.ToLower(values[0])
	case "contains":
		return strings.Contains(actual, strings.ToLower(values[0]))
	case "in":
		for _, value := range values {
			if actual == strings.ToLower(value) {
				return true
			}
		}
	}
	return false
}

func compareNumber(actual float64, filter DocumentFilter) bool {
	values := anySlice(filter.Value)
	if filter.Operator != "between" {
		values = []any{filter.Value}
	}
	left, _ := numberValue(values[0])
	switch filter.Operator {
	case "eq":
		return actual == left
	case "gt":
		return actual > left
	case "gte":
		return actual >= left
	case "lt":
		return actual < left
	case "lte":
		return actual <= left
	case "between":
		right, _ := numberValue(values[1])
		return actual >= left && actual <= right
	default:
		return false
	}
}

func compareTime(actual time.Time, filter DocumentFilter) bool {
	values := anySlice(filter.Value)
	if filter.Operator != "between" {
		values = []any{filter.Value}
	}
	left, _ := timeValue(values[0])
	switch filter.Operator {
	case "eq":
		return actual.Equal(left)
	case "before":
		return actual.Before(left)
	case "after":
		return actual.After(left)
	case "between":
		right, _ := timeValue(values[1])
		return !actual.Before(left) && !actual.After(right)
	default:
		return false
	}
}

func sortDocumentSummaries(documents []FileDocumentSummary, orders []DocumentSort) {
	orders = normalizedDocumentSort(orders)
	sort.SliceStable(documents, func(leftIndex, rightIndex int) bool {
		return compareDocumentSummaries(documents[leftIndex], documents[rightIndex], orders) < 0
	})
}

func normalizedDocumentSort(orders []DocumentSort) []DocumentSort {
	if len(orders) == 0 {
		return []DocumentSort{{Field: "relativePath", Direction: "asc"}}
	}
	return orders
}

func compareDocumentSummaries(
	left, right FileDocumentSummary,
	orders []DocumentSort,
) int {
	for _, order := range orders {
		comparison := compareDocumentField(left, right, order.Field)
		if order.Direction == "desc" {
			comparison = -comparison
		}
		if comparison != 0 {
			return comparison
		}
	}
	return strings.Compare(left.DocumentID, right.DocumentID)
}

func cloneDocumentSummaries(source []FileDocumentSummary) []FileDocumentSummary {
	result := make([]FileDocumentSummary, len(source))
	for index := range source {
		result[index] = source[index]
		if source[index].FormalVersion != nil {
			value := *source[index].FormalVersion
			result[index].FormalVersion = &value
		}
	}
	return result
}

func compareDocumentField(left, right FileDocumentSummary, field string) int {
	switch field {
	case "documentId":
		return strings.Compare(left.DocumentID, right.DocumentID)
	case "displayName":
		return strings.Compare(strings.ToLower(left.DisplayName), strings.ToLower(right.DisplayName))
	case "relativePath":
		return strings.Compare(strings.ToLower(left.RelativePath), strings.ToLower(right.RelativePath))
	case "extension":
		return strings.Compare(left.Extension, right.Extension)
	case "mimeType":
		return strings.Compare(strings.ToLower(left.MimeType), strings.ToLower(right.MimeType))
	case "sizeBytes":
		return compareOrdered(left.SizeBytes, right.SizeBytes)
	case "effectiveRevisionCreatedAt":
		return compareOrdered(left.EffectiveRevisionCreatedAt.UnixNano(), right.EffectiveRevisionCreatedAt.UnixNano())
	case "formalVersion":
		return compareOrdered(optionalVersion(left.FormalVersion), optionalVersion(right.FormalVersion))
	case "status":
		return strings.Compare(string(left.Status), string(right.Status))
	default:
		return 0
	}
}

func compareOrdered[T ~int64 | ~uint64](left, right T) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func optionalVersion(value *uint64) uint64 {
	if value == nil {
		return 0
	}
	return *value
}

func documentQueryFingerprint(request DocumentQueryRequest) (string, error) {
	canonical := struct {
		Logic   string           `json:"logic"`
		Filters []DocumentFilter `json:"filters"`
		Sort    []DocumentSort   `json:"sort"`
		Limit   int              `json:"limit"`
	}{request.Logic, request.Filters, request.Sort, request.Limit}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func encodeDocumentCursor(cursor documentCursor) (string, error) {
	cursor.Checksum = documentCursorChecksum(cursor)
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeDocumentCursor(value string) (documentCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return documentCursor{}, err
	}
	var cursor documentCursor
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return documentCursor{}, ErrDocumentQueryInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) ||
		cursor.After.DocumentID == "" ||
		cursor.Checksum != documentCursorChecksum(cursor) {
		return documentCursor{}, ErrDocumentQueryInvalid
	}
	return cursor, nil
}

func documentCursorChecksum(cursor documentCursor) string {
	payload := struct {
		Contract         string              `json:"contract"`
		TopologyRevision uint64              `json:"topologyRevision"`
		Fingerprint      string              `json:"fingerprint"`
		After            FileDocumentSummary `json:"after"`
	}{
		Contract:         "file-document-query.v1",
		TopologyRevision: cursor.TopologyRevision,
		Fingerprint:      cursor.Fingerprint,
		After:            cursor.After,
	}
	raw, _ := json.Marshal(payload)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func stringValues(value any) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	values := anySlice(value)
	result := make([]string, 0, len(values))
	for _, item := range values {
		text, ok := item.(string)
		if !ok {
			return nil
		}
		result = append(result, text)
	}
	return result
}

func anySlice(value any) []any {
	if values, ok := value.([]any); ok {
		return values
	}
	if values, ok := value.([]string); ok {
		result := make([]any, len(values))
		for index := range values {
			result[index] = values[index]
		}
		return result
	}
	return nil
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func timeValue(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	return parsed, err == nil
}
