package schemaexecution

import (
	"encoding/json"
	"math"

	"github.com/vibetable/vibetable/sidecar/internal/schemaerror"
)

const maxSafeRevision = int64(1<<53 - 1)

// ParseStoredRevision validates persisted revision metadata without coercion.
func ParseStoredRevision(
	value any,
	code string,
	path string,
) (int64, error) {
	var revision int64
	switch number := value.(type) {
	case float64:
		if number < 0 || number > float64(maxSafeRevision) ||
			math.Trunc(number) != number {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case float32:
		converted := float64(number)
		if converted < 0 || converted > float64(maxSafeRevision) ||
			math.Trunc(converted) != converted {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case int:
		revision = int64(number)
	case int64:
		revision = number
	case int32:
		revision = int64(number)
	case uint:
		if uint64(number) > uint64(maxSafeRevision) {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case uint64:
		if number > uint64(maxSafeRevision) {
			return 0, invalidStoredRevision(code, path)
		}
		revision = int64(number)
	case uint32:
		revision = int64(number)
	case json.Number:
		parsed, parseErr := number.Int64()
		if parseErr != nil {
			return 0, invalidStoredRevision(code, path)
		}
		revision = parsed
	default:
		return 0, invalidStoredRevision(code, path)
	}
	if revision < 0 || revision > maxSafeRevision {
		return 0, invalidStoredRevision(code, path)
	}
	return revision, nil
}

func invalidStoredRevision(code, path string) *schemaerror.ProductError {
	return &schemaerror.ProductError{
		Code: code, Path: path,
		Message: "stored revision must be a present non-negative safe integer",
	}
}
