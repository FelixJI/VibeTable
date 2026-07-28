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

func TestParseWorkspaceV2IdentityIsTrustedAllOrNone(t *testing.T) {
	env := map[string]string{
		SessionSecretEnv: strings.Repeat("01", 32),
		WorkspaceIDEnv:   "11111111-1111-4111-8111-111111111111",
		SessionEpochEnv:  "7",
		FenceEpochEnv:    "3",
		ClaimIDEnv:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	cfg, err := Parse(nil, func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceV2 == nil ||
		cfg.WorkspaceV2.WorkspaceID != env[WorkspaceIDEnv] ||
		cfg.WorkspaceV2.SessionEpoch != 7 ||
		cfg.WorkspaceV2.FenceEpoch != 3 ||
		cfg.WorkspaceV2.ClaimID != env[ClaimIDEnv] {
		t.Fatalf("workspace identity = %#v", cfg.WorkspaceV2)
	}
}

func TestParseWorkspaceV2IdentityNeverDefaultsOrAcceptsPartial(t *testing.T) {
	base := map[string]string{
		SessionSecretEnv: strings.Repeat("01", 32),
	}
	cfg, err := Parse(nil, func(key string) string { return base[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceV2 != nil {
		t.Fatalf("legacy launch invented identity: %#v", cfg.WorkspaceV2)
	}
	base[WorkspaceIDEnv] = "11111111-1111-4111-8111-111111111111"
	if _, err := Parse(nil, func(key string) string { return base[key] }); err == nil {
		t.Fatal("partial workspace identity accepted")
	}
	base[SessionEpochEnv] = "0"
	base[FenceEpochEnv] = "3"
	base[ClaimIDEnv] = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := Parse(nil, func(key string) string { return base[key] }); err == nil {
		t.Fatal("zero session epoch accepted")
	}
	base[SessionEpochEnv] = "7"
	base[ClaimIDEnv] = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	if _, err := Parse(nil, func(key string) string { return base[key] }); err == nil {
		t.Fatal("noncanonical claim id accepted")
	}
}

func TestRepositoryOnboardingRequiresTrustedEnvironmentAndExclusiveMode(t *testing.T) {
	env := map[string]string{
		SessionSecretEnv: strings.Repeat("01", 32),
		DataDirEnv:       "trusted-data",
		WorkspaceIDEnv:   "11111111-1111-4111-8111-111111111111",
		SessionEpochEnv:  "7",
		FenceEpochEnv:    "3",
		ClaimIDEnv:       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}
	getenv := func(key string) string { return env[key] }
	cfg, err := Parse(
		[]string{"--initialize-workspace-repository"},
		getenv,
	)
	if err != nil || !cfg.InitializeWorkspaceRepository {
		t.Fatalf("trusted onboarding rejected: %#v %v", cfg, err)
	}
	rotate, err := Parse(
		[]string{"--rotate-workspace-repository"},
		getenv,
	)
	if err != nil || !rotate.RotateWorkspaceRepository {
		t.Fatalf("trusted rotation rejected: %#v %v", rotate, err)
	}
	if _, err := Parse(
		[]string{
			"--initialize-workspace-repository",
			"--data-dir",
			"from-flag",
		},
		getenv,
	); err == nil {
		t.Fatal("onboarding accepted command-line dataDir")
	}
	if _, err := Parse(
		[]string{
			"--initialize-workspace-repository",
			"--unlock-workspace-repository",
			"--rotate-workspace-repository",
		},
		getenv,
	); err == nil {
		t.Fatal("mutually exclusive onboarding modes accepted")
	}
	delete(env, DataDirEnv)
	if _, err := Parse(
		[]string{"--unlock-workspace-repository"},
		getenv,
	); err == nil {
		t.Fatal("unlock accepted missing trusted dataDir")
	}
}
