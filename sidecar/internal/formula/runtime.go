// Package formula contains the CEL integration point. WP-01 only verifies that
// the pinned CEL runtime can initialize; the authoritative language and
// evaluator are introduced by the formula work package.
package formula

import (
	"fmt"

	"github.com/google/cel-go/cel"
)

func ValidateRuntime() error {
	if _, err := cel.NewEnv(); err != nil {
		return fmt.Errorf("initialize CEL runtime: %w", err)
	}
	return nil
}
