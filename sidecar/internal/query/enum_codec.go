package query

import "encoding/json"

func decodeEnumValue(descriptor *EnumDescriptor, value any) any {
	if descriptor == nil || value == nil {
		return value
	}
	decodeOne := func(storage string) any {
		for _, option := range descriptor.Options {
			if storage == option.StorageValue ||
				option.LegacyStorageValue != "" &&
					storage == option.LegacyStorageValue {
				return option.Value
			}
		}
		return storage
	}
	if !descriptor.Multiple {
		switch typed := value.(type) {
		case string:
			return decodeOne(typed)
		case []byte:
			return decodeOne(string(typed))
		default:
			return value
		}
	}
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return []any{}
		}
	case []byte:
		if len(typed) == 0 {
			return []any{}
		}
	}
	values, ok := asSlice(value)
	if !ok {
		return value
	}
	result := make([]any, len(values))
	for index, item := range values {
		if storage, isString := item.(string); isString {
			result[index] = decodeOne(storage)
		} else {
			result[index] = item
		}
	}
	return result
}

func enumStorageCandidates(
	descriptor *EnumDescriptor,
	value any,
) ([]string, bool) {
	if descriptor == nil {
		return nil, false
	}
	for _, option := range descriptor.Options {
		if !queryJSONValuesEqual(option.Value, value) {
			continue
		}
		result := []string{option.StorageValue}
		if option.LegacyStorageValue != "" &&
			option.LegacyStorageValue != option.StorageValue {
			result = append(result, option.LegacyStorageValue)
		}
		return result, true
	}
	return nil, false
}

func queryJSONValuesEqual(left, right any) bool {
	leftRaw, leftErr := json.Marshal(left)
	rightRaw, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftRaw) == string(rightRaw)
}
