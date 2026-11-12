package emquant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"
)

// RawStore persists SDK-decoded data in object storage. It must store the exact
// body supplied, be idempotent for the same hash, and return a durable reference.
// The SDK exposes typed values rather than the vendor's transport bytes; the
// archived content therefore cannot be described as the original wire payload.
type RawStore interface {
	Put(ctx context.Context, sha256 string, body []byte) (ref string, err error)
}

type ArchiveRef struct {
	RequestFingerprint string
	BodySHA256         string
	BodyRef            string
	ReceivedAt         time.Time
}

// Fetcher isolates SDK access from archival. A caller can replace it with a
// test implementation, but production wiring must supply a real SDK client.
type Fetcher interface {
	CSD(codes, indicators, from, to, options string) (Cube, error)
	QuoteSnapshot(codes, indicators, options string) (Quote, error)
}

type Collector struct {
	Fetcher Fetcher
	Store   RawStore
}

type request struct {
	Method     string `json:"method"`
	Codes      string `json:"codes"`
	Indicators string `json:"indicators"`
	From       string `json:"from,omitempty"`
	To         string `json:"to,omitempty"`
	Options    string `json:"options,omitempty"`
}

type archiveBody struct {
	Format     string    `json:"format"`
	ReceivedAt time.Time `json:"received_at"`
	Data       Cube      `json:"data"`
}

// DecodeArchive restores SDK value types and checks the content hash before
// replay. JSON's default float64 decoding would silently lose 64-bit integers.
func DecodeArchive(body []byte, expectedHash string) (Cube, time.Time, error) {
	if len(body) > 64<<20 {
		return Cube{}, time.Time{}, errors.New("emquant: archive too large")
	}
	if len(expectedHash) != 64 || digest(body) != expectedHash {
		return Cube{}, time.Time{}, errors.New("emquant: archive hash mismatch")
	}
	var raw archiveBody
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return Cube{}, time.Time{}, fmt.Errorf("emquant: invalid archive: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Cube{}, time.Time{}, errors.New("emquant: trailing archive content")
	}
	if raw.Format != "emquant-sdk-decoded-v1" || raw.ReceivedAt.IsZero() {
		return Cube{}, time.Time{}, errors.New("emquant: unsupported archive format")
	}
	for i := range raw.Data.Values {
		v := &raw.Data.Values[i]
		switch v.Type {
		case 0:
			if v.Value != nil {
				return Cube{}, time.Time{}, errors.New("emquant: malformed null")
			}
		case 1, 3, 4, 6, 8:
			n, ok := v.Value.(json.Number)
			if !ok {
				return Cube{}, time.Time{}, errors.New("emquant: malformed signed value")
			}
			value, err := n.Int64()
			if err != nil {
				return Cube{}, time.Time{}, errors.New("emquant: invalid signed value")
			}
			v.Value = value
		case 2, 5, 7, 9:
			n, ok := v.Value.(json.Number)
			if !ok {
				return Cube{}, time.Time{}, errors.New("emquant: malformed unsigned value")
			}
			value, err := strconv.ParseUint(string(n), 10, 64)
			if err != nil {
				return Cube{}, time.Time{}, errors.New("emquant: invalid unsigned value")
			}
			v.Value = value
		case 10, 11:
			n, ok := v.Value.(json.Number)
			if !ok {
				return Cube{}, time.Time{}, errors.New("emquant: malformed floating value")
			}
			value, err := n.Float64()
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return Cube{}, time.Time{}, errors.New("emquant: invalid floating value")
			}
			v.Value = value
		case 13, 14:
			if _, ok := v.Value.(string); !ok {
				return Cube{}, time.Time{}, errors.New("emquant: malformed string")
			}
		default:
			return Cube{}, time.Time{}, errors.New("emquant: unsupported archived value type")
		}
	}
	if len(raw.Data.Codes) > 0 && len(raw.Data.Indicators) > 0 && len(raw.Data.Dates) > 0 {
		if _, err := raw.Data.At(0, 0, 0); err != nil {
			return Cube{}, time.Time{}, err
		}
	} else if len(raw.Data.Values) != 0 {
		return Cube{}, time.Time{}, ErrShape
	}
	return raw.Data, raw.ReceivedAt, nil
}

func digest(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func (c Collector) persist(ctx context.Context, req request, cube Cube, received time.Time) (ArchiveRef, error) {
	if c.Store == nil {
		return ArchiveRef{}, errors.New("emquant: raw store is required")
	}
	if err := ctx.Err(); err != nil {
		return ArchiveRef{}, err
	}
	// Hash the complete request without logging or archiving request options;
	// options could accidentally contain secrets or proprietary identifiers.
	requestBody, err := json.Marshal(req)
	if err != nil {
		return ArchiveRef{}, fmt.Errorf("emquant: fingerprint: %w", err)
	}
	body, err := json.Marshal(archiveBody{Format: "emquant-sdk-decoded-v1", ReceivedAt: received.UTC(), Data: cube})
	if err != nil {
		return ArchiveRef{}, fmt.Errorf("emquant: encode raw result: %w", err)
	}
	hash := digest(body)
	ref, err := c.Store.Put(ctx, hash, body)
	if err != nil {
		return ArchiveRef{}, fmt.Errorf("emquant: archive result: %w", err)
	}
	if ref == "" {
		return ArchiveRef{}, errors.New("emquant: raw store returned an empty reference")
	}
	if err := ctx.Err(); err != nil {
		return ArchiveRef{}, err
	}
	return ArchiveRef{RequestFingerprint: digest(requestBody), BodySHA256: hash, BodyRef: ref, ReceivedAt: received.UTC()}, nil
}

// CollectCSD returns data only after the SDK-decoded response was archived.
// The synchronous SDK cannot cancel an in-flight native request.
func (c Collector) CollectCSD(ctx context.Context, codes, indicators, from, to, options string) (Cube, ArchiveRef, error) {
	if c.Fetcher == nil || c.Store == nil {
		return Cube{}, ArchiveRef{}, errors.New("emquant: fetcher and raw store are required")
	}
	if err := ctx.Err(); err != nil {
		return Cube{}, ArchiveRef{}, err
	}
	cube, err := c.Fetcher.CSD(codes, indicators, from, to, options)
	if err != nil {
		return Cube{}, ArchiveRef{}, err
	}
	ref, err := c.persist(ctx, request{Method: "csd", Codes: codes, Indicators: indicators, From: from, To: to, Options: options}, cube, time.Now())
	if err != nil {
		return Cube{}, ArchiveRef{}, err
	}
	return cube, ref, nil
}

// CollectQuote archives the quote before exposing it for validation or mapping.
func (c Collector) CollectQuote(ctx context.Context, codes, indicators, options string) (Quote, ArchiveRef, error) {
	if c.Fetcher == nil || c.Store == nil {
		return Quote{}, ArchiveRef{}, errors.New("emquant: fetcher and raw store are required")
	}
	if err := ctx.Err(); err != nil {
		return Quote{}, ArchiveRef{}, err
	}
	quote, err := c.Fetcher.QuoteSnapshot(codes, indicators, options)
	if err != nil {
		return Quote{}, ArchiveRef{}, err
	}
	if quote.ObservedAt.IsZero() {
		return Quote{}, ArchiveRef{}, errors.New("emquant: quote missing observation time")
	}
	ref, err := c.persist(ctx, request{Method: "csqsnapshot", Codes: codes, Indicators: indicators, Options: options}, quote.Data, quote.ObservedAt)
	if err != nil {
		return Quote{}, ArchiveRef{}, err
	}
	return quote, ref, nil
}
