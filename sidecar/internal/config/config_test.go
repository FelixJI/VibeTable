package config

import (
	"strings"
	"testing"
)

func TestParseLoadsSecretOnlyFromEnvironment(t *testing.T) {
	env := map[string]string{
		SessionSecretEnv: strings.Repeat("01", 32),
		DataDirEnv:       "from-env",
	}
	cfg, err := Parse([]string{"--data-dir", "from-flag", "--dev"}, func(key string) string {
		return env[key]
	})
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	if cfg.DataDir != "from-flag" || !cfg.Dev || cfg.Session.IsZero() {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestParseRequiresSessionForServerMode(t *testing.T) {
	if _, err := Parse(nil, func(string) string { return "" }); err == nil {
		t.Fatal("Parse() unexpectedly accepted missing session secret")
	}
}

func TestBuildInfoDoesNotRequireSession(t *testing.T) {
	cfg, err := Parse([]string{"--build-info"}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Parse(): %v", err)
	}
	if !cfg.BuildInfoOnly {
		t.Fatal("BuildInfoOnly = false")
	}
}

func TestParseDoesNotDefineSecretFlag(t *testing.T) {
	_, err := Parse(
		[]string{"--session-secret", strings.Repeat("00", 32)},
		func(string) string { return "" },
	)
	if err == nil {
		t.Fatal("command-line secret was unexpectedly accepted")
	}
}
