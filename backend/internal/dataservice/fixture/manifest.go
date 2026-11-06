// Package fixture generates the synthetic demo universe described by tests/fixtures/demo-universe.json.
// It is refused in production (config) and every record it emits is marked synthetic via its provider.
package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type Manifest struct {
	FixtureID          string     `json:"fixture_id"`
	Synthetic          bool       `json:"synthetic"`
	ProductionEligible bool       `json:"production_eligible"`
	Seed               int64      `json:"seed"`
	AsOf               time.Time  `json:"as_of"`
	HistoryStart       string     `json:"history_start"`
	Industries         []Industry `json:"industries"`
	Securities         []Security `json:"securities"`
	Adversarial        []Adv      `json:"adversarial"`
}

type Industry struct {
	ID         string   `json:"id"`
	Taxonomy   string   `json:"taxonomy"`
	Level1Name string   `json:"level1_name"`
	Level2Name string   `json:"level2_name"`
	Members    []string `json:"members"`
}

type Security struct {
	Code           string `json:"code"`
	Name           string `json:"name"`
	Industry       string `json:"industry"`
	Profile        string `json:"profile"`
	IsFinancial    bool   `json:"is_financial"`
	MarginEligible bool   `json:"margin_eligible"`
}

type Adv struct {
	ID      string `json:"id"`
	Target  string `json:"target"`
	Dataset string `json:"dataset"`
	Content string `json:"content"`
}

func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse fixture manifest: %w", err)
	}
	if !m.Synthetic || m.ProductionEligible {
		return nil, fmt.Errorf("fixture manifest must be synthetic and not production eligible")
	}
	return &m, nil
}
