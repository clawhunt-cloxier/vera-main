package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/fixture"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

const root = "../../../"

type adminRig struct {
	t    *testing.T
	a    *app.App
	pool *pgxpool.Pool
}

func newAdmin(t *testing.T) *adminRig {
	pool := dbtest.New(t)
	p, err := fixture.New(root + "tests/fixtures/demo-universe.json")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := dataservice.LoadCatalog(root + "contracts/datasets/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	return &adminRig{t: t, pool: pool, a: &app.App{Pool: pool, Store: dataservice.NewStore(pool, cat), Primary: p}}
}

func (r *adminRig) run(args ...string) (string, error) {
	var out bytes.Buffer
	err := adminTo(context.Background(), r.a, args, &out, "vera-admin:test")
	return out.String(), err
}

// lastAudit returns result and detail error of the newest audit event for action.
func (r *adminRig) lastAudit(action string) (result, detail string) {
	r.t.Helper()
	err := r.pool.QueryRow(context.Background(), `SELECT result, detail::text FROM audit_events WHERE action=$1 ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		action).Scan(&result, &detail)
	if err != nil {
		r.t.Fatalf("no audit event for %s: %v", action, err)
	}
	return
}

func (r *adminRig) user(email string) uuid.UUID {
	var id uuid.UUID
	ctx := context.Background()
	if err := r.pool.QueryRow(ctx, `INSERT INTO users(issuer,subject,email) VALUES('test',$1,$1) RETURNING id`, email).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.pool.Exec(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1)`, id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

func TestAdminEveryCommandIsAudited(t *testing.T) {
	r := newAdmin(t)
	out, err := r.run("invite", "create", "--count", "2", "--note", "pilot")
	if err != nil {
		t.Fatal(err)
	}
	if res, d := r.lastAudit("invite.create"); res != "success" || strings.Contains(d, strings.Fields(out)[len(strings.Fields(out))-1]) {
		t.Fatalf("invite.create audit %s %s (codes must not be audited)", res, d)
	}
	if _, err := r.run("invite", "list"); err != nil {
		t.Fatal(err)
	}
	if res, _ := r.lastAudit("invite.list"); res != "success" {
		t.Fatal("read command not audited")
	}
	if _, err := r.run("invite", "revoke", "not-a-uuid"); err == nil {
		t.Fatal("bad id accepted")
	}
	if res, d := r.lastAudit("invite.revoke"); res != "failure" || !strings.Contains(d, "无效") {
		t.Fatalf("failed command audit %s %s", res, d)
	}
	if _, err := r.run("bogus"); err == nil {
		t.Fatal("unknown command accepted")
	}
}

func TestAdminUsers(t *testing.T) {
	r := newAdmin(t)
	ctx := context.Background()
	id := r.user("alice@example.invalid")
	if _, err := r.pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,csrf_token,expires_at) VALUES('h',$1,'c',now()+interval '1 hour')`, id); err != nil {
		// T013 replaces csrf_token by csrf_hash; accept either schema.
		if _, err := r.pool.Exec(ctx, `INSERT INTO sessions(id_hash,user_id,csrf_hash,expires_at) VALUES('h',$1,'c',now()+interval '1 hour')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.run("user", "disable", id.String()); err != nil {
		t.Fatal(err)
	}
	var live int
	_ = r.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL`).Scan(&live)
	if live != 0 {
		t.Fatal("disable did not revoke sessions")
	}
	if _, err := r.run("user", "disable", uuid.NewString()); err == nil {
		t.Fatal("disabling an unknown user succeeded")
	}
	if _, err := r.run("user", "reset-password", id.String()); !errors.Is(err, errNotImplemented) {
		t.Fatalf("reset-password must report not implemented, got %v", err)
	}
	if res, _ := r.lastAudit("user.reset-password"); res != "failure" {
		t.Fatal("reset-password failure not audited")
	}
	if _, err := r.run("quota", "set", "--user", id.String(), "--daily", "3"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("quota", "set", "--user", id.String(), "--deep", "1"); err != nil {
		t.Fatal(err)
	}
	var q string
	_ = r.pool.QueryRow(ctx, `SELECT quota_override::text FROM workspaces WHERE owner_user_id=$1`, id).Scan(&q)
	if !strings.Contains(q, `"user_daily_tasks": 3`) || !strings.Contains(q, `"user_daily_deep_research": 1`) {
		t.Fatalf("quota override not merged: %s", q)
	}
}

func TestAdminDataSyncFiltersAndProviderTest(t *testing.T) {
	r := newAdmin(t)
	ctx := context.Background()
	recs, _ := r.a.Primary.Records(ctx)
	ds := recs[0].DatasetID
	if _, err := r.run("data", "sync", "--dataset", "no.such.dataset"); err == nil {
		t.Fatal("unknown dataset accepted")
	}
	if _, err := r.run("data", "sync", "--dataset", ds); err != nil {
		t.Fatal(err)
	}
	var sets []string
	rows, _ := r.pool.Query(ctx, `SELECT DISTINCT dataset_id FROM records`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		sets = append(sets, s)
	}
	if len(sets) != 1 || sets[0] != ds {
		t.Fatalf("filtered sync wrote datasets %v, want only %s", sets, ds)
	}
	if res, d := r.lastAudit("data.sync"); res != "success" || !strings.Contains(d, `"inserted"`) {
		t.Fatalf("sync audit %s %s", res, d)
	}
	if out, err := r.run("provider", "test", r.a.Primary.ID()); err != nil || !strings.Contains(out, "登记能力") {
		t.Fatalf("provider test: %v %s", err, out)
	}
	if _, err := r.run("provider", "test", "other"); err == nil {
		t.Fatal("testing an unconfigured provider succeeded")
	}
	if _, err := r.run("provider", "test", "tushare", "--research"); err == nil {
		t.Fatal("TuShare research probes ran in fixture mode")
	}
	if _, err := r.run("provider", "test", r.a.Primary.ID(), "--wrong"); err == nil {
		t.Fatal("invalid probe mode was ignored")
	}
}

func TestAdminUsageReportAndReconcile(t *testing.T) {
	r := newAdmin(t)
	ctx := context.Background()
	alice, bob := r.user("alice@example.invalid"), r.user("bob@example.invalid")
	ws := func(u uuid.UUID) uuid.UUID {
		var id uuid.UUID
		_ = r.pool.QueryRow(ctx, `SELECT id FROM workspaces WHERE owner_user_id=$1`, u).Scan(&id)
		return id
	}
	call := func(w uuid.UUID, status, at string, reserved, cost string) uuid.UUID {
		var id uuid.UUID
		var c any
		if cost != "" {
			c = cost
		}
		err := r.pool.QueryRow(ctx, `INSERT INTO model_calls(workspace_id,step_key,attempt,profile,adapter,model_id,request_hash,status,reserved_usd,cost_usd,usage,sent_at)
			VALUES($1,'plan',1,'router','openai','m','h',$2,$3::numeric,$4::numeric,'{"prompt_tokens":10,"completion_tokens":5}',$5::timestamptz) RETURNING id`,
			w, status, reserved, c, at).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// 2026-11-07 23:30 UTC is 2026-11-08 07:30 in Asia/Shanghai.
	call(ws(alice), "SUCCEEDED", "2026-11-07T10:00:00Z", "1", "0.25")
	unk := call(ws(alice), "UNKNOWN", "2026-11-07T23:30:00Z", "2", "")
	call(ws(bob), "SUCCEEDED", "2026-11-07T12:00:00Z", "1", "-1") // pricing failed: counts at reservation
	out, err := r.run("usage", "report", "--day", "2026-11-07")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "alice@example.invalid") || !strings.Contains(out, "bob@example.invalid") || !strings.Contains(out, "3.250000") {
		t.Fatalf("UTC report:\n%s", out)
	}
	out, _ = r.run("usage", "report", "--day", "2026-11-07", "--tz", "Asia/Shanghai")
	if !strings.Contains(out, "1.250000") {
		t.Fatalf("Shanghai day must exclude the 07:30 call:\n%s", out)
	}
	if _, err := r.run("usage", "reconcile", "--release", unk.String(), "--cost", "-1"); err == nil {
		t.Fatal("negative cost accepted")
	}
	if _, err := r.run("usage", "reconcile", "--release", unk.String(), "--cost", "0.4"); err != nil {
		t.Fatal(err)
	}
	var st, c string
	_ = r.pool.QueryRow(ctx, `SELECT status, cost_usd::text FROM model_calls WHERE id=$1`, unk).Scan(&st, &c)
	if st != "RECONCILED" || !strings.HasPrefix(c, "0.4") {
		t.Fatalf("reconcile %s %s", st, c)
	}
	if _, err := r.run("usage", "reconcile", "--release", unk.String(), "--cost", "0.5"); err == nil {
		t.Fatal("manually reconciled call reconciled twice")
	}
	private := call(ws(alice), "UNKNOWN", "2026-11-07T10:00:00Z", "1", "")
	if _, err := r.pool.Exec(ctx, `UPDATE model_calls SET status='RECONCILED', cost_usd=0,
		content_erased_at=now(), auto_released_at=now() WHERE id=$1`, private); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("usage", "reconcile", "--release", private.String(), "--cost", "0.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("usage", "reconcile", "--release", private.String(), "--cost", "0.4"); err == nil {
		t.Fatal("released private call reconciled twice")
	}
	if _, err := r.run("task", "inspect", uuid.NewString()); err == nil {
		t.Fatal("inspect of a missing task succeeded")
	}
}
