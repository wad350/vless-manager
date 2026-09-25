package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Preserve pre-migration catalog data before the normal unsupported-transport
// cleanup. Never replace the original backup on a later restart.
func backupBeforeXray(dataDir string) error {
	for _, name := range []string{"config.json", "subscriptions.json"} {
		source := filepath.Join(dataDir, name)
		backup := source + ".before-xray"
		if _, err := os.Lstat(backup); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := os.ReadFile(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err := writeCoreBackup(backup, data); err != nil {
			return fmt.Errorf("backup %s: %w", name, err)
		}
	}
	return nil
}

func writeCoreBackup(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".core-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Atomic no-overwrite publication; CreateTemp already uses mode 0600.
	err = os.Link(f.Name(), path)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}
