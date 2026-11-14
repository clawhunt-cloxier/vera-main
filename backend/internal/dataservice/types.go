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

	// Blood lineage (optional; set by vendor adapters, not fixture).
	RawRef         string // object-storage key for the raw response
	RawSHA256      string // SHA-256 of raw response body
	RawLocator     string // JSON path or page range within raw body
	MappingVersion int    // version of the MappingSpec used

	// Quality state: PASS (default) | WARN | QUARANTINED | REJECTED.
	// QUARANTINED records are excluded from snapshots.
	QualityState string
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

// ErrClass classifies provider errors so callers can decide whether to retry, skip, or alert.
type ErrClass string

const (
	ErrRateLimited         ErrClass = "RATE_LIMITED"         // back off and retry
	ErrAuthFailed          ErrClass = "AUTH_FAILED"          // do not retry; block provider
	ErrNotCovered          ErrClass = "NOT_COVERED"          // provider does not cover this subject/period
	ErrSchemaChanged       ErrClass = "SCHEMA_CHANGED"       // mapping failed; alert and quarantine
	ErrUpstreamUnavailable ErrClass = "UPSTREAM_UNAVAILABLE" // transient; retry
	ErrEmptyValid          ErrClass = "EMPTY_VALID"          // no data for period (e.g. no dragon-tiger today)
)

// ProviderError carries a classification alongside the underlying error.
type ProviderError struct {
	Class ErrClass
	Err   error
}

func (e *ProviderError) Error() string { return string(e.Class) + ": " + e.Err.Error() }
func (e *ProviderError) Unwrap() error { return e.Err }

// QualityIssue is produced by Mapper.Normalize for a single field or record.
type QualityIssue struct {
	RecordKey string
	Field     string
	Message   string
	Fatal     bool // true → record should be QUARANTINED
}

// MappingSpec describes how to map one provider dataset to a canonical dataset.
// Loaded from backend/internal/dataservice/mappings/<provider>/<dataset>.yaml.
type MappingSpec struct {
	Provider        string       `yaml:"provider"`
	ProviderDataset string       `yaml:"provider_dataset"`
	Target          string       `yaml:"target"`
	Version         int          `yaml:"version"`
	Fields          []FieldMap   `yaml:"fields"`
	Basis           MappingBasis `yaml:"basis"`
}

// FieldMap maps one provider field to a canonical field.
type FieldMap struct {
	Target    string `yaml:"target"`
	Source    string `yaml:"source"` // JSONPath expression ($.field or $.nested.field)
	Unit      string `yaml:"unit"`
	Transform string `yaml:"transform"` // decimal | date_yyyymmdd | scale_to_base | string | integer
}

// MappingBasis records methodology metadata for the mapping (adjust, tier, etc.)
type MappingBasis struct {
	Adjust string `yaml:"adjust"`
	Tier   string `yaml:"tier"`
}
