package v2

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"
)

const maxIdentityAttempts = 32

type ReservationStore interface {
	Reserve(ctx context.Context, namespace string, value string) (bool, error)
}

type IdentityAllocator struct {
	store  ReservationStore
	random io.Reader
}

func NewIdentityAllocator(store ReservationStore) *IdentityAllocator {
	if store == nil {
		store = NewMemoryReservationStore()
	}
	return &IdentityAllocator{store: store, random: rand.Reader}
}

func (allocator *IdentityAllocator) AllocateField(ctx context.Context) (FieldIdentity, error) {
	fieldID, err := allocator.allocate(ctx, "field", "fld_", 20)
	if err != nil {
		return FieldIdentity{}, err
	}
	physicalName, err := allocator.allocate(ctx, "physical", "f_", 20)
	if err != nil {
		return FieldIdentity{}, err
	}
	providerFieldID, err := allocator.AllocateProviderField(ctx)
	if err != nil {
		return FieldIdentity{}, err
	}
	return FieldIdentity{
		FieldID: fieldID, PhysicalName: physicalName, ProviderFieldID: providerFieldID,
	}, nil
}

func (allocator *IdentityAllocator) AllocateProviderField(ctx context.Context) (string, error) {
	// PocketBase provider field IDs are exactly 15 characters.
	return allocator.allocate(ctx, "provider", "pb_", 12)
}

func (allocator *IdentityAllocator) AllocateOption(ctx context.Context) (string, error) {
	return allocator.allocate(ctx, "option", "opt_", 20)
}

func (allocator *IdentityAllocator) AllocatePresence(
	ctx context.Context,
	publicPhysicalName string,
) (PresenceSpec, error) {
	if !physicalNamePattern.MatchString(publicPhysicalName) {
		return PresenceSpec{}, fmt.Errorf("invalid public physical name %q", publicPhysicalName)
	}
	providerFieldID, err := allocator.AllocateProviderField(ctx)
	if err != nil {
		return PresenceSpec{}, err
	}
	return PresenceSpec{
		Mode: PresenceCompanion, ProviderFieldID: providerFieldID,
		PhysicalName: "__vt_has_" + publicPhysicalName,
	}, nil
}

func (allocator *IdentityAllocator) allocate(
	ctx context.Context,
	namespace string,
	prefix string,
	length int,
) (string, error) {
	for range maxIdentityAttempts {
		token, err := randomToken(allocator.random, length)
		if err != nil {
			return "", fmt.Errorf("allocate %s identity: %w", namespace, err)
		}
		candidate := prefix + token
		reserved, err := allocator.store.Reserve(ctx, namespace, candidate)
		if err != nil {
			return "", fmt.Errorf("reserve %s identity: %w", namespace, err)
		}
		if reserved {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("allocate %s identity: collision limit exceeded", namespace)
}

const identityAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func randomToken(reader io.Reader, length int) (string, error) {
	raw := make([]byte, length)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return "", err
	}
	var result strings.Builder
	result.Grow(length)
	for _, value := range raw {
		result.WriteByte(identityAlphabet[int(value)%len(identityAlphabet)])
	}
	return result.String(), nil
}

type MemoryReservationStore struct {
	mu     sync.Mutex
	values map[string]struct{}
}

func NewMemoryReservationStore() *MemoryReservationStore {
	return &MemoryReservationStore{values: map[string]struct{}{}}
}

func (store *MemoryReservationStore) Reserve(
	ctx context.Context,
	namespace string,
	value string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	key := namespace + "\x00" + value
	if _, exists := store.values[key]; exists {
		return false, nil
	}
	store.values[key] = struct{}{}
	return true, nil
}
