package app

import (
	"strings"
	"testing"
)

func TestAdminBootstrapKeepsAuthDataOutOfScriptSource(t *testing.T) {
	authValue := []byte(
		`{"token":"</script><script>alert(1)</script>","record":{"email":"\" onload=\"alert(1)"}}`,
	)

	page, err := renderAdminBootstrap(authValue)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, string(authValue)) ||
		strings.Contains(page, "</script><script>") {
		t.Fatalf("auth data entered executable markup: %s", page)
	}
	if strings.Count(page, "<script>") != 1 ||
		!strings.Contains(page, "bootstrap.dataset.auth") {
		t.Fatalf("bootstrap script contract changed: %s", page)
	}
	if !strings.Contains(page, "data-auth=") ||
		!strings.Contains(page, "&lt;/script&gt;") {
		t.Fatalf("auth data was not HTML-attribute escaped: %s", page)
	}
}
