//go:build linux && cgo

package emquant

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestOpenRejectsInvalidAndMissingLibrary(t *testing.T) {
	for _, path := range []string{"relative.so", filepath.Join(t.TempDir(), "missing.so")} {
		if c, err := Open(path, "user", "pass"); err == nil {
			_ = c.Close()
			t.Fatalf("Open(%q) unexpectedly succeeded", path)
		}
	}
}

func TestNativeLifecycleAndResponse(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "fake.so")
	if output, err := exec.Command("cc", "-shared", "-fPIC", "-o", lib, "testdata/fake.c").CombinedOutput(); err != nil {
		t.Fatalf("compile fake library: %v: %s", err, output)
	}
	if _, err := Open(lib, "user", "wrong"); err == nil || !strings.Contains(err.Error(), "77") {
		t.Fatalf("login error: %v", err)
	}
	c, err := Open(lib, "user", "pass")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	copyOfClient := *c
	if err := copyOfClient.Close(); err == nil {
		t.Fatal("copied client was accepted")
	}
	if _, err = Open(lib, "user", "pass"); err == nil {
		t.Fatal("second session accepted")
	}
	result, err := c.CSD("A,B", "X,Y", "D1", "D2", "")
	if err != nil {
		t.Fatal(err)
	}
	value, err := result.At(1, 0, 1)
	if err != nil || value.Value != int64(6) {
		t.Fatalf("value: %#v, %v", value, err)
	}
	value, err = result.At(1, 1, 1)
	if err != nil || value.Type != 0 || value.Value != nil {
		t.Fatalf("null: %#v, %v", value, err)
	}
	if _, err = c.CSD("TRUNCATED", "X", "D1", "D2", ""); err == nil {
		t.Fatal("accepted truncated result")
	}
	if _, err = c.CSD("BADUTF", "X", "D1", "D2", ""); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
	if _, err = c.CSD("ERROR", "X", "D1", "D2", ""); err == nil || !strings.Contains(err.Error(), "88") {
		t.Fatalf("query error: %v", err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CSD("A", "X", "D1", "D2", ""); err == nil {
		t.Fatal("closed client accepted query")
	}
	var zero Client
	if err = zero.Close(); err != nil {
		t.Fatal(err)
	}
}

// This test is explicitly opt-in: it consumes vendor quota and needs a local
// runtime copy. It never prints credentials or market data.
func TestLiveSDK(t *testing.T) {
	if os.Getenv("EMQUANT_INTEGRATION") != "1" {
		t.Skip("live vendor SDK test not requested")
	}
	c, err := Open(os.Getenv("EMQUANT_LIBRARY"), os.Getenv("EMQUANT_USER"), os.Getenv("EMQUANT_PASSWORD"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := Open(os.Getenv("EMQUANT_LIBRARY"), "x", "y"); err == nil {
		t.Fatal("second session was accepted")
	}
	cube, err := c.CSD("300059.SZ", "CLOSE", "2026-11-02", "2026-11-02", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cube.Codes) != 1 || len(cube.Indicators) != 1 || len(cube.Dates) != 1 {
		t.Fatal("unexpected result dimensions")
	}
	value, err := cube.At(0, 0, 0)
	if err != nil || value.Type != 11 {
		t.Fatalf("unexpected cell type %d, error %v", value.Type, err)
	}
	status, err := c.CSD("300059.SZ", "TRADESTATUS", "2026-11-02", "2026-11-02", "Period=1")
	if err != nil {
		t.Fatal(err)
	}
	statusValue, err := status.At(0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := statusValue.Value.(string)
	if !ok {
		t.Fatalf("unexpected status cell type %d", statusValue.Type)
	}
	if !strings.ContainsFunc(text, func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
		t.Fatal("status response did not contain Chinese text")
	}
}
