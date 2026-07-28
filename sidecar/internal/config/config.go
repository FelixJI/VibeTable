// Package config parses only VibeTable-owned launch options. The session
// credential deliberately has no command-line representation.
package config

import (
	"errors"
	"flag"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/vibetable/vibetable/sidecar/internal/auth"
)

const (
	SessionSecretEnv = "VIBETABLE_SIDECAR_SESSION_SECRET"
	DataDirEnv       = "VIBETABLE_SIDECAR_DATA_DIR"
	WorkspaceIDEnv   = "VIBETABLE_WORKSPACE_ID"
	SessionEpochEnv  = "VIBETABLE_WORKSPACE_SESSION_EPOCH"
	FenceEpochEnv    = "VIBETABLE_WORKSPACE_FENCE_EPOCH"
	ClaimIDEnv       = "VIBETABLE_WORKSPACE_CLAIM_ID"
	ReplicaRootEnv   = "VIBETABLE_REPLICA_ROOT"
	ActivityRootEnv  = "VIBETABLE_ACTIVITY_ROOT"
)

type WorkspaceIdentity struct {
	WorkspaceID  string
	SessionEpoch uint64
	FenceEpoch   uint64
	ClaimID      string
}

type Config struct {
	DataDir                       string
	Dev                           bool
	BuildInfoOnly                 bool
	InitializeWorkspaceRepository bool
	UnlockWorkspaceRepository     bool
	RotateWorkspaceRepository     bool
	InitializeWorkspaceReplica    bool
	RecoverWorkspaceReplica       bool
	VerifyWorkspaceReplica        bool
	Session                       auth.Secret
	WorkspaceV2                   *WorkspaceIdentity
	ReplicaRoot                   string
	ActivityRoot                  string
}

func Parse(args []string, getenv func(string) string) (Config, error) {
	if getenv == nil {
		return Config{}, errors.New("environment reader is required")
	}

	flags := flag.NewFlagSet("vibetable-pb", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var result Config
	flags.StringVar(&result.DataDir, "data-dir", getenv(DataDirEnv), "PocketBase data directory")
	flags.BoolVar(&result.Dev, "dev", false, "enable development diagnostics")
	flags.BoolVar(&result.BuildInfoOnly, "build-info", false, "print build metadata and exit")
	flags.BoolVar(
		&result.InitializeWorkspaceRepository,
		"initialize-workspace-repository",
		false,
		"initialize the bound workspace repository and exit",
	)
	flags.BoolVar(
		&result.UnlockWorkspaceRepository,
		"unlock-workspace-repository",
		false,
		"restore a protected repository key from trusted stdin and exit",
	)
	flags.BoolVar(
		&result.RotateWorkspaceRepository,
		"rotate-workspace-repository",
		false,
		"rotate a protected repository key and exit",
	)
	flags.BoolVar(
		&result.InitializeWorkspaceReplica,
		"initialize-workspace-replica",
		false,
		"initialize and verify the bound workspace replica and exit",
	)
	flags.BoolVar(
		&result.RecoverWorkspaceReplica,
		"recover-workspace-replica",
		false,
		"recover a new activity root from the bound workspace replica and exit",
	)
	flags.BoolVar(
		&result.VerifyWorkspaceReplica,
		"verify-workspace-replica",
		false,
		"read-only verify the bound workspace replica and exit",
	)
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, errors.New("unexpected positional arguments")
	}
	modeCount := 0
	for _, enabled := range []bool{
		result.BuildInfoOnly,
		result.InitializeWorkspaceRepository,
		result.UnlockWorkspaceRepository,
		result.RotateWorkspaceRepository,
		result.InitializeWorkspaceReplica,
		result.RecoverWorkspaceReplica,
		result.VerifyWorkspaceReplica,
	} {
		if enabled {
			modeCount++
		}
	}
	if modeCount > 1 {
		return Config{}, errors.New("sidecar one-shot modes are mutually exclusive")
	}
	if result.BuildInfoOnly {
		return result, nil
	}
	if result.DataDir == "" {
		result.DataDir = "./pb_data"
	}

	session, err := auth.Parse(getenv(SessionSecretEnv))
	if err != nil {
		return Config{}, err
	}
	result.Session = session
	workspace, err := parseWorkspaceIdentity(getenv)
	if err != nil {
		return Config{}, err
	}
	result.WorkspaceV2 = workspace
	replicaRoot := strings.TrimSpace(getenv(ReplicaRootEnv))
	if replicaRoot != "" {
		if workspace == nil {
			return Config{}, errors.New(
				"replica configuration requires workspace v2 identity",
			)
		}
		result.ReplicaRoot = replicaRoot
	}
	repositoryOneShot := result.InitializeWorkspaceRepository ||
		result.UnlockWorkspaceRepository ||
		result.RotateWorkspaceRepository
	replicaOneShot := result.InitializeWorkspaceReplica ||
		result.RecoverWorkspaceReplica ||
		result.VerifyWorkspaceReplica
	if repositoryOneShot {
		if strings.TrimSpace(getenv(DataDirEnv)) == "" ||
			result.WorkspaceV2 == nil {
			return Config{}, errors.New(
				"repository onboarding requires env dataDir and workspace identity",
			)
		}
		for _, argument := range args {
			if argument == "--data-dir" ||
				strings.HasPrefix(argument, "--data-dir=") {
				return Config{}, errors.New(
					"repository onboarding dataDir must come from the environment",
				)
			}
		}
	}
	if replicaOneShot {
		if strings.TrimSpace(getenv(DataDirEnv)) == "" ||
			result.WorkspaceV2 == nil {
			return Config{}, errors.New(
				"replica one-shot requires env dataDir and workspace identity",
			)
		}
		for _, argument := range args {
			if argument == "--data-dir" ||
				strings.HasPrefix(argument, "--data-dir=") {
				return Config{}, errors.New(
					"replica one-shot dataDir must come from the environment",
				)
			}
		}
		if result.ReplicaRoot == "" {
			return Config{}, errors.New(
				"replica one-shot requires env replica root",
			)
		}
		for _, argument := range args {
			if argument == "--replica-root" ||
				strings.HasPrefix(argument, "--replica-root=") {
				return Config{}, errors.New(
					"replica root must come from the environment",
				)
			}
		}
	}
	activityRoot := strings.TrimSpace(getenv(ActivityRootEnv))
	if result.RecoverWorkspaceReplica {
		if activityRoot == "" {
			return Config{}, errors.New(
				"replica recovery requires env activity root",
			)
		}
		result.ActivityRoot = activityRoot
	} else if activityRoot != "" {
		return Config{}, errors.New(
			"activity root is valid only for replica recovery",
		)
	}
	return result, nil
}

func parseWorkspaceIdentity(
	getenv func(string) string,
) (*WorkspaceIdentity, error) {
	values := map[string]string{
		WorkspaceIDEnv:  strings.TrimSpace(getenv(WorkspaceIDEnv)),
		SessionEpochEnv: strings.TrimSpace(getenv(SessionEpochEnv)),
		FenceEpochEnv:   strings.TrimSpace(getenv(FenceEpochEnv)),
		ClaimIDEnv:      strings.TrimSpace(getenv(ClaimIDEnv)),
	}
	present := 0
	for _, value := range values {
		if value != "" {
			present++
		}
	}
	if present == 0 {
		return nil, nil
	}
	if present != len(values) {
		return nil, errors.New("workspace v2 identity must be provided all-or-none")
	}
	workspaceID := values[WorkspaceIDEnv]
	claimID := values[ClaimIDEnv]
	if !canonicalUUID(workspaceID) || !canonicalUUID(claimID) {
		return nil, errors.New("workspace v2 identity UUID is invalid")
	}
	sessionEpoch, err := strconv.ParseUint(values[SessionEpochEnv], 10, 64)
	if err != nil || sessionEpoch == 0 || sessionEpoch > math.MaxInt64 {
		return nil, errors.New("workspace v2 session epoch is invalid")
	}
	fenceEpoch, err := strconv.ParseUint(values[FenceEpochEnv], 10, 64)
	if err != nil || fenceEpoch == 0 || fenceEpoch > math.MaxInt64 {
		return nil, errors.New("workspace v2 fence epoch is invalid")
	}
	return &WorkspaceIdentity{
		WorkspaceID:  workspaceID,
		SessionEpoch: sessionEpoch,
		FenceEpoch:   fenceEpoch,
		ClaimID:      claimID,
	}, nil
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil &&
		value == strings.ToLower(parsed.String())
}
