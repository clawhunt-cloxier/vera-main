package emquant

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
)

// RegisteredArchive ties an archived result to the registry identity. It is
// not a published research record or proof of data-product permissions.
type RegisteredArchive struct {
	ID        uuid.UUID
	DatasetID string
	Source    ArchiveRef
}

// RegisteredCollector exposes data only after both object storage and the
// database registry have accepted it. A failed database write can leave an
// orphaned object, which must not be treated as a published research record.
type RegisteredCollector struct {
	Collector Collector
	Registry  ArchiveRegistry
}

func (c RegisteredCollector) validate(dataset string) error {
	if err := c.validateConfig(); err != nil {
		return err
	}
	if _, ok := c.Registry.Catalog.Dataset(dataset); !ok {
		return errors.New("emquant: collection dataset is not in catalog")
	}
	return nil
}

func (c RegisteredCollector) validateConfig() error {
	store, ok := c.Collector.Store.(*S3RawStore)
	if c.Collector.Gate == nil || c.Registry.Pool == nil || c.Registry.Store == nil || c.Registry.Catalog == nil || !ok || store != c.Registry.Store || c.Registry.ProviderID == "" || len(c.Registry.ProviderID) > 128 || strings.TrimSpace(c.Registry.ProviderID) != c.Registry.ProviderID {
		return errors.New("emquant: registered collector is not configured")
	}
	return nil
}

func (c RegisteredCollector) CollectCSD(ctx context.Context, dataset, codes, indicators, from, to, options string) (Cube, RegisteredArchive, error) {
	if err := c.validate(dataset); err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	cube, ref, err := c.Collector.CollectCSD(ctx, codes, indicators, from, to, options)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	id, err := c.Registry.RegisterCube(ctx, dataset, ref)
	if err != nil {
		return Cube{}, RegisteredArchive{}, err
	}
	return cube, RegisteredArchive{ID: id, DatasetID: dataset, Source: ref}, nil
}

func (c RegisteredCollector) CollectQuote(ctx context.Context, dataset, codes, indicators, options string) (Quote, RegisteredArchive, error) {
	if err := c.validate(dataset); err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	if dataset != "mkt.intraday_quote" && dataset != "mkt.intraday_index_quote" {
		return Quote{}, RegisteredArchive{}, errors.New("emquant: quote requires an intraday dataset")
	}
	quote, ref, err := c.Collector.CollectQuote(ctx, codes, indicators, options)
	if err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	id, err := c.Registry.RegisterCube(ctx, dataset, ref)
	if err != nil {
		return Quote{}, RegisteredArchive{}, err
	}
	return quote, RegisteredArchive{ID: id, DatasetID: dataset, Source: ref}, nil
}
