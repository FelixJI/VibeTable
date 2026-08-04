package backupreceipt

import (
	"strings"
	"testing"
)

func TestReceiptRoundTripUsesStrictIntegrityMetadata(t *testing.T) {
	digest := strings.Repeat("a", 64)
	receipt, err := Encode("snapshot.zip", digest, digest)
	if err != nil {
		t.Fatal(err)
	}
	value, err := decode(receipt)
	if err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if value.Name != "snapshot.zip" || value.SHA256 != digest || value.RevisionDigest != digest {
		t.Fatalf("unexpected receipt: %#v", value)
	}
}

func TestReceiptRejectsMalformedIntegrityMetadata(t *testing.T) {
	if _, err := Encode("../snapshot.zip", strings.Repeat("a", 64), strings.Repeat("b", 64)); err == nil {
		t.Fatal("unsafe backup name was accepted")
	}
	if _, err := decode("vbr1.not-base64+"); err == nil {
		t.Fatal("malformed receipt was accepted")
	}
}
