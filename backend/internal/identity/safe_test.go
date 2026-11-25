package identity

import "testing"

func TestSafeReturn(t *testing.T) {
	for in, want := range map[string]string{
		"":                     "/app",
		"/app/tasks?x=1":       "/app/tasks?x=1",
		"/app/chat/问答":         "/app/chat/问答",
		"https://evil.example": "/app",
		"//evil.example":       "/app",
		`/\evil`:               "/app",
		"app":                  "/app",
		"/app\t/tasks":         "/app",
		"/app\n/tasks":         "/app",
		"/app\r/tasks":         "/app",
		"/app\x00/tasks":       "/app",
		"/app\x7f/tasks":       "/app",
	} {
		if got := safeReturn(in); got != want {
			t.Errorf("safeReturn(%q)=%q want %q", in, got, want)
		}
	}
	if NormalizeInvite(" vera-abcd ") != "VERA-ABCD" {
		t.Error("normalize")
	}
}
