package snapshotpkg

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

var ErrAgeUnavailable = errors.New("snapshot.age_unavailable")

// AgeCLI deliberately uses the public age command line interface so encrypted
// packages remain interoperable and no unstable internal package becomes part
// of VibeTable's storage contract.
type AgeCLI struct {
	Executable string
}

func (age AgeCLI) Encrypt(ctx context.Context, recipient string, input io.Reader, output io.Writer) error {
	if age.Executable == "" {
		return ErrAgeUnavailable
	}
	command := exec.CommandContext(ctx, age.Executable, "--encrypt", "--recipient", recipient)
	command.Stdin, command.Stdout = input, output
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		return errors.Join(ErrAgeUnavailable, err, errors.New(diagnostic.String()))
	}
	return nil
}

func (age AgeCLI) Decrypt(ctx context.Context, identityFile string, input io.Reader, output io.Writer) error {
	if age.Executable == "" {
		return ErrAgeUnavailable
	}
	command := exec.CommandContext(ctx, age.Executable, "--decrypt", "--identity", identityFile)
	command.Stdin, command.Stdout = input, output
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		return errors.Join(ErrAgeUnavailable, err, errors.New(diagnostic.String()))
	}
	return nil
}
