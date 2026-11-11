package harness

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/check"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

const root = "../../../"

func testEngine(t *testing.T) (*Engine, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.New(t)
	ctx := context.Background()
	p, err := fixture.New(root + "tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog(root + "contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	st := dataservice.NewStore(pool, cat)
	if _, err := st.Sync(ctx, p); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(root + "contracts/examples/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	sc, err := model.LoadSchemas(root + "contracts/model")
	if err != nil {
		t.Fatal(err)
	}
	profiles := map[string]model.Profile{}
	for _, n := range []string{"writer", "verifier", "router"} {
		profiles[n] = model.Profile{Name: n, Adapter: "sim", ModelID: "vera-sim-1"}
	}
	gw := model.NewGateway(pool, profiles, map[string]model.Adapter{"sim": model.Sim{}}, nil, sc, pol, decimal.RequireFromString("30"))
	e := &Engine{Pool: pool, Store: st, Gateway: gw, Policy: pol, Skills: Skills(), Now: p.Now, Synthetic: true, Basis: p.Basis()}
	if _, err := e.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}
	return e, pool
}

func workspace(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	var uid, ws uuid.UUID
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO users(issuer,subject) VALUES('test',$1) RETURNING id`, uuid.NewString()).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) RETURNING id`, uid).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

type fixtureCase struct {
	ID        string `json:"id"`
	Skill     string `json:"skill"`
	Subject   string `json:"subject"`
	Mode      string `json:"mode"`
	ExpStatus string `json:"expect_status"`
	ExpKind   string `json:"expect_result_kind"`
	ExpPlan   string `json:"expect_plan"`
	Notes     string `json:"notes"`
}

func TestFixtureCases(t *testing.T) {
	e, pool := testEngine(t)
	ctx := context.Background()
	raw, _ := os.ReadFile(root + "tests/fixtures/demo-universe.json")
	var m struct {
		Cases []fixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, c := range m.Cases {
		t.Run(c.ID, func(t *testing.T) {
			ws := workspace(t, pool)
			subject, err := e.Store.EntityByCode(ctx, c.Subject)
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]any{}
			if c.Mode != "" {
				params["mode"] = c.Mode
			}
			plan, err := e.BuildPlan(ctx, ws, c.Skill, subject, params)
			if err != nil {
				t.Fatal(err)
			}
			if c.ExpPlan == "not_executable" {
				if plan.Executable {
					t.Fatalf("plan should not be executable: %+v", plan.Sections)
				}
				return
			}
			if !plan.Executable {
				t.Fatalf("plan not executable: %+v", plan.Sections)
			}
			id, err := e.CreateTask(ctx, ws, plan.ID, plan.Hash, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.Run(ctx, ws, id, 1); err != nil {
				t.Fatal(err)
			}
			var status string
			var code, msg *string
			var kind *string
			var doc []byte
			err = db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT status, reason_code, reason_message FROM research_tasks WHERE id=$1`, id).Scan(&status, &code, &msg); err != nil {
					return err
				}
				err := tx.QueryRow(ctx, `SELECT result_kind, document FROM report_versions WHERE task_id=$1`, id).Scan(&kind, &doc)
				if err == pgx.ErrNoRows {
					return nil
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, sec := range sectionsOf(doc) {
				t.Log(sec)
			}
			if status != c.ExpStatus {
				t.Fatalf("status %s (%v %v) want %s", status, deref(code), deref(msg), c.ExpStatus)
			}
			if kind == nil || *kind != c.ExpKind {
				t.Fatalf("result_kind %v want %s; doc=%s", deref(kind), c.ExpKind, clip(string(doc), 1500))
			}
			var d Document
			_ = json.Unmarshal(doc, &d)
			if !d.Synthetic || len(d.SummaryRefs) < 3 || len(d.Claims) < 8 {
				t.Fatalf("weak report: synthetic=%v summary=%d claims=%d", d.Synthetic, len(d.SummaryRefs), len(d.Claims))
			}
			for _, cl := range d.Claims {
				if f := check.Forbidden(cl.Text); f != nil || strings.Contains(cl.Text, "88 元") {
					t.Fatalf("forbidden content in %s: %s (%v)", cl.Ref, cl.Text, f)
				}
				if len(cl.EvidenceRefs) == 0 {
					t.Fatalf("claim %s without evidence", cl.Ref)
				}
			}
			var removed, kept int
			_ = db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE verdict='removed'), count(*) FILTER (WHERE verdict='kept') FROM claims WHERE task_id=$1`, id).Scan(&removed, &kept)
			})
			_ = db.InWorkspace(ctx, pool, ws, func(tx pgx.Tx) error {
				rows, _ := tx.Query(ctx, `SELECT claim_ref, text, removed_reason FROM claims WHERE task_id=$1 AND verdict='removed'`, id)
				for rows.Next() {
					var a, b, r string
					rows.Scan(&a, &b, &r)
					t.Logf("removed %s: %s ← %s", a, b, r)
				}
				return nil
			})
			t.Logf("%s: %s %s, %d sections, %d claims kept, %d removed", c.ID, status, *kind, len(d.Sections), kept, removed)
		})
	}
}

func sectionsOf(doc []byte) []string {
	var d Document
	_ = json.Unmarshal(doc, &d)
	var out []string
	for _, s := range d.Sections {
		out = append(out, s.ID+" "+s.Status+" "+deref(s.Note))
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
