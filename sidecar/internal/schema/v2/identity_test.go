package v2_test

import (
	"context"
	"sync"
	"testing"

	v2 "github.com/vibetable/vibetable/sidecar/internal/schema/v2"
)

func TestIdentityAllocatorCreatesProviderSafeStableShapes(t *testing.T) {
	t.Parallel()
	allocator := v2.NewIdentityAllocator(nil)
	identity, err := allocator.AllocateField(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.ProviderFieldID) != 15 {
		t.Fatalf("provider field ID length = %d", len(identity.ProviderFieldID))
	}
	presence, err := allocator.AllocatePresence(context.Background(), identity.PhysicalName)
	if err != nil {
		t.Fatal(err)
	}
	if presence.PhysicalName != "__vt_has_"+identity.PhysicalName ||
		presence.ProviderFieldID == identity.ProviderFieldID {
		t.Fatalf("invalid presence identity: %#v", presence)
	}
	optionID, err := allocator.AllocateOption(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(optionID) != 24 || optionID[:4] != "opt_" {
		t.Fatalf("invalid option identity %q", optionID)
	}
}

func TestIdentityAllocatorIsCollisionFreeUnderConcurrentCreate(t *testing.T) {
	t.Parallel()
	const count = 256
	allocator := v2.NewIdentityAllocator(nil)
	identities := make(chan v2.FieldIdentity, count)
	errors := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			identity, err := allocator.AllocateField(context.Background())
			if err != nil {
				errors <- err
				return
			}
			identities <- identity
		}()
	}
	group.Wait()
	close(errors)
	close(identities)
	for err := range errors {
		t.Error(err)
	}
	seen := map[string]struct{}{}
	for identity := range identities {
		for _, value := range []string{
			identity.FieldID, identity.PhysicalName, identity.ProviderFieldID,
		} {
			if _, duplicate := seen[value]; duplicate {
				t.Fatalf("duplicate allocated identity %q", value)
			}
			seen[value] = struct{}{}
		}
	}
	if len(seen) != count*3 {
		t.Fatalf("allocated identities = %d, want %d", len(seen), count*3)
	}
}

func TestMemoryReservationStoreHonorsCancellationAndNamespace(t *testing.T) {
	t.Parallel()
	store := v2.NewMemoryReservationStore()
	ok, err := store.Reserve(context.Background(), "field", "same")
	if err != nil || !ok {
		t.Fatalf("first reserve = %v, %v", ok, err)
	}
	ok, err = store.Reserve(context.Background(), "field", "same")
	if err != nil || ok {
		t.Fatalf("duplicate reserve = %v, %v", ok, err)
	}
	ok, err = store.Reserve(context.Background(), "option", "same")
	if err != nil || !ok {
		t.Fatalf("cross-namespace reserve = %v, %v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Reserve(ctx, "field", "other"); err == nil {
		t.Fatal("cancelled reservation unexpectedly succeeded")
	}
}
