// Package tushare collects TuShare Pro data for Vera's cutoff-based research skills.
package tushare

import (
	"context"
	"time"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// Row preserves the exact numeric representation and the location of a row in its archived response.
// Absent, null and empty cells are omitted.
type Row struct {
	Values                        map[string]string
	RawRef, RawSHA256, RawLocator string
	ReceivedAt                    time.Time
}

type Source interface {
	Rows(ctx context.Context, api string, params map[string]string, fields []string, pageSize int) ([]Row, error)
	Archive(ctx context.Context, kind string, data []byte) (ref, sha string, err error)
}

type Batch struct {
	Entities []dataservice.Entity
	Records  []dataservice.Record
	Peers    []string
	Notes    []string
}
