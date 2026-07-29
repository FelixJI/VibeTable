package backupreceipt

import (
	"strings"
	"testing"
)

func TestReceiptRequiresAuthenticSigningKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	otherKey := []byte("abcdef0123456789abcdef0123456789")
	digest := strings.Repeat("a", 64)
	receipt, err := Encode("snapshot.zip", digest, digest, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decode(receipt, key); err != nil {
		t.Fatalf("decode authentic receipt: %v", err)
	}
	if _, err := decode(receipt, otherKey); err == nil {
		t.Fatal("receipt signed by another key was accepted")
	}
}
