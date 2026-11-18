package emquant

import (
	"errors"
	"fmt"
)

var (
	ErrRateLimited  = errors.New("emquant: request rate limited")
	ErrUnavailable  = errors.New("emquant: upstream unavailable")
	ErrIPRestricted = errors.New("emquant: upstream access restricted by source IP")
)

// SDKError retains the vendor code for diagnosis without exposing credentials
// or upstream text in logs. Only verified codes are classified.
type SDKError struct {
	Operation string
	Code      int
}

func (e *SDKError) Error() string {
	return fmt.Sprintf("emquant: %s failed (code %d)", e.Operation, e.Code)
}

func (e *SDKError) Is(target error) bool {
	return (e.Code == 10000016 && target == ErrRateLimited) ||
		(e.Code == 10002002 && target == ErrUnavailable) ||
		(e.Code == 10000017 && target == ErrIPRestricted)
}
