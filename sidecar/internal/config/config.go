// Package config parses only VibeTable-owned launch options. The session
// credential deliberately has no command-line representation.
package config

import (
	"errors"
	"flag"
	"io"

	"github.com/vibetable/vibetable/sidecar/internal/auth"
)

const (
	SessionSecretEnv = "VIBETABLE_SIDECAR_SESSION_SECRET"
	DataDirEnv       = "VIBETABLE_SIDECAR_DATA_DIR"
)

type Config struct {
	DataDir       string
	Dev           bool
	BuildInfoOnly bool
	Session       auth.Secret
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
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if flags.NArg() != 0 {
		return Config{}, errors.New("unexpected positional arguments")
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
	return result, nil
}
