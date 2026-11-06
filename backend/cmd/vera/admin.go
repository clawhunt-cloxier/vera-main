package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/audit"
)

const adminUsage = `vera admin <command>

  invite create [--count N] [--uses N] [--expires 14d] [--note 文本]   生成邀请码（只显示一次）
  invite list | invite revoke <id>
  user list | user disable <id> | user enable <id>
  quota set --user <id> [--active N] [--queued N] [--daily N] [--deep N]
  provider list
  data sync                         按配置的数据提供方同步
  usage report [--day 2026-11-07]   当日模型调用、token、费用（按技能汇总）
  usage reconcile [--release <call-id>]   列出 UNKNOWN 调用 / 核销一条
  kill-switch on|off                停止/恢复接受新研究任务（在途任务继续）
  task inspect <id>                 步骤台账、模型调用与被删除的结论
`

func tw() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0) }

func days(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(s)
}

func record(ctx context.Context, a *app.App, action, resource string, detail map[string]any) error {
	return pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, nil, "vera-admin:"+os.Getenv("USER"), action, resource, "success", detail)
	})
}

func admin(ctx context.Context, a *app.App, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Print(adminUsage)
		return nil
	}
	sub := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	switch args[0] + " " + sub(1) {
	case "invite create":
		fs := flag.NewFlagSet("invite create", flag.ContinueOnError)
		count := fs.Int("count", 1, "")
		uses := fs.Int("uses", 1, "")
		exp := fs.String("expires", "14d", "")
		note := fs.String("note", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ttl, err := days(*exp)
		if err != nil || *count < 1 || *count > 100 || *uses < 1 {
			return errors.New("参数无效")
		}
		codes, err := identity.CreateInvites(ctx, a.Pool, *count, *uses, ttl, *note, "vera-admin:"+os.Getenv("USER"))
		if err != nil {
			return err
		}
		fmt.Printf("已生成 %d 个邀请码（只显示这一次，库中仅存哈希）：\n", len(codes))
		for _, c := range codes {
			fmt.Println(c)
		}
	case "invite list":
		rows, err := a.Pool.Query(ctx, `SELECT id, remaining, note, expires_at, revoked_at IS NOT NULL FROM invites ORDER BY created_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tw()
		fmt.Fprintln(w, "ID\t剩余\t备注\t过期\t作废")
		for rows.Next() {
			var id uuid.UUID
			var n int
			var note string
			var exp time.Time
			var rev bool
			if err := rows.Scan(&id, &n, &note, &exp, &rev); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%v\n", id, n, note, exp.Format("2006-01-02"), rev)
		}
		return w.Flush()
	case "invite revoke":
		tag, err := a.Pool.Exec(ctx, `UPDATE invites SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, sub(2))
		if err != nil || tag.RowsAffected() == 0 {
			return fmt.Errorf("邀请码不存在或已作废: %v", err)
		}
		return record(ctx, a, "invite.revoke", "invite:"+sub(2), nil)
	case "user list":
		rows, err := a.Pool.Query(ctx, `SELECT u.id, u.email, u.display_name, u.persona, w.id IS NOT NULL, u.disabled_at IS NOT NULL, u.created_at
			FROM users u LEFT JOIN workspaces w ON w.owner_user_id=u.id ORDER BY u.created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tw()
		fmt.Fprintln(w, "ID\t邮箱\t名称\t身份标签\t已开通\t停用\t注册")
		for rows.Next() {
			var id uuid.UUID
			var email, name, persona string
			var ws, dis bool
			var at time.Time
			if err := rows.Scan(&id, &email, &name, &persona, &ws, &dis, &at); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%v\t%s\n", id, email, name, persona, ws, dis, at.Format("2006-01-02"))
		}
		return w.Flush()
	case "user disable", "user enable":
		q := `UPDATE users SET disabled_at=now() WHERE id=$1`
		if sub(1) == "enable" {
			q = `UPDATE users SET disabled_at=NULL WHERE id=$1`
		}
		if _, err := a.Pool.Exec(ctx, q, sub(2)); err != nil {
			return err
		}
		if sub(1) == "disable" {
			if _, err := a.Pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, sub(2)); err != nil {
				return err
			}
		}
		fmt.Println("完成。密码重置请在身份服务（Keycloak）管理台操作。")
		return record(ctx, a, "user."+sub(1), "user:"+sub(2), nil)
	case "quota set":
		fs := flag.NewFlagSet("quota set", flag.ContinueOnError)
		user := fs.String("user", "", "")
		o := map[string]*int{"user_active_tasks": fs.Int("active", -1, ""), "user_queued_tasks": fs.Int("queued", -1, ""),
			"user_daily_tasks": fs.Int("daily", -1, ""), "user_daily_deep_research": fs.Int("deep", -1, "")}
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		set := map[string]int{}
		for k, v := range o {
			if *v >= 0 {
				set[k] = *v
			}
		}
		b, _ := json.Marshal(set)
		tag, err := a.Pool.Exec(ctx, `UPDATE workspaces SET quota_override=$2 WHERE owner_user_id=$1`, *user, b)
		if err != nil || tag.RowsAffected() == 0 {
			return fmt.Errorf("用户不存在或尚未开通: %v", err)
		}
		return record(ctx, a, "quota.set", "user:"+*user, map[string]any{"override": set})
	case "provider list":
		rows, err := a.Pool.Query(ctx, `SELECT p.id, p.name, p.kind, p.synthetic, p.allow_model_processing, p.enabled,
			(SELECT count(*) FROM records r WHERE r.provider_id=p.id), (SELECT max(available_at) FROM records r WHERE r.provider_id=p.id) FROM providers p`)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tw()
		fmt.Fprintln(w, "ID\t名称\t类型\t模拟\t允许模型处理\t启用\t记录数\t最新可用时点")
		for rows.Next() {
			var id, name, kind string
			var syn, amp, en bool
			var n int64
			var last *time.Time
			if err := rows.Scan(&id, &name, &kind, &syn, &amp, &en, &n, &last); err != nil {
				return err
			}
			l := "-"
			if last != nil {
				l = last.Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%v\t%v\t%d\t%s\n", id, name, kind, syn, amp, en, n, l)
		}
		return w.Flush()
	case "data sync":
		return a.Sync(ctx)
	case "usage report":
		fs := flag.NewFlagSet("usage report", flag.ContinueOnError)
		day := fs.String("day", time.Now().UTC().Format("2006-01-02"), "")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		rows, err := a.Pool.Query(ctx, `SELECT coalesce(t.skill_id, m.step_key), m.profile, m.adapter, count(*),
			count(*) FILTER (WHERE m.status='SUCCEEDED'), count(*) FILTER (WHERE m.status='UNKNOWN'),
			coalesce(sum((m.usage->>'prompt_tokens')::bigint),0), coalesce(sum((m.usage->>'completion_tokens')::bigint),0),
			coalesce(sum(CASE WHEN m.status IN ('SENT','UNKNOWN') THEN m.reserved_usd ELSE m.cost_usd END),0)::text
			FROM model_calls m LEFT JOIN research_tasks t ON t.id=m.task_id
			WHERE m.sent_at >= $1::date AND m.sent_at < $1::date + 1 GROUP BY 1,2,3 ORDER BY 1,2`, *day)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tw()
		fmt.Fprintln(w, "技能/步骤\tprofile\tadapter\t调用\t成功\t未知\t输入token\t输出token\t费用USD")
		for rows.Next() {
			var k, p, ad, cost string
			var n, ok, unk, in, out int64
			if err := rows.Scan(&k, &p, &ad, &n, &ok, &unk, &in, &out, &cost); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", k, p, ad, n, ok, unk, in, out, cost)
		}
		fmt.Fprintf(w, "\n平台日费用上限\t%s USD\n", a.Engine.Gateway.DailyLimit)
		return w.Flush()
	case "usage reconcile":
		fs := flag.NewFlagSet("usage reconcile", flag.ContinueOnError)
		rel := fs.String("release", "", "")
		cost := fs.String("cost", "0", "按厂商账单确认的实际费用")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *rel != "" {
			tag, err := a.Pool.Exec(ctx, `UPDATE model_calls SET status='RECONCILED', cost_usd=$2::numeric WHERE id=$1 AND status='UNKNOWN'`, *rel, *cost)
			if err != nil || tag.RowsAffected() == 0 {
				return fmt.Errorf("调用不存在或不是 UNKNOWN: %v", err)
			}
			return record(ctx, a, "usage.reconcile", "model_call:"+*rel, map[string]any{"cost_usd": *cost})
		}
		rows, err := a.Pool.Query(ctx, `SELECT id, task_id, step_key, profile, reserved_usd::text, sent_at, coalesce(error,'') FROM model_calls
			WHERE status='UNKNOWN' ORDER BY sent_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		w := tw()
		fmt.Fprintln(w, "调用ID\t任务\t步骤\tprofile\t预留USD\t发送时间\t错误")
		for rows.Next() {
			var id uuid.UUID
			var task *uuid.UUID
			var step, prof, res, e string
			var at time.Time
			if err := rows.Scan(&id, &task, &step, &prof, &res, &at, &e); err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\t%v\t%s\t%s\t%s\t%s\t%s\n", id, task, step, prof, res, at.Format(time.RFC3339), e)
		}
		return w.Flush()
	case "kill-switch on", "kill-switch off":
		on := sub(1) == "on"
		if _, err := a.Pool.Exec(ctx, `INSERT INTO platform_settings(key,value,updated_at) VALUES('kill_switch',$1,now())
			ON CONFLICT (key) DO UPDATE SET value=excluded.value, updated_at=now()`, map[string]bool{"on": on}); err != nil {
			return err
		}
		fmt.Println("kill-switch:", sub(1))
		return record(ctx, a, "kill_switch."+sub(1), "platform", nil)
	case "task inspect":
		return inspect(ctx, a, sub(2))
	default:
		fmt.Print(adminUsage)
		return errors.New("未知命令")
	}
	return nil
}

func inspect(ctx context.Context, a *app.App, id string) error {
	var ws uuid.UUID
	var status, skill string
	var epoch int64
	var code *string
	if err := a.Pool.QueryRow(ctx, `SELECT workspace_id, status, skill_id, epoch, reason_code FROM research_tasks WHERE id=$1`, id).
		Scan(&ws, &status, &skill, &epoch, &code); err != nil {
		return err
	}
	fmt.Printf("任务 %s  技能 %s  状态 %s  epoch %d  原因 %v\n\n", id, skill, status, epoch, deref(code))
	w := tw()
	fmt.Fprintln(w, "步骤\t状态\t开始\t结束\t错误")
	rows, err := a.Pool.Query(ctx, `SELECT step_key, status, started_at, finished_at, coalesce(error,'') FROM task_steps WHERE task_id=$1 AND epoch=$2 ORDER BY id`, id, epoch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, s, e string
		var st, fi *time.Time
		_ = rows.Scan(&k, &s, &st, &fi, &e)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k, s, ts(st), ts(fi), e)
	}
	rows.Close()
	fmt.Fprintln(w, "\n模型调用\t状态\tadapter\t输入/输出token\t费用USD")
	rows, err = a.Pool.Query(ctx, `SELECT step_key||'#'||attempt, status, adapter, coalesce(usage->>'prompt_tokens','0')||'/'||coalesce(usage->>'completion_tokens','0'),
		coalesce(cost_usd::text, reserved_usd::text||'(预留)') FROM model_calls WHERE task_id=$1 ORDER BY sent_at`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, s, ad, tok, c string
		_ = rows.Scan(&k, &s, &ad, &tok, &c)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k, s, ad, tok, c)
	}
	rows.Close()
	fmt.Fprintln(w, "\n被删除的结论\t原因\t文本")
	rows, err = a.Pool.Query(ctx, `SELECT claim_ref, coalesce(removed_reason,''), text FROM claims WHERE task_id=$1 AND verdict='removed' ORDER BY claim_ref`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var r, why, text string
		_ = rows.Scan(&r, &why, &text)
		fmt.Fprintf(w, "%s\t%s\t%s\n", r, why, text)
	}
	rows.Close()
	return w.Flush()
}

func ts(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("15:04:05.000")
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
