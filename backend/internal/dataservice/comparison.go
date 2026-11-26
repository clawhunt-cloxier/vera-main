package dataservice

const (
	ComparisonSubjectLimit  = 5
	ComparisonPeerLimit     = 3
	ComparisonSampleDataset = "ref.industry_peer_sample"
)

// ComparisonSample describes the selection produced by a governed publication.
// It contains no prices or user text. Codes exclude the comparison anchor.
type ComparisonSample struct {
	Codes        []string `json:"codes,omitempty"`
	Date         string   `json:"date,omitempty"`
	IndustryCode string   `json:"industry_code,omitempty"`
	ProofSHA256  string   `json:"proof_sha256,omitempty"`
}
