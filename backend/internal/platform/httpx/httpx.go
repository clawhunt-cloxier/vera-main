// Package httpx holds JSON helpers, the error envelope and request-id middleware.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
)

type ctxKey int

const requestIDKey ctxKey = 1

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		id := hex.EncodeToString(b)
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// JSON writes v with status; maps add request_id when it is a map[string]any.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes the API error envelope and logs internal causes without leaking them.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	e := errs.As(err)
	if e.Status >= 500 {
		slog.ErrorContext(r.Context(), "request failed", "request_id", RequestID(r.Context()), "path", r.URL.Path, "err", err)
	}
	body := map[string]any{"code": e.Code, "message": e.Message, "retryable": e.Retryable}
	if len(e.Details) > 0 {
		body["details"] = e.Details
	}
	JSON(w, r, e.Status, map[string]any{"request_id": RequestID(r.Context()), "error": body})
}

// Decode reads a JSON body (max 64 KiB) rejecting unknown fields.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errs.BadRequest("INVALID_BODY", "请求格式错误").Wrap(err)
	}
	if dec.More() {
		return errs.BadRequest("INVALID_BODY", "请求格式错误").Wrap(errors.New("trailing data"))
	}
	return nil
}
