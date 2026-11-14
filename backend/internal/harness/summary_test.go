package harness

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

// summaryAdapter paraphrases claims in the summary step and lets the summary verifier reject chosen points.
// Every other call goes to the deterministic simulator.
type summaryAdapter struct {
	reject   map[string]bool
	verified *atomic.Int32
}

func (summaryAdapter) Name() string { return "sim" }

var claimLine = regexp.MustCompile(`(S\d+\.\d+)（[a-z]+）：(.+)`)

func (a summaryAdapter) Do(ctx context.Context, c model.Call) (model.Raw, error) {
	var out any
	switch c.Messages[0].Content {
	case summaryRules:
		var pts []map[string]any
		for _, m := range claimLine.FindAllStringSubmatch(c.Messages[1].Content, -1) {
			if len(pts) == 4 {
				break
			}
			pts = append(pts, map[string]any{"text": "要点：" + m[2], "claim_refs": []string{m[1]}})
		}
		out = map[string]any{"points": pts}
	case summaryVerifyRules:
		a.verified.Add(1)
		var vs []map[string]string
		for _, ref := range regexp.MustCompile(`要点 (S1\.\d+)：`).FindAllStringSubmatch(c.Messages[1].Content, -1) {
			v := "supported"
			if a.reject[ref[1]] {
				v = "unsupported"
			}
			vs = append(vs, map[string]string{"claim_ref": ref[1], "verdict": v, "reason": "", "rewrite": "", "suggested_kind": "none"})
		}
		out = map[string]any{"verdicts": vs}
	default:
		return model.Sim{}.Do(ctx, c)
	}
	b, _ := json.Marshal(out)
	return model.Raw{Content: string(b), FinishReason: "stop", ObservedModel: "vera-sim-1"}, nil
}

func summaryOf(t *testing.T, reject map[string]bool) (points []string, verifierCalls int32) {
	t.Helper()
	e, pool := testEngine(t)
	var n atomic.Int32
	e.Gateway.Adapters["sim"] = summaryAdapter{reject: reject, verified: &n}
	ctx := context.Background()
	ws := workspace(t, pool)
	p := plan(t, e, ws, "stock_analysis", "DM0001.DEMO")
	id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Run(ctx, ws, id, 1); err != nil {
		t.Fatal(err)
	}
	if s, _ := status(t, e, ws, id); s != "COMPLETED" {
		t.Fatalf("status %s", s)
	}
	var doc Document
	_ = db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT document FROM report_versions WHERE task_id=$1`, id).Scan(&doc)
	})
	for _, c := range doc.Claims {
		if strings.HasPrefix(c.Ref, "S1.") {
			points = append(points, c.Text)
		}
	}
	return points, n.Load()
}

func TestSummaryPointsAreSemanticallyVerified(t *testing.T) {
	pts, calls := summaryOf(t, map[string]bool{"S1.2": true})
	if calls != 1 || len(pts) != 3 {
		t.Fatalf("verifier calls %d, points %v", calls, pts)
	}
	for _, p := range pts {
		if !strings.HasPrefix(p, "要点：") {
			t.Fatalf("expected the three supported paraphrases, got %v", pts)
		}
	}
	// Too few supported points: the deterministic fallback quotes verified claims verbatim.
	pts, _ = summaryOf(t, map[string]bool{"S1.1": true, "S1.2": true})
	if len(pts) < 3 {
		t.Fatalf("fallback summary %v", pts)
	}
	for _, p := range pts {
		if strings.HasPrefix(p, "要点：") {
			t.Fatalf("unverified paraphrase delivered after fallback: %v", pts)
		}
	}
}
