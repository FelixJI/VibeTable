package backupreceipt

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

var backupNamePattern = regexp.MustCompile(
	`^[a-z0-9][a-z0-9_-]{0,62}\.zip$`,
)

type payload struct {
	Name           string `json:"name"`
	SHA256         string `json:"sha256"`
	RevisionDigest string `json:"revisionDigest"`
	Checksum       string `json:"checksum"`
}

func SnapshotDigest(app core.App) (string, error) {
	records, err := app.FindRecordsByFilter(
		"vibetable_tables", "", "table_id", 0, 0,
	)
	if err != nil {
		return "", fmt.Errorf("load workspace revisions: %w", err)
	}
	hash := sha256.New()
	for _, record := range records {
		_, _ = io.WriteString(
			hash,
			record.GetString("table_id")+"\x00"+
				record.GetString("schema_revision")+"\x00"+
				record.GetString("data_revision")+"\n",
		)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func Encode(
	name string,
	archiveSHA256 string,
	revisionDigest string,
	signingKey []byte,
) (string, error) {
	value := payload{
		Name: name, SHA256: archiveSHA256, RevisionDigest: revisionDigest,
	}
	if !validName(value.Name) ||
		!validDigest(value.SHA256) ||
		!validDigest(value.RevisionDigest) ||
		len(signingKey) < 32 {
		return "", errors.New("backup receipt inputs are invalid")
	}
	value.Checksum = checksum(value, signingKey)
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return "vbr1." + base64.RawURLEncoding.EncodeToString(raw), nil
}

// Verify proves that a backup archive still exists with the recorded digest
// and no table schema/data revision has changed since it was made.
func Verify(
	ctx context.Context,
	app core.App,
	receipt string,
	signingKey []byte,
) error {
	value, err := decode(receipt, signingKey)
	if err != nil {
		return err
	}
	revisionDigest, err := SnapshotDigest(app)
	if err != nil {
		return err
	}
	if revisionDigest != value.RevisionDigest {
		return errors.New("backup receipt is stale")
	}
	fsys, err := app.NewBackupsFilesystem()
	if err != nil {
		return fmt.Errorf("open backup storage: %w", err)
	}
	defer fsys.Close()
	fsys.SetContext(ctx)
	reader, err := fsys.GetReader(value.Name)
	if err != nil {
		return fmt.Errorf("open backup archive: %w", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if hex.EncodeToString(hash.Sum(nil)) != value.SHA256 {
		return errors.New("backup archive digest does not match receipt")
	}
	return nil
}

func decode(receipt string, signingKey []byte) (payload, error) {
	if !strings.HasPrefix(receipt, "vbr1.") ||
		len(receipt) > 2048 ||
		len(signingKey) < 32 {
		return payload{}, errors.New("backup receipt format is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(
		strings.TrimPrefix(receipt, "vbr1."),
	)
	if err != nil {
		return payload{}, errors.New("backup receipt encoding is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value payload
	if err := decoder.Decode(&value); err != nil {
		return payload{}, errors.New("backup receipt payload is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return payload{}, errors.New("backup receipt payload has trailing data")
	}
	if !validName(value.Name) ||
		!validDigest(value.SHA256) ||
		!validDigest(value.RevisionDigest) ||
		!validDigest(value.Checksum) ||
		!hmac.Equal(
			[]byte(value.Checksum),
			[]byte(checksum(value, signingKey)),
		) {
		return payload{}, errors.New("backup receipt verification failed")
	}
	return value, nil
}

func checksum(value payload, signingKey []byte) string {
	mac := hmac.New(sha256.New, signingKey)
	_, _ = io.WriteString(
		mac,
		"vibetable-backup-receipt-v1\x00"+
			value.Name+"\x00"+value.SHA256+"\x00"+value.RevisionDigest,
	)
	return hex.EncodeToString(mac.Sum(nil))
}

func validName(value string) bool {
	return backupNamePattern.MatchString(value) && path.Base(value) == value
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
