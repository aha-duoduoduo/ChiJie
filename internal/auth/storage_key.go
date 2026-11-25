package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The storage key survives JWT signing-key rotation. The key file belongs in
// the same protected backup as access-keys.json, and is always written 0600.
type storageKeyFile struct {
	Version int    `json:"version"`
	Key     string `json:"key"`
}

func readStorageKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file storageKeyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(file.Key)
	if err != nil || file.Version != 1 || len(key) != 32 {
		return nil, fmt.Errorf("invalid access-key storage key")
	}
	return key, nil
}

func writeStorageKey(path string, key []byte) error {
	data, err := json.Marshal(storageKeyFile{Version: 1, Key: base64.StdEncoding.EncodeToString(key)})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".storage-key-*")
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
	// Link publishes the fully synced key without overwriting another creator.
	if err := os.Link(tmp.Name(), path); err != nil {
		return err
	}
	return nil
}

func storageMaterial(path string, legacy []byte) ([]byte, bool, error) {
	key, err := readStorageKey(path + ".key")
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	if _, err := os.Stat(path); err == nil {
		// Existing ciphertext is validated before publishing its legacy key.
		return legacy, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	if err := writeStorageKey(path+".key", key); err != nil {
		if errors.Is(err, os.ErrExist) {
			key, err = readStorageKey(path + ".key")
		}
		return key, false, err
	}
	return key, false, nil
}
