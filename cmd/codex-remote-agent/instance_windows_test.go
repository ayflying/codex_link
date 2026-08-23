//go:build windows

package main

import (
	"errors"
	"testing"
)

func TestAcquireInstancePreventsDuplicate(t *testing.T) {
	dataDir := t.TempDir()
	release, err := acquireInstance("test", dataDir)
	if err != nil {
		t.Fatalf("acquire instance: %v", err)
	}
	defer release()

	secondRelease, err := acquireInstance("test", dataDir)
	if !errors.Is(err, errInstanceAlreadyRunning) {
		t.Fatalf("expected duplicate instance error, got %v", err)
	}
	secondRelease()
}
