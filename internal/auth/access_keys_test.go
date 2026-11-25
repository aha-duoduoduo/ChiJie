package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAccessKeysEncryptedPersistentAndRevocable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "access-keys.json")
	secret := "test-secret-for-access-keys"
	store, err := LoadAccessKeys(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	never, err := store.Create("bot", 0)
	if err != nil {
		t.Fatal(err)
	}
	if never.ExpiresAt != nil || !store.Verify(never.Token) {
		t.Fatal("never-expiring key is invalid")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(content, []byte(never.Token)) {
		t.Fatal("plaintext key persisted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v", info.Mode())
	}
	reloaded, err := LoadAccessKeys(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Verify(never.Token) {
		t.Fatal("key did not survive restart")
	}
	items, err := reloaded.List()
	if err != nil || len(items) != 1 || items[0].Token != never.Token {
		t.Fatalf("listed key = %#v, %v", items, err)
	}
	if err := reloaded.Revoke(never.ID); err != nil {
		t.Fatal(err)
	}
	if reloaded.Verify(never.Token) {
		t.Fatal("revoked key accepted")
	}
	items, err = reloaded.List()
	if err != nil || len(items) != 1 || items[0].Token != "" || items[0].RevokedAt == nil {
		t.Fatalf("revoked listing = %#v, %v", items, err)
	}
	short, err := reloaded.Create("short", 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if reloaded.Verify(short.Token) {
		t.Fatal("expired key accepted")
	}
}
