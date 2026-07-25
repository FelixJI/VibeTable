package schema

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

const typedEnumStoragePrefix = "__vibetable_enum_v1__"

// EnumStorageOption binds one provider-neutral enum value to its stable
// PocketBase Select storage value. Strings remain byte-for-byte unchanged for
// old-database compatibility; numbers and booleans use a reserved typed
// encoding so values such as 1 and "1" cannot collapse.
type EnumStorageOption struct {
	Value              any
	StorageValue       string
	LegacyStorageValue string
}

func EnumStorageOptions(field FieldDefinition) ([]EnumStorageOption, error) {
	constraint := enumConstraint(field)
	if constraint == nil {
		return nil, fmt.Errorf("enum constraint is required")
	}
	options := make([]EnumStorageOption, 0, len(constraint.Options))
	preferred := make(map[string]int, len(constraint.Options))
	for index, option := range constraint.Options {
		storage, legacy, err := encodeEnumOption(option.Value)
		if err != nil {
			return nil, fmt.Errorf("option %d: %w", index, err)
		}
		if previous, exists := preferred[storage]; exists {
			return nil, fmt.Errorf(
				"options %d and %d have the same storage value",
				previous,
				index,
			)
		}
		preferred[storage] = index
		options = append(options, EnumStorageOption{
			Value: option.Value, StorageValue: storage, LegacyStorageValue: legacy,
		})
	}
	// A legacy fmt.Sprint value is only safe as a read/write fallback when it
	// cannot be confused with any current preferred storage value.
	for index := range options {
		legacy := options[index].LegacyStorageValue
		if legacy == "" {
			continue
		}
		if owner, collision := preferred[legacy]; collision && owner != index {
			options[index].LegacyStorageValue = ""
			continue
		}
		for otherIndex, other := range options {
			if otherIndex != index && other.LegacyStorageValue == legacy {
				options[index].LegacyStorageValue = ""
				break
			}
		}
	}
	return options, nil
}

func EnumStorageValues(field FieldDefinition) ([]string, error) {
	options, err := EnumStorageOptions(field)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(options))
	for index, option := range options {
		values[index] = option.StorageValue
	}
	return values, nil
}

// EncodeSelectValueForStorage converts a validated product value to the
// PocketBase Select representation. allowedStorageValues enables transparent
// writes to typed schemas compiled by pre-codec versions, whose PB values used
// fmt.Sprint; nil selects the canonical v1 encoding.
func EncodeSelectValueForStorage(
	field FieldDefinition,
	value any,
	allowedStorageValues []string,
) (any, error) {
	options, err := EnumStorageOptions(field)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowedStorageValues))
	for _, item := range allowedStorageValues {
		allowed[item] = struct{}{}
	}
	encodeOne := func(candidate any) (string, error) {
		for _, option := range options {
			if !jsonValuesEqual(option.Value, candidate) {
				continue
			}
			if len(allowed) == 0 {
				return option.StorageValue, nil
			}
			if _, ok := allowed[option.StorageValue]; ok {
				return option.StorageValue, nil
			}
			if option.LegacyStorageValue != "" {
				if _, ok := allowed[option.LegacyStorageValue]; ok {
					return option.LegacyStorageValue, nil
				}
			}
			return "", fmt.Errorf("enum option is not present in the storage schema")
		}
		return "", fmt.Errorf("select value is not an allowed option")
	}
	if effectiveDataType(field) != DataTypeMultiSelect {
		return encodeOne(value)
	}
	values, ok := arrayValues(value)
	if !ok {
		return nil, fmt.Errorf("multiSelect value must be an array")
	}
	encoded := make([]string, len(values))
	for index, candidate := range values {
		encoded[index], err = encodeOne(candidate)
		if err != nil {
			return nil, fmt.Errorf("value %d: %w", index, err)
		}
	}
	return encoded, nil
}

// DecodeSelectValueFromStorage restores normalized product enum values. It
// only decodes values listed by the field's enum definition; arbitrary user
// strings that resemble the reserved prefix are never interpreted by syntax.
func DecodeSelectValueFromStorage(field FieldDefinition, value any) any {
	options, err := EnumStorageOptions(field)
	if err != nil {
		return value
	}
	decodeOne := func(storage string) any {
		for _, option := range options {
			if storage == option.StorageValue ||
				option.LegacyStorageValue != "" && storage == option.LegacyStorageValue {
				return option.Value
			}
		}
		return storage
	}
	if effectiveDataType(field) != DataTypeMultiSelect {
		switch typed := value.(type) {
		case string:
			return decodeOne(typed)
		case []byte:
			return decodeOne(string(typed))
		default:
			return value
		}
	}
	var values []any
	switch typed := value.(type) {
	case string:
		if json.Unmarshal([]byte(typed), &values) != nil {
			return value
		}
	case []byte:
		if json.Unmarshal(typed, &values) != nil {
			return value
		}
	default:
		var ok bool
		values, ok = arrayValues(value)
		if !ok {
			return value
		}
	}
	decoded := make([]any, len(values))
	for index, item := range values {
		switch typed := item.(type) {
		case string:
			decoded[index] = decodeOne(typed)
		case []byte:
			decoded[index] = decodeOne(string(typed))
		default:
			decoded[index] = item
		}
	}
	return decoded
}

func ProductValuesEqual(left, right any) bool {
	return jsonValuesEqual(left, right)
}

func encodeEnumOption(value any) (storage string, legacy string, err error) {
	switch typed := value.(type) {
	case string:
		return typed, "", nil
	case bool:
		if typed {
			return typedEnumStoragePrefix + "b:1", "true", nil
		}
		return typedEnumStoragePrefix + "b:0", "false", nil
	default:
		number, _, numberErr := numericValue(value)
		if numberErr != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return "", "", fmt.Errorf("enum value must be a string, number, or boolean")
		}
		canonical := strconv.FormatFloat(number, 'g', -1, 64)
		encoded := base64.RawURLEncoding.EncodeToString([]byte(canonical))
		return typedEnumStoragePrefix + "n:" + encoded, fmt.Sprint(value), nil
	}
}
