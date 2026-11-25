package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyKeysMigrateAndSurviveJWTRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	// An existing v1 empty store uses the legacy JWT-derived encryption key.
	if err := os.WriteFile(path, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := LoadAccessKeys(path, "original-jwt-secret-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Create("legacy", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".key"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAccessKeys(path, "wrong-original-secret-123456"); err == nil {
		t.Fatal("legacy ciphertext accepted a wrong secret")
	}
	if _, err := os.Stat(path + ".key"); !os.IsNotExist(err) {
		t.Fatal("failed migration published a key")
	}
	if _, err := LoadAccessKeys(path, "original-jwt-secret-at-least-16"); err != nil {
		t.Fatal(err)
	}
	rotated, err := LoadAccessKeys(path, "rotated-jwt-secret-at-least-16")
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.Verify(key.Token) {
		t.Fatal("JWT rotation invalidated an access key")
	}
	items, err := rotated.List()
	if err != nil || len(items) != 1 || items[0].Token != key.Token {
		t.Fatalf("could not decrypt existing key: %v", err)
	}
	info, err := os.Stat(path + ".key")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("storage key permissions: %v %v", info, err)
	}
}

func TestAccessKeyCapacityReclaimsInactiveHistory(t *testing.T) {
	store, err := LoadAccessKeys(filepath.Join(t.TempDir(), "keys.json"), "test-storage-secret-12345")
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.Create("active", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Isolate quota accounting from random token generation and 1,000 disk syncs.
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 999; i++ {
		id := fmt.Sprintf("history-%04d", i)
		store.records[id] = storedAccessKey{ID: id, Hash: id, CreatedAt: old, RevokedAt: &old}
		store.byHash[id] = id
	}
	if _, err := store.Create("replacement", 0); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 1000 || !store.Verify(active.Token) {
		t.Fatal("reclamation removed an active key or exceeded capacity")
	}
}
