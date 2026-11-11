package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
)

const harnessUsage = `vera harness <command>

  eval [--cases tests/fixtures/demo-universe.json] [--only SA-01,...]
        按评测集逐个运行技能（使用当前配置的模型 profile：sim/replay/openai），输出状态、结论存活率、调用数与费用；
        与期望不符时退出码为 1。
  replay --task <id>
        用原任务的技能、对象、参数与数据截止时点在评测 workspace 中重跑，并对比结论数量与结果类型。
`

type evalCase struct {
	ID        string `json:"id"`
	Skill     string `json:"skill"`
	Subject   string `json:"subject"`
	Mode      string `json:"mode"`
	ExpStatus string `json:"expect_status"`
	ExpKind   string `json:"expect_result_kind"`
	ExpPlan   string `json:"expect_plan"`
}

// evalWorkspace returns (creating once) the dedicated workspace used by harness runs.
func evalWorkspace(ctx context.Context, a *app.App) (uuid.UUID, error) {
	var ws uuid.UUID
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		var uid uuid.UUID
		if err := tx.QueryRow(ctx, `INSERT INTO users(issuer,subject,display_name) VALUES('vera-harness','eval','评测') ON CONFLICT (issuer,subject)
			DO UPDATE SET display_name=excluded.display_name RETURNING id`).Scan(&uid); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO workspaces(owner_user_id) VALUES($1) ON CONFLICT (owner_user_id) DO UPDATE SET owner_user_id=excluded.owner_user_id
			RETURNING id`, uid).Scan(&ws)
	})
	return ws, err
}

type runResult struct {
	status, kind, code string
	kept, removed      int
	calls              int
	cost               string
	took               time.Duration
}

func runOnce(ctx context.Context, a *app.App, ws uuid.UUID, skill, code string, params map[string]any) (bool, runResult, error) {
	var r runResult
	sub, err := a.Store.EntityByCode(ctx, code)
	if err != nil {
		return false, r, fmt.Errorf("subject %s: %w", code, err)
	}
	start := time.Now()
	p, err := a.Engine.BuildPlan(ctx, ws, skill, sub, params)
	if err != nil {
		return false, r, err
	}
	if !p.Executable {
		return false, r, nil
	}
	a.Engine.Policy.Quota.UserDailyTasks, a.Engine.Policy.Quota.UserDailyDeepResearch = 1<<20, 1<<20 // eval workspace is not user quota
	id, err := a.Engine.CreateTask(ctx, ws, p.ID, p.Hash, nil)
	if err != nil {
		return true, r, err
	}
	if err := a.Engine.Run(ctx, ws, id, 1); err != nil {
		return true, r, err
	}
	r.took = time.Since(start)
	err = db.InWorkspace(ctx, a.Pool, ws, func(tx pgx.Tx) error {
		var c *string
		if err := tx.QueryRow(ctx, `SELECT status, reason_code FROM research_tasks WHERE id=$1`, id).Scan(&r.status, &c); err != nil {
			return err
		}
		if c != nil {
			r.code = *c
		}
		_ = tx.QueryRow(ctx, `SELECT result_kind FROM report_versions WHERE task_id=$1`, id).Scan(&r.kind)
		_ = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE verdict='kept'), count(*) FILTER (WHERE verdict='removed') FROM claims WHERE task_id=$1`, id).Scan(&r.kept, &r.removed)
		return tx.QueryRow(ctx, `SELECT count(*), coalesce(sum(coalesce(cost_usd, reserved_usd)),0)::text FROM model_calls WHERE task_id=$1`, id).Scan(&r.calls, &r.cost)
	})
	return true, r, err
}

func harnessCmd(ctx context.Context, a *app.App, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(harnessUsage)
		return nil
	}
	// CreateTask enqueues River jobs in its transaction, so the engine needs an insert-only client.
	// Runs below execute synchronously via Engine.Run; no worker is started here.
	if _, err := a.Engine.NewRiver(a.Pool, false, 0); err != nil {
		return err
	}
	ws, err := evalWorkspace(ctx, a)
	if err != nil {
		return err
	}
	switch args[0] {
	case "eval":
		fs := flag.NewFlagSet("eval", flag.ContinueOnError)
		path := fs.String("cases", a.Cfg.FixtureManifest, "")
		only := fs.String("only", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		b, err := os.ReadFile(*path)
		if err != nil {
			return err
		}
		var m struct {
			Cases []evalCase `json:"cases"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		w := tw()
		fmt.Fprintln(w, "用例\t技能\t对象\t结果\t期望\t保留/删除结论\t存活率\t调用\t费用USD\t耗时")
		failed := 0
		for _, c := range m.Cases {
			if *only != "" && !contains(*only, c.ID) {
				continue
			}
			params := map[string]any{}
			if c.Mode != "" {
				params["mode"] = c.Mode
			}
			exec, r, err := runOnce(ctx, a, ws, c.Skill, c.Subject, params)
			got, want := r.status+" "+r.kind, c.ExpStatus+" "+c.ExpKind
			if c.ExpPlan == "not_executable" {
				got, want = "executable", "not_executable"
				if !exec {
					got = "not_executable"
				}
			}
			if err != nil {
				got = "ERROR " + err.Error()
			}
			mark := ""
			if got != want {
				mark, failed = " ✗", failed+1
			}
			rate := "-"
			if r.kept+r.removed > 0 {
				rate = fmt.Sprintf("%.0f%%", 100*float64(r.kept)/float64(r.kept+r.removed))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\t%s\t%d/%d\t%s\t%d\t%s\t%s\n", c.ID, c.Skill, c.Subject, got, mark, want, r.kept, r.removed, rate,
				r.calls, r.cost, r.took.Round(time.Millisecond))
		}
		_ = w.Flush()
		if failed > 0 {
			return fmt.Errorf("%d 个用例与期望不符", failed)
		}
		return nil
	case "replay":
		fs := flag.NewFlagSet("replay", flag.ContinueOnError)
		task := fs.String("task", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var skill, code, kind string
		var params []byte
		var asOf time.Time
		var kept int
		if err := a.Pool.QueryRow(ctx, `SELECT t.skill_id, e.code, t.params, t.as_of, coalesce(r.result_kind,''),
			(SELECT count(*) FROM claims c WHERE c.task_id=t.id AND c.verdict='kept') FROM research_tasks t JOIN entities e ON e.id=t.subject_id
			LEFT JOIN report_versions r ON r.task_id=t.id WHERE t.id=$1`, *task).Scan(&skill, &code, &params, &asOf, &kind, &kept); err != nil {
			return err
		}
		raw := map[string]any{}
		_ = json.Unmarshal(params, &raw) // harness.Params JSON uses the public parameter names
		if skill != "deep_research" {
			delete(raw, "mode")
			delete(raw, "focus")
		} else {
			delete(raw, "window")
		}
		a.Engine.Now = func() time.Time { return asOf }
		_, r, err := runOnce(ctx, a, ws, skill, code, raw)
		if err != nil {
			return err
		}
		fmt.Printf("原任务：%s %s，保留结论 %d\n重跑：  %s %s，保留结论 %d，删除 %d，调用 %d，费用 %s USD\n", kind, skill, kept, r.status, r.kind, r.kept, r.removed, r.calls, r.cost)
		if r.kind != kind {
			return errors.New("结果类型与原任务不同")
		}
		return nil
	}
	fmt.Print(harnessUsage)
	return errors.New("未知命令")
}

func contains(list, id string) bool {
	for _, x := range splitComma(list) {
		if x == id {
			return true
		}
	}
	return false
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, c := range s + "," {
		if c == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	return out
}
