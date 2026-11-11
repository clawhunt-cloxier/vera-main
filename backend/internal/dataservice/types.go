// Package dataservice is the only data entry point for the Harness (docs/DATA_SERVICE.md).
package dataservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Entity as produced by a provider; IDs are assigned by the catalog on sync.
type Entity struct {
	Kind           string // stock | industry | market
	Code           string
	Name           string
	Exchange       string
	Status         string
	IsFinancial    bool
	Classification string
	Aliases        []string
	Attrs          map[string]any
}

// Record is a unified record before it receives a database id. Payload values follow contracts/datasets/catalog.json.
type Record struct {
	DatasetID  string
	EntityCode string // empty for market-wide datasets keyed otherwise
	RecordKey  string
	// LogicalKey groups revisions of the same fact (e.g. a restated statement item); defaults to RecordKey.
	LogicalKey  string
	Version     int
	Period      *time.Time
	PublishedAt *time.Time
	AvailableAt time.Time
	Origin      string
	SourceTier  string
	Basis       string
	Payload     map[string]any
}

// Hash is the content hash stored with the record.
func (r Record) Hash() string {
	b, _ := json.Marshal(r.Payload)
	h := sha256.Sum256(append([]byte(r.DatasetID+"|"+r.RecordKey+"|"), b...))
	return hex.EncodeToString(h[:])
}

// Capability states that a provider covers a dataset for an entity (nil = all entities of the dataset).
type Capability struct {
	DatasetID  string
	EntityCode string
	From, To   *time.Time
}

// Provider is implemented by every data source adapter (fixture now, vendors later in T014b).
type Provider interface {
	ID() string
	Name() string
	Synthetic() bool
	AllowModelProcessing() bool
	// Basis returns per-dataset methodology notes, e.g. order-size thresholds for flow.order_size.
	Basis() map[string]string
	// Now is the provider's data clock: wall time for vendors, the manifest as_of for fixtures.
	Now() time.Time
	Entities(ctx context.Context) ([]Entity, error)
	Records(ctx context.Context) ([]Record, error)
	Capabilities(ctx context.Context) ([]Capability, error)
}
