package dataservice

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// Field is one entry of contracts/datasets/catalog.json.
type Field struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Unit        string   `json:"unit"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
	Values      []string `json:"values"`
	Ref         string   `json:"ref"`
}

type Dataset struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Origin      string   `json:"origin"`
	DefaultTier string   `json:"default_tier"`
	Grain       []string `json:"grain"`
	MVP         bool     `json:"mvp"`
	Fields      []Field  `json:"fields"`
}

type StatementItem struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	Statement string `json:"statement"`
}

type Catalog struct {
	Version        string            `json:"version"`
	Units          map[string]string `json:"units"`
	Datasets       []Dataset         `json:"datasets"`
	StatementItems []StatementItem   `json:"statement_items"`
	byID           map[string]*Dataset
	items          map[string]StatementItem
}

func LoadCatalog(path string) (*Catalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Catalog
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	c.byID = map[string]*Dataset{}
	c.items = map[string]StatementItem{}
	for _, it := range c.StatementItems {
		c.items[it.Code] = it
	}
	for i := range c.Datasets {
		c.byID[c.Datasets[i].ID] = &c.Datasets[i]
	}
	return &c, nil
}

func (c *Catalog) Dataset(id string) (*Dataset, bool) { d, ok := c.byID[id]; return d, ok }

// Item returns the statement item definition (label in Chinese, statement kind).
func (c *Catalog) Item(code string) (StatementItem, bool) { it, ok := c.items[code]; return it, ok }

func (d *Dataset) Field(name string) (*Field, bool) {
	for i := range d.Fields {
		if d.Fields[i].Name == name {
			return &d.Fields[i], true
		}
	}
	return nil, false
}

var datePat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Validate checks a payload against the dictionary: no unknown fields, required present, types parse.
// Decimal fields must be strings so that no float rounding enters the evidence chain.
func (c *Catalog) Validate(r Record) error {
	d, ok := c.byID[r.DatasetID]
	if !ok {
		return fmt.Errorf("dataset %q not in catalog", r.DatasetID)
	}
	for k := range r.Payload {
		if _, ok := d.Field(k); !ok {
			return fmt.Errorf("%s/%s: unknown field %q", r.DatasetID, r.RecordKey, k)
		}
	}
	for _, f := range d.Fields {
		v, present := r.Payload[f.Name]
		if !present || v == nil {
			if f.Required {
				return fmt.Errorf("%s/%s: missing required %q", r.DatasetID, r.RecordKey, f.Name)
			}
			continue
		}
		if err := c.checkType(f, v); err != nil {
			return fmt.Errorf("%s/%s: field %q: %w", r.DatasetID, r.RecordKey, f.Name, err)
		}
	}
	return nil
}

func (c *Catalog) checkType(f Field, v any) error {
	s, isStr := v.(string)
	switch f.Type {
	case "decimal":
		if !isStr {
			return fmt.Errorf("decimal must be a string, got %T", v)
		}
		_, err := decimal.NewFromString(s)
		return err
	case "integer":
		switch n := v.(type) {
		case int, int64:
			return nil
		case float64:
			if n == float64(int64(n)) {
				return nil
			}
		}
		return fmt.Errorf("want integer, got %v", v)
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", v)
		}
	case "date":
		if !isStr || !datePat.MatchString(s) {
			return fmt.Errorf("want YYYY-MM-DD, got %v", v)
		}
	case "datetime":
		if !isStr {
			return fmt.Errorf("want RFC3339 string")
		}
		_, err := time.Parse(time.RFC3339, s)
		return err
	case "enum":
		if !isStr || !slices.Contains(f.Values, s) {
			return fmt.Errorf("value %v not in %v", v, f.Values)
		}
	case "enum_ref":
		if f.Ref != "statement_items" {
			return fmt.Errorf("unsupported ref %q", f.Ref)
		}
		if _, ok := c.items[s]; !isStr || !ok {
			return fmt.Errorf("unknown statement item %v", v)
		}
	case "string", "text":
		if !isStr {
			return fmt.Errorf("want string, got %T", v)
		}
	default:
		return fmt.Errorf("unknown catalog type %q", f.Type)
	}
	return nil
}
