package identity

import "testing"

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{"": "/app", "/app/tasks?x=1": "/app/tasks?x=1", "https://evil.example": "/app",
		"//evil.example": "/app", `/\evil`: "/app", "app": "/app"} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q want %q", in, got, want)
		}
	}
	if NormalizeInvite(" vera-abcd ") != "VERA-ABCD" {
		t.Error("normalize")
	}
}
