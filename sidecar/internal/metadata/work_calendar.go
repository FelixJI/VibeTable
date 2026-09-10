package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/pocketbase/pocketbase/core"
)

const WorkCalendarID = "work-calendar"
const MaxWorkCalendarOverrides = 3660

type WorkCalendarOverride struct {
	Date string `json:"date"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}
type WorkCalendarResult struct {
	Overrides []WorkCalendarOverride `json:"overrides"`
	Revision  string                 `json:"revision"`
}
type WorkCalendarCommit struct {
	Overrides        []WorkCalendarOverride `json:"overrides"`
	ExpectedRevision string                 `json:"expectedRevision"`
	IdempotencyKey   string                 `json:"idempotencyKey"`
}
type WorkCalendarReceipt struct {
	ReceiptTrace
	WorkCalendarResult
}
type calendarPayload struct {
	Scope      string `json:"scope"`
	Key        string `json:"key"`
	Generation string `json:"generation"`
	Value      struct {
		Overrides []WorkCalendarOverride `json:"overrides"`
	} `json:"value"`
}

func calendarError(code, path string) error {
	return &Error{Code: "settings.calendar." + code, Path: path, Message: "Shared work calendar request could not be completed"}
}

func ValidateWorkCalendar(overrides []WorkCalendarOverride) ([]WorkCalendarOverride, error) {
	if overrides == nil || len(overrides) > MaxWorkCalendarOverrides {
		return nil, calendarError("invalid", "overrides")
	}
	result := make([]WorkCalendarOverride, 0, len(overrides))
	seen := make(map[string]bool, len(overrides))
	for i, value := range overrides {
		path := fmt.Sprintf("overrides[%d]", i)
		parsed, err := time.Parse("2006-01-02", value.Date)
		if err != nil || parsed.Year() < 100 || parsed.Year() > 9999 || parsed.Format("2006-01-02") != value.Date || seen[value.Date] {
			return nil, calendarError("invalid", path+".date")
		}
		if value.Kind != "holiday" && value.Kind != "workday" {
			return nil, calendarError("invalid", path+".kind")
		}
		value.Name = strings.TrimFunc(value.Name, func(r rune) bool {
			return r == 0xFEFF || r == 0x0009 || r == 0x000A || r == 0x000B || r == 0x000C || r == 0x000D || r == 0x2028 || r == 0x2029 || unicode.Is(unicode.Zs, r)
		})
		if !utf8.ValidString(value.Name) || len(utf16.Encode([]rune(value.Name))) > 40 {
			return nil, calendarError("invalid", path+".name")
		}
		seen[value.Date] = true
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Date < result[j].Date })
	return result, nil
}

func readWorkCalendar(app core.App) (WorkCalendarResult, error) {
	collection, err := resolveCollection(app, NamespaceSharedSettings)
	if err != nil {
		return WorkCalendarResult{}, err
	}
	record, err := findRecord(app, collection, WorkCalendarID)
	if err != nil {
		return WorkCalendarResult{}, err
	}
	if record == nil {
		return WorkCalendarResult{Overrides: []WorkCalendarOverride{}}, nil
	}
	item, err := itemFromRecord(NamespaceSharedSettings, record)
	if err != nil {
		return WorkCalendarResult{}, err
	}
	var payload calendarPayload
	decoder := json.NewDecoder(bytes.NewReader(item.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil || payload.Scope != "workspace" || payload.Key != WorkCalendarID || payload.Generation == "" {
		return WorkCalendarResult{}, calendarError("corrupt", "")
	}
	values, err := ValidateWorkCalendar(payload.Value.Overrides)
	if err != nil {
		return WorkCalendarResult{}, calendarError("corrupt", "")
	}
	return WorkCalendarResult{Overrides: values, Revision: item.Revision}, nil
}

func (service *Service) ReadWorkCalendar(ctx context.Context) (WorkCalendarResult, error) {
	if err := ctx.Err(); err != nil {
		return WorkCalendarResult{}, err
	}
	result, err := readWorkCalendar(service.app)
	if err != nil {
		return WorkCalendarResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return WorkCalendarResult{}, err
	}
	return result, nil
}

func (service *Service) CommitWorkCalendar(ctx context.Context, request WorkCalendarCommit) (WorkCalendarReceipt, error) {
	if err := validateExpectedRevision(request.ExpectedRevision, "expectedRevision", true); err != nil {
		return WorkCalendarReceipt{}, err
	}
	values, err := ValidateWorkCalendar(request.Overrides)
	if err != nil {
		return WorkCalendarReceipt{}, err
	}
	if !idempotencyPattern.MatchString(request.IdempotencyKey) {
		return WorkCalendarReceipt{}, calendarError("invalid", "idempotencyKey")
	}
	request.Overrides = values
	requestHash, err := hashValue(struct {
		Operation string             `json:"operation"`
		Request   WorkCalendarCommit `json:"request"`
	}{"work-calendar.commit", request})
	if err != nil {
		return WorkCalendarReceipt{}, err
	}
	return executeIdempotent(service, ctx, request.IdempotencyKey, requestHash,
		func(tx core.App) (WorkCalendarReceipt, []metadataChange, error) {
			current, err := readWorkCalendar(tx)
			if err != nil {
				return WorkCalendarReceipt{}, nil, err
			}
			if current.Revision != request.ExpectedRevision {
				return WorkCalendarReceipt{}, nil, calendarError("revision_conflict", "expectedRevision")
			}
			payload := calendarPayload{Scope: "workspace", Key: WorkCalendarID, Generation: service.newID("calendar")}
			payload.Value.Overrides = values
			raw, err := json.Marshal(payload)
			if err != nil {
				return WorkCalendarReceipt{}, nil, err
			}
			item, change, err := service.upsert(tx, NamespaceSharedSettings, ItemMutation{LogicalID: WorkCalendarID, Payload: raw, ExpectedRevision: request.ExpectedRevision}, "", "")
			if err != nil {
				return WorkCalendarReceipt{}, nil, err
			}
			return WorkCalendarReceipt{ReceiptTrace: ReceiptTrace{Status: StatusApplied}, WorkCalendarResult: WorkCalendarResult{Overrides: values, Revision: item.Revision}}, []metadataChange{change}, nil
		},
		func(receipt *WorkCalendarReceipt, changeSet string, events []string) {
			receipt.ChangeSetID = changeSet
			receipt.EmittedEvents = events
		},
		func(receipt *WorkCalendarReceipt) { receipt.Status = StatusReplayed },
	)
}
