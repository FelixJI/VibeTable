package formula

import "testing"

func TestPinnedCELRuntimeInitializes(t *testing.T) {
	if err := ValidateRuntime(); err != nil {
		t.Fatal(err)
	}
}
