package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibetable/vibetable/sidecar/internal/backup"
)

func TestDecodeBackupBodyIsStrictAndBounded(t *testing.T) {
	var input backupRequest
	if err := decodeBackupBody(
		strings.NewReader(`{"name":"manual_001.zip"}`),
		&input,
	); err != nil || input.Name != "manual_001.zip" {
		t.Fatalf("input = %#v, err=%v", input, err)
	}
	for name, body := range map[string]string{
		"empty":    ``,
		"unknown":  `{"name":"manual.zip","path":"c:\\data"}`,
		"trailing": `{"name":"manual.zip"} {}`,
		"oversized": `"` + strings.Repeat(
			"x", maxBackupRequestBytes,
		) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			var target backupRequest
			err := decodeBackupBody(
				strings.NewReader(body), &target,
			)
			var productErr *backup.Error
			if !errors.As(err, &productErr) ||
				productErr.Code != "backup.request.invalid" {
				t.Fatalf("error = %#v", err)
			}
		})
	}
}
