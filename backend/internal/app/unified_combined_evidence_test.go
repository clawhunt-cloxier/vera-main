//go:build linux && cgo

package app

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestUnifiedCombinedEvidence(t *testing.T) {
	library := filepath.Join(t.TempDir(), "choice-supplement.so")
	if output, err := exec.Command("cc", "-shared", "-fPIC", "-o", library, "../dataservice/emquant/testdata/brief.c").CombinedOutput(); err != nil {
		t.Fatalf("compile Choice SDK stub: %v: %s", err, output)
	}
	testUnifiedCloseReportsWithoutAnnouncements(t, library)
}
