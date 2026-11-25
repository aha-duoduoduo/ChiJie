package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const AccessKeyPrefix = "chijie_pk_v1_"

type AccessKey struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

type storedAccessKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hash       string     `json:"hash"`
	Ciphertext string     `json:"ciphertext"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// AccessKeys stores opaque proxy keys encrypted at rest so admins can copy them again.
type AccessKeys struct {
	mu      sync.RWMutex
	path    string
	aead    cipher.AEAD
	records map[string]storedAccessKey
	byHash  map[string]string
}

func LoadAccessKeys(path, secret string) (*AccessKeys, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("access-key encryption secret is too short")
	}
	material := sha256.Sum256([]byte("chijie:access-keys:v1:" + secret))
	key, migrate, err := storageMaterial(path, material[:])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &AccessKeys{path: path, aead: aead, records: make(map[string]storedAccessKey), byHash: make(map[string]string)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var records []storedAccessKey
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse access keys: %w", err)
	}
	for _, record := range records {
		if record.ID == "" || record.Hash == "" || record.Ciphertext == "" {
			return nil, fmt.Errorf("invalid access key record")
		}
		token, err := s.decrypt(record)
		if err != nil {
			return nil, fmt.Errorf("decrypt access key %s: %w", record.ID, err)
		}
		if keyHash(token) != record.Hash {
			return nil, fmt.Errorf("access key %s hash mismatch", record.ID)
		}
		if _, duplicate := s.records[record.ID]; duplicate {
			return nil, fmt.Errorf("duplicate access key id")
		}
		s.records[record.ID] = record
		s.byHash[record.Hash] = record.ID
	}
	if migrate {
		if err := writeStorageKey(path+".key", key); err != nil {
			return nil, fmt.Errorf("migrate access-key storage: %w", err)
		}
	}
	return s, nil
}

func keyHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func randomKeyPart(length int) (string, error) {
	data := make([]byte, length)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func (s *AccessKeys) Create(name string, duration time.Duration) (AccessKey, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "proxy-api"
	}
	if len(name) > 128 {
		return AccessKey{}, fmt.Errorf("key name too long")
	}
	if duration < 0 || duration > 365*24*time.Hour {
		return AccessKey{}, fmt.Errorf("invalid key duration")
	}
	id, err := randomKeyPart(12)
	if err != nil {
		return AccessKey{}, err
	}
	part, err := randomKeyPart(32)
	if err != nil {
		return AccessKey{}, err
	}
	token := AccessKeyPrefix + part
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return AccessKey{}, err
	}
	ciphertext := s.aead.Seal(nonce, nonce, []byte(token), []byte(id))
	now := time.Now().UTC()
	item := AccessKey{ID: id, Name: name, Token: token, CreatedAt: now}
	if duration > 0 {
		expiry := now.Add(duration)
		item.ExpiresAt = &expiry
	}
	record := storedAccessKey{ID: id, Name: name, Hash: keyHash(token), Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext), CreatedAt: now, ExpiresAt: item.ExpiresAt}
	s.mu.Lock()
	defer s.mu.Unlock()
	var previousRecords map[string]storedAccessKey
	var previousHashes map[string]string
	if len(s.records) >= 1000 {
		var inactive []storedAccessKey
		for _, record := range s.records {
			if record.RevokedAt != nil || (record.ExpiresAt != nil && !now.Before(*record.ExpiresAt)) {
				inactive = append(inactive, record)
			}
		}
		needed := len(s.records) - 999
		if len(inactive) < needed {
			return AccessKey{}, fmt.Errorf("active access key limit reached")
		}
		sort.Slice(inactive, func(i, j int) bool { return inactive[i].CreatedAt.Before(inactive[j].CreatedAt) })
		previousRecords = maps.Clone(s.records)
		previousHashes = maps.Clone(s.byHash)
		for _, record := range inactive[:needed] {
			delete(s.records, record.ID)
			delete(s.byHash, record.Hash)
		}
	}
	s.records[id] = record
	s.byHash[record.Hash] = id
	if err := s.saveLocked(); err != nil {
		delete(s.records, id)
		delete(s.byHash, record.Hash)
		if previousRecords != nil {
			s.records = previousRecords
			s.byHash = previousHashes
		}
		return AccessKey{}, err
	}
	return item, nil
}

func (s *AccessKeys) Verify(token string) bool {
	if !strings.HasPrefix(token, AccessKeyPrefix) {
		return false
	}
	hash := keyHash(token)
	s.mu.RLock()
	id := s.byHash[hash]
	record, exists := s.records[id]
	s.mu.RUnlock()
	if !exists || record.RevokedAt != nil {
		return false
	}
	return record.ExpiresAt == nil || time.Now().Before(*record.ExpiresAt)
}

func (s *AccessKeys) List() ([]AccessKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]AccessKey, 0, len(s.records))
	for _, record := range s.records {
		item := AccessKey{ID: record.ID, Name: record.Name, CreatedAt: record.CreatedAt, ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt}
		if record.RevokedAt == nil && (record.ExpiresAt == nil || time.Now().Before(*record.ExpiresAt)) {
			token, err := s.decrypt(record)
			if err != nil {
				return nil, err
			}
			item.Token = token
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items, nil
}

func (s *AccessKeys) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[id]
	if !exists {
		return os.ErrNotExist
	}
	if record.RevokedAt != nil {
		return nil
	}
	previous := record
	now := time.Now().UTC()
	record.RevokedAt = &now
	s.records[id] = record
	if err := s.saveLocked(); err != nil {
		s.records[id] = previous
		return err
	}
	return nil
}

func (s *AccessKeys) decrypt(record storedAccessKey) (string, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(record.Ciphertext)
	if err != nil {
		return "", err
	}
	if len(encoded) < s.aead.NonceSize() {
		return "", fmt.Errorf("short ciphertext")
	}
	plaintext, err := s.aead.Open(nil, encoded[:s.aead.NonceSize()], encoded[s.aead.NonceSize():], []byte(record.ID))
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (s *AccessKeys) saveLocked() error {
	items := make([]storedAccessKey, 0, len(s.records))
	for _, record := range s.records {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".keys-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
