package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var socksUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// SocksAccount is the public account record. The password hash is never returned by the API.
type SocksAccount struct {
	Username    string        `json:"username"`
	Egress      EgressOptions `json:"egress"`
	AffinityTTL string        `json:"affinity_ttl,omitempty"`
	Enabled     bool          `json:"enabled"`
}

type storedSocksAccount struct {
	SocksAccount
	PasswordHash string `json:"password_hash"`
}

// SocksAccounts persists only password hashes. An empty store denies every SOCKS login.
type SocksAccounts struct {
	mu       sync.RWMutex
	path     string
	accounts map[string]storedSocksAccount
}

func LoadSocksAccounts(path string) (*SocksAccounts, error) {
	s := &SocksAccounts{path: path, accounts: make(map[string]storedSocksAccount)}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var records []storedSocksAccount
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("parse SOCKS accounts: %w", err)
	}
	for _, record := range records {
		if !socksUsernamePattern.MatchString(record.Username) || record.PasswordHash == "" {
			return nil, fmt.Errorf("invalid SOCKS account record")
		}
		if _, exists := s.accounts[record.Username]; exists {
			return nil, fmt.Errorf("duplicate SOCKS account: %s", record.Username)
		}
		s.accounts[record.Username] = record
	}
	return s, nil
}

func (s *SocksAccounts) List() []SocksAccount {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]SocksAccount, 0, len(s.accounts))
	for _, record := range s.accounts {
		items = append(items, record.SocksAccount)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Username < items[j].Username })
	return items
}

func (s *SocksAccounts) Authenticate(username, password string) (SocksAccount, bool) {
	s.mu.RLock()
	record, exists := s.accounts[username]
	s.mu.RUnlock()
	if !exists || !record.Enabled || bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(password)) != nil {
		return SocksAccount{}, false
	}
	return record.SocksAccount, true
}

func (s *SocksAccounts) Create(account SocksAccount, password string) (string, error) {
	if !socksUsernamePattern.MatchString(account.Username) {
		return "", fmt.Errorf("username must be 1-64 ASCII letters, digits, underscore, dot or hyphen")
	}
	if err := validateSocksAccount(account); err != nil {
		return "", err
	}
	if password == "" {
		random := make([]byte, 24)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		password = base64.RawURLEncoding.EncodeToString(random)
	}
	if len(password) < 12 || len(password) > 72 {
		return "", fmt.Errorf("password must be 12-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[account.Username]; exists {
		return "", fmt.Errorf("SOCKS username already exists")
	}
	s.accounts[account.Username] = storedSocksAccount{SocksAccount: account, PasswordHash: string(hash)}
	if err := s.saveLocked(); err != nil {
		delete(s.accounts, account.Username)
		return "", err
	}
	return password, nil
}

func (s *SocksAccounts) Update(account SocksAccount, password string) error {
	if err := validateSocksAccount(account); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.accounts[account.Username]
	if !exists {
		return os.ErrNotExist
	}
	next := previous
	next.SocksAccount = account
	if password != "" {
		if len(password) < 12 || len(password) > 72 {
			return fmt.Errorf("password must be 12-72 bytes")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		next.PasswordHash = string(hash)
	}
	s.accounts[account.Username] = next
	if err := s.saveLocked(); err != nil {
		s.accounts[account.Username] = previous
		return err
	}
	return nil
}

func (s *SocksAccounts) Delete(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, exists := s.accounts[username]
	if !exists {
		return os.ErrNotExist
	}
	delete(s.accounts, username)
	if err := s.saveLocked(); err != nil {
		s.accounts[username] = previous
		return err
	}
	return nil
}

func validateSocksAccount(account SocksAccount) error {
	if account.AffinityTTL != "" {
		ttl, err := time.ParseDuration(account.AffinityTTL)
		if err != nil || ttl < 0 || ttl > 30*24*time.Hour {
			return fmt.Errorf("affinity_ttl must be between 0 and 720h")
		}
	}
	return validateRelayEgress(account.Egress)
}

func (s *SocksAccounts) saveLocked() error {
	items := make([]storedSocksAccount, 0, len(s.accounts))
	for _, record := range s.accounts {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Username < items[j].Username })
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".socks-*")
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
