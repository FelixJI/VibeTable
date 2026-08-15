package jsonschemavalidation

import (
	"fmt"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

// ValueError keeps the instance location without coupling schema/v2 to the
// legacy runtime field model.
type ValueError struct {
	InstancePath string
}

func (err *ValueError) Error() string { return "value does not satisfy JSON Schema" }

func ValidateDocument(document map[string]any) error {
	_, err := compile(document)
	return err
}

func ValidateValue(document map[string]any, value any) error {
	compiled, err := compile(document)
	if err != nil {
		return err
	}
	if err := compiled.Validate(value); err != nil {
		instancePath := ""
		if validationErr, ok := err.(*jsonschema.ValidationError); ok {
			instancePath = strings.TrimSuffix(
				"/"+strings.Join(validationErr.InstanceLocation, "/"), "/",
			)
		}
		return &ValueError{InstancePath: instancePath}
	}
	return nil
}

func compile(document map[string]any) (*jsonschema.Schema, error) {
	if document == nil {
		return nil, fmt.Errorf("JSON Schema must be an object")
	}
	if err := rejectExternalReferences(document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	const location = "vibetable://field.schema.json"
	if err := compiler.AddResource(location, document); err != nil {
		return nil, err
	}
	return compiler.Compile(location)
}

func rejectExternalReferences(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || (!strings.HasPrefix(reference, "#") && reference != "") {
					return fmt.Errorf("external JSON Schema references are not allowed")
				}
			}
			if err := rejectExternalReferences(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectExternalReferences(child); err != nil {
				return err
			}
		}
	}
	return nil
}
