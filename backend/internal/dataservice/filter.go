package dataservice

import (
	"context"
	"slices"
	"time"
)

type filtered struct {
	Provider
	datasets []string
	since    time.Time
}

// FilterRecords limits a provider's records to the given datasets (all when empty) available at or after since
// (no limit when zero). Entities and capabilities are passed through. Store.Sync only inserts, so syncing a
// filtered provider never removes data outside the filter.
func FilterRecords(p Provider, datasets []string, since time.Time) Provider {
	return filtered{Provider: p, datasets: datasets, since: since}
}

func (f filtered) Records(ctx context.Context) ([]Record, error) {
	all, err := f.Provider.Records(ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, r := range all {
		if (len(f.datasets) == 0 || slices.Contains(f.datasets, r.DatasetID)) && !r.AvailableAt.Before(f.since) {
			out = append(out, r)
		}
	}
	return out, nil
}
