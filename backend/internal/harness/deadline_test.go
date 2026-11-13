package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

// slowSection never answers the writer of one section; the call ends only when the run's deadline cancels it.
type slowSection struct{ section string }

func (slowSection) Name() string { return "sim" }

func (a slowSection) Do(ctx context.Context, c model.Call) (model.Raw, error) {
	if strings.Contains(c.Messages[len(c.Messages)-1].Content, "请撰写章节 "+a.section+"「") {
		<-ctx.Done()
		return model.Raw{}, &model.Error{Transient: true, Unknown: true, Msg: "model request failed: " + ctx.Err().Error()}
	}
	return model.Sim{}.Do(ctx, c)
}

// HARNESS §10.1: at the deadline, if every required section is valid, the report is delivered as limited with the
// unfinished optional section marked; it is not abstained.
func TestDeadlineWithRequiredSectionsDoneDeliversLimited(t *testing.T) {
	e, pool := testEngine(t)
	e.Gateway.Adapters["sim"] = slowSection{section: "S9"}
	ctx := context.Background()
	ws := workspace(t, pool)
	p := plan(t, e, ws, "stock_analysis", "DM0001.DEMO")
	id, err := e.CreateTask(ctx, ws, p.ID, p.Hash, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Run stops model work 30s before the deadline; leave it a few seconds for the other sections.
	if err := db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE research_tasks SET deadline = now() + interval '40 seconds' WHERE id=$1`, id)
		return err
	}); err != nil {
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
	if doc.ResultKind != "limited" {
		t.Fatalf("result kind %s", doc.ResultKind)
	}
	for _, s := range doc.Sections {
		if s.ID == "S9" && (s.Status != "omitted" || s.Note == nil) {
			t.Fatalf("S9 should be omitted with a note: %+v", s)
		}
	}
}
