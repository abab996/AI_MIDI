package project

import (
	"bytes"
	"os"
	"testing"
)

func TestLegacyArrangementBackupIsOriginalAndIdempotent(t *testing.T) {
	_, pid := setupArrangementEnv(t)
	file, err := ArrangementFile(pid)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"version":1,"rev":7,"tracks":[],"unknownLegacy":{"data":"preserve"}}`)
	if err = os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	next := []byte(`{"version":2,"rev":7,"tracks":[]}`)
	if _, err = WriteArrangementVersioned(pid, next); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(file + ".before-v2-migration")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("backup: %s %v", backup, err)
	}
	current, err := ReadArrangement(pid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = WriteArrangementVersioned(pid, current); err != nil {
		t.Fatal(err)
	}
	repeated, err := os.ReadFile(file + ".before-v2-migration")
	if err != nil || !bytes.Equal(repeated, original) {
		t.Fatal("repeat changed original backup")
	}
}
