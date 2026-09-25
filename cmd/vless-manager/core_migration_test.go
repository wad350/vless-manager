package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoreMigrationBackupIsPrivateAndNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := backupBeforeXray(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "subscriptions.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("original"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := backupBeforeXray(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "subscriptions.json"} {
		os.WriteFile(filepath.Join(dir, name), []byte("new"), 0644)
	}
	if err := backupBeforeXray(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "subscriptions.json"} {
		path := filepath.Join(dir, name) + ".before-xray"
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatalf("backup changed: %q %v", data, err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("backup permissions: %v %v", info, err)
		}
	}
}
