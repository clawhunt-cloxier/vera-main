package emquant

import (
	"errors"
	"testing"
)

func TestSDKErrorClassification(t *testing.T) {
	for _, tt := range []struct {
		code   int
		target error
		want   bool
	}{
		{10000016, ErrRateLimited, true}, {10002002, ErrUnavailable, true},
		{10000016, ErrUnavailable, false}, {12345, ErrRateLimited, false},
	} {
		if got := errors.Is(&SDKError{Operation: "query", Code: tt.code}, tt.target); got != tt.want {
			t.Fatalf("code %d classification=%t", tt.code, got)
		}
	}
}
