// Package audit appends audit events; failures are logged, never silently dropped.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Record writes an audit event inside tx.
func Record(ctx context.Context, tx pgx.Tx, ws *uuid.UUID, actor, action, resource, result string, detail map[string]any) error {
	if detail == nil {
		detail = map[string]any{}
	}
	d, _ := json.Marshal(detail)
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,actor,action,resource,result,detail) VALUES($1,$2,$3,$4,$5,$6)`,
		ws, actor, action, resource, result, d)
	if err != nil {
		slog.ErrorContext(ctx, "audit write failed", "action", action, "err", err)
	}
	return err
}
