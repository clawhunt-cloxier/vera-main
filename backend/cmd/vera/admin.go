package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/app"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/audit"
)

const adminUsage = `vera admin <command>

  invite create [--count N] [--uses N] [--expires 14d] [--note 文本]   生成邀请码（只显示一次）
  invite list | invite revoke <id>
  user list | user disable <id> | user enable <id> | user reset-password <id>
  quota set --user <id> [--active N] [--queued N] [--daily N] [--deep N]
  provider list | provider test <id>
  provider test tushare --research      探测首个配置证券的名录、近一年公告和三张合并报表，各请求一行
  provider model-use tushare on|off     设置本数据库下一次研究可使用的 TuShare 权限
  data sync [--dataset <id>[,<id>]] [--since 2026-11-01]   按配置的数据提供方同步（只新增）
  usage report [--day 2026-11-07] [--tz UTC]   当日模型调用、token、费用（按用户与技能汇总）
  usage reconcile [--release <call-id> --cost <USD>]   列出 UNKNOWN 调用 / 按账单核销一条
  kill-switch on|off                停止/恢复接受新研究任务（在途任务继续）
  task list | task inspect <id>     步骤台账、模型调用与被删除的结论（= vera harness replay --task <id> --dry-run）

每条命令（包括只读命令与失败的命令）都写审计事件。
`

// errNotImplemented marks commands whose backing service is not available yet; they fail loudly, never fake success.
var errNotImplemented = errors.New("未实现")

func tw(w io.Writer) *tabwriter.Writer { return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0) }

func days(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(s)
}

func actor() string { return "vera-admin:" + os.Getenv("USER") }

// adminResult is what a command reports for its audit event.
type adminResult struct {
	action, resource string
	detail           map[string]any
}

func admin(ctx context.Context, a *app.App, args []string) error {
	return adminTo(ctx, a, args, os.Stdout, actor())
}

// adminTo runs one command and always records an audit event for it (result success or failure).
func adminTo(ctx context.Context, a *app.App, args []string, out io.Writer, who string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(out, adminUsage)
		return nil
	}
	res, err := runAdmin(ctx, a, args, out, who)
	if res.action == "" {
		return err
	}
	result := "success"
	if res.detail == nil {
		res.detail = map[string]any{}
	}
	if err != nil {
		result = "failure"
		res.detail["error"] = err.Error()
	}
	aerr := pgx.BeginFunc(context.WithoutCancel(ctx), a.Pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, nil, who, res.action, res.resource, result, res.detail)
	})
	if err == nil && aerr != nil {
		return fmt.Errorf("操作已执行，但审计事件写入失败: %w", aerr)
	}
	return err
}

func parseID(s, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s ID 无效: %q", what, s)
	}
	return id, nil
}

func runAdmin(ctx context.Context, a *app.App, args []string, out io.Writer, who string) (adminResult, error) {
	sub := func(i int) string {
		if len(args) > i {
			return args[i]
		}
		return ""
	}
	cmd := args[0] + " " + sub(1)
	res := adminResult{action: strings.ReplaceAll(cmd, " ", "."), resource: "platform"}
	switch cmd {
	case "invite create":
		fs := flag.NewFlagSet("invite create", flag.ContinueOnError)
		count := fs.Int("count", 1, "")
		uses := fs.Int("uses", 1, "")
		exp := fs.String("expires", "14d", "")
		note := fs.String("note", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return res, err
		}
		ttl, err := days(*exp)
		if err != nil || ttl <= 0 || *count < 1 || *count > 100 || *uses < 1 {
			return res, errors.New("参数无效")
		}
		res.resource, res.detail = "invite", map[string]any{"count": *count, "uses": *uses, "expires": *exp, "note": *note}
		codes, err := identity.CreateInvites(ctx, a.Pool, *count, *uses, ttl, *note, who)
		if err != nil {
			return res, err
		}
		fmt.Fprintf(out, "已生成 %d 个邀请码（只显示这一次，库中仅存哈希）：\n", len(codes))
		for _, c := range codes {
			fmt.Fprintln(out, c)
		}
		return res, nil
	case "invite list":
		res.resource = "invite"
		rows, err := a.Pool.Query(ctx, `SELECT id, remaining, note, expires_at, revoked_at IS NOT NULL FROM invites ORDER BY created_at DESC`)
		if err != nil {
			return res, err
		}
		defer rows.Close()
		w := tw(out)
		fmt.Fprintln(w, "ID\t剩余\t备注\t过期\t作废")
		for rows.Next() {
			var id uuid.UUID
			var n int
			var note string
			var exp time.Time
			var rev bool
			if err := rows.Scan(&id, &n, &note, &exp, &rev); err != nil {
				return res, err
			}
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%v\n", id, n, note, exp.Format("2006-01-02"), rev)
		}
		return res, w.Flush()
	case "invite revoke":
		res.resource = "invite:" + sub(2)
		id, err := parseID(sub(2), "邀请码")
		if err != nil {
			return res, err
		}
		tag, err := a.Pool.Exec(ctx, `UPDATE invites SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("邀请码不存在或已作废")
		}
		return res, err
	case "user list":
		res.resource = "user"
		rows, err := a.Pool.Query(ctx, `SELECT u.id, u.email, u.display_name, u.persona, w.id IS NOT NULL, u.disabled_at IS NOT NULL, u.created_at
			FROM users u LEFT JOIN workspaces w ON w.owner_user_id=u.id ORDER BY u.created_at`)
		if err != nil {
			return res, err
		}
		defer rows.Close()
		w := tw(out)
		fmt.Fprintln(w, "ID\t邮箱\t名称\t身份标签\t已开通\t停用\t注册")
		for rows.Next() {
			var id uuid.UUID
			var email, name, persona string
			var ws, dis bool
			var at time.Time
			if err := rows.Scan(&id, &email, &name, &persona, &ws, &dis, &at); err != nil {
				return res, err
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%v\t%s\n", id, email, name, persona, ws, dis, at.Format("2006-01-02"))
		}
		return res, w.Flush()
	case "user disable", "user enable":
		res.resource = "user:" + sub(2)
		id, err := parseID(sub(2), "用户")
		if err != nil {
			return res, err
		}
		err = pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
			q := `UPDATE users SET disabled_at=now() WHERE id=$1 AND disabled_at IS NULL`
			if sub(1) == "enable" {
				q = `UPDATE users SET disabled_at=NULL WHERE id=$1 AND disabled_at IS NOT NULL`
			}
			tag, err := tx.Exec(ctx, q, id)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				var exists bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists); err != nil {
					return err
				}
				if !exists {
					return errors.New("用户不存在")
				}
			}
			if sub(1) == "disable" {
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, id)
			}
			return err
		})
		if err == nil {
			fmt.Fprintln(out, "完成。")
		}
		return res, err
	case "user reset-password":
		res.resource = "user:" + sub(2)
		return res, fmt.Errorf("%w：密码由 Keycloak 管理，Admin API 接入随 Keycloak 部署（T012）完成；在此之前请在 Keycloak 管理台操作", errNotImplemented)
	case "quota set":
		fs := flag.NewFlagSet("quota set", flag.ContinueOnError)
		user := fs.String("user", "", "")
		o := map[string]*int{"user_active_tasks": fs.Int("active", -1, ""), "user_queued_tasks": fs.Int("queued", -1, ""),
			"user_daily_tasks": fs.Int("daily", -1, ""), "user_daily_deep_research": fs.Int("deep", -1, "")}
		if err := fs.Parse(args[2:]); err != nil {
			return res, err
		}
		res.resource = "user:" + *user
		id, err := parseID(*user, "用户")
		if err != nil {
			return res, err
		}
		set := map[string]int{}
		for k, v := range o {
			if *v >= 0 {
				set[k] = *v
			}
		}
		if len(set) == 0 {
			return res, errors.New("至少指定一项配额")
		}
		res.detail = map[string]any{"override": set}
		b, _ := json.Marshal(set)
		tag, err := a.Pool.Exec(ctx, `UPDATE workspaces SET quota_override=coalesce(quota_override,'{}'::jsonb) || $2::jsonb WHERE owner_user_id=$1`, id, b)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("用户不存在或尚未开通")
		}
		return res, err
	case "provider list":
		res.resource = "provider"
		return res, providerList(ctx, a, out)
	case "provider test":
		if len(args) == 4 && sub(2) == "tushare" && sub(3) == "--research" {
			res.action, res.resource = "provider.test.research", "provider:tushare"
			if a.TuShare == nil {
				return res, errors.New("请先配置 TuShare 数据源")
			}
			checks, err := a.TuShare.ResearchAccess(ctx)
			res.detail = map[string]any{"checks": checks}
			if writeErr := json.NewEncoder(out).Encode(checks); writeErr != nil {
				return res, writeErr
			}
			return res, err
		}
		if len(args) != 3 {
			return res, errors.New("用法：provider test <id> 或 provider test tushare --research")
		}
		res.resource = "provider:" + sub(2)
		return res, providerTest(ctx, a, sub(2), out)
	case "provider model-use":
		res.resource = "provider:tushare"
		if a.TuShare == nil || len(args) != 4 || sub(2) != "tushare" || (sub(3) != "on" && sub(3) != "off") {
			return res, errors.New("用法：provider model-use tushare on|off")
		}
		allowed := sub(3) == "on"
		if allowed && !a.TuShare.AllowModelProcessing() {
			return res, errors.New("需要先在部署中启用 TUSHARE_ALLOW_MODEL_PROCESSING")
		}
		res.detail = map[string]any{"allow_model_processing": allowed}
		tag, err := a.Pool.Exec(ctx, `UPDATE providers SET allow_model_processing=$1 WHERE id='tushare' AND kind='vendor' AND NOT synthetic`, allowed)
		if err == nil && tag.RowsAffected() == 0 {
			return res, errors.New("请先同步以登记 TuShare")
		}
		return res, err
	case "data sync":
		fs := flag.NewFlagSet("data sync", flag.ContinueOnError)
		ds := fs.String("dataset", "", "")
		since := fs.String("since", "", "")
		if err := fs.Parse(args[2:]); err != nil {
			return res, err
		}
		res.resource = "provider:" + a.Primary.ID()
		res.detail = map[string]any{"dataset": *ds, "since": *since}
		if a.TuShare != nil {
			if *ds != "" || *since != "" {
				return res, errors.New("TuShare 按研究对象发布完整观测，请使用 vera sync")
			}
			return res, a.Sync(ctx)
		}
		var list []string
		for _, d := range strings.Split(*ds, ",") {
			if d = strings.TrimSpace(d); d == "" {
				continue
			}
			if _, ok := a.Store.Catalog().Dataset(d); !ok {
				return res, fmt.Errorf("未登记的数据集: %s", d)
			}
			list = append(list, d)
		}
		var from time.Time
		if *since != "" {
			t, err := time.Parse("2006-01-02", *since)
			if err != nil {
				return res, fmt.Errorf("--since 需为 YYYY-MM-DD: %w", err)
			}
			from = t
		}
		n, err := a.Store.Sync(ctx, dataservice.FilterRecords(a.Primary, list, from))
		res.detail["inserted"] = n
		if err == nil {
			fmt.Fprintf(out, "同步完成：新增 %d 条记录\n", n)
		}
		return res, err
	case "usage report":
		fs := flag.NewFlagSet("usage report", flag.ContinueOnError)
		day := fs.String("day", "", "")
		tz := fs.String("tz", "UTC", "")
		if err := fs.Parse(args[2:]); err != nil {
			return res, err
		}
		res.resource, res.detail = "usage", map[string]any{"day": *day, "tz": *tz}
		return res, usageReport(ctx, a, *day, *tz, out)
	case "usage reconcile":
		fs := flag.NewFlagSet("usage reconcile", flag.ContinueOnError)
		rel := fs.String("release", "", "")
		cost := fs.String("cost", "", "按厂商账单确认的实际费用（USD）")
		if err := fs.Parse(args[2:]); err != nil {
			return res, err
		}
		res.resource = "usage"
		if *rel == "" {
			return res, unknownCalls(ctx, a, out)
		}
		res.resource, res.detail = "model_call:"+*rel, map[string]any{"cost_usd": *cost}
		id, err := parseID(*rel, "调用")
		if err != nil {
			return res, err
		}
		c, err := decimal.NewFromString(*cost)
		if err != nil || c.IsNegative() {
			return res, errors.New("--cost 必须是按账单确认的非负金额")
		}
		// UNKNOWN calls, or calls auto-released after 24h (cost 0) whose bill arrived later.
		tag, err := a.Pool.Exec(ctx, `UPDATE model_calls SET status='RECONCILED', cost_usd=$2, finished_at=coalesce(finished_at, now())
			WHERE id=$1 AND (status='UNKNOWN' OR (status='RECONCILED' AND error LIKE '%auto-released%'))`, id, c)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("调用不存在，或不是 UNKNOWN / 自动释放的调用")
		}
		return res, err
	case "kill-switch on", "kill-switch off":
		on := sub(1) == "on"
		if _, err := a.Pool.Exec(ctx, `INSERT INTO platform_settings(key,value,updated_at) VALUES('kill_switch',$1,now())
			ON CONFLICT (key) DO UPDATE SET value=excluded.value, updated_at=now()`, map[string]bool{"on": on}); err != nil {
			return res, err
		}
		fmt.Fprintln(out, "kill-switch:", sub(1))
		return res, nil
	case "task list":
		res.resource = "task"
		return res, taskList(ctx, a, out)
	case "task inspect":
		res.resource = "task:" + sub(2)
		return res, inspect(ctx, a, sub(2), out)
	}
	fmt.Fprint(out, adminUsage)
	return adminResult{}, errors.New("未知命令")
}

func providerList(ctx context.Context, a *app.App, out io.Writer) error {
	rows, err := a.Pool.Query(ctx, `SELECT p.id, p.name, p.kind, p.synthetic, p.allow_model_processing, p.enabled,
		(SELECT count(*) FROM records r WHERE r.provider_id=p.id), (SELECT max(available_at) FROM records r WHERE r.provider_id=p.id) FROM providers p`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tw(out)
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
}

// providerTest makes read-only requests to the configured provider and reports what came back. Nothing is stored.
func providerTest(ctx context.Context, a *app.App, id string, out io.Writer) error {
	p := a.ProviderByID(id)
	if p == nil {
		return errors.New("只能测试当前部署已配置的数据提供方")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	start := time.Now()
	if a.TuShare != nil && id == a.TuShare.ID() {
		if err := a.TuShare.Probe(ctx); err != nil {
			return fmt.Errorf("TuShare 股票名录探测失败: %w", err)
		}
		fmt.Fprintf(out, "TuShare 股票名录已返回配置的证券，耗时 %s。其他接口的账号权限由同步结果确认。\n", time.Since(start).Round(time.Millisecond))
		return nil
	}
	ents, err := p.Entities(ctx)
	if err != nil {
		return fmt.Errorf("实体列表请求失败: %w", err)
	}
	caps, err := p.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("能力清单请求失败: %w", err)
	}
	fmt.Fprintf(out, "提供方 %s（%s），实体 %d 个，登记能力 %d 项，模拟数据 %v，耗时 %s\n", p.ID(), p.Name(),
		len(ents), len(caps), p.Synthetic(), time.Since(start).Round(time.Millisecond))
	return nil
}

// usageReport sums model calls of one calendar day in tz, by user and skill. Unsettled calls (SENT/UNKNOWN or
// unpriced) count at their reservation, as the gateway's budget does.
func usageReport(ctx context.Context, a *app.App, day, tz string, out io.Writer) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("--tz 无效: %w", err)
	}
	start := time.Now().In(loc)
	if day != "" {
		if start, err = time.ParseInLocation("2006-01-02", day, loc); err != nil {
			return fmt.Errorf("--day 需为 YYYY-MM-DD: %w", err)
		}
	}
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	end := start.AddDate(0, 0, 1)
	rows, err := a.Pool.Query(ctx, `SELECT coalesce(nullif(u.email,''), u.id::text, '(无用户)'), coalesce(t.skill_id, m.step_key), m.profile, count(*),
		count(*) FILTER (WHERE m.status='SUCCEEDED'), count(*) FILTER (WHERE m.status='UNKNOWN'),
		coalesce(sum((m.usage->>'prompt_tokens')::bigint),0), coalesce(sum((m.usage->>'completion_tokens')::bigint + coalesce((m.usage->>'reasoning_tokens')::bigint,0)),0),
		coalesce(sum(CASE WHEN m.status IN ('SENT','UNKNOWN') OR m.cost_usd < 0 THEN m.reserved_usd ELSE coalesce(m.cost_usd,0) END),0)
		FROM model_calls m LEFT JOIN research_tasks t ON t.id=m.task_id
		LEFT JOIN workspaces w ON w.id=m.workspace_id LEFT JOIN users u ON u.id=w.owner_user_id
		WHERE m.sent_at >= $1 AND m.sent_at < $2 GROUP BY 1,2,3 ORDER BY 1,2,3`, start, end)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tw(out)
	fmt.Fprintf(w, "时段\t%s 至 %s（%s）\n\n", start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"), loc)
	fmt.Fprintln(w, "用户\t技能/步骤\tprofile\t调用\t成功\t未知\t输入token\t输出token\t费用USD")
	total := decimal.Zero
	for rows.Next() {
		var user, k, p string
		var n, ok, unk, in, outTok int64
		var cost decimal.Decimal
		if err := rows.Scan(&user, &k, &p, &n, &ok, &unk, &in, &outTok, &cost); err != nil {
			return err
		}
		total = total.Add(cost)
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\n", user, k, p, n, ok, unk, in, outTok, cost.StringFixed(6))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n合计\t\t\t\t\t\t\t\t%s\n", total.StringFixed(6))
	if a.Engine != nil && a.Engine.Gateway != nil {
		fmt.Fprintf(w, "平台日费用上限\t%s USD（按数据库时区的自然日计）\n", a.Engine.Gateway.DailyLimit)
	}
	return w.Flush()
}

func unknownCalls(ctx context.Context, a *app.App, out io.Writer) error {
	rows, err := a.Pool.Query(ctx, `SELECT id, task_id, step_key, profile, reserved_usd::text, sent_at, coalesce(error,'') FROM model_calls
		WHERE status='UNKNOWN' ORDER BY sent_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tw(out)
	fmt.Fprintln(w, "调用ID\t任务\t步骤\tprofile\t预留USD\t发送时间\t错误")
	for rows.Next() {
		var id uuid.UUID
		var task *uuid.UUID
		var step, prof, res, e string
		var at time.Time
		if err := rows.Scan(&id, &task, &step, &prof, &res, &at, &e); err != nil {
			return err
		}
		t := "-"
		if task != nil {
			t = task.String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", id, t, step, prof, res, at.Format(time.RFC3339), e)
	}
	return w.Flush()
}

func taskList(ctx context.Context, a *app.App, out io.Writer) error {
	rows, err := a.Pool.Query(ctx, `SELECT id::text, skill_id, status, coalesce(reason_code,'-'), epoch, created_at FROM research_tasks ORDER BY created_at DESC LIMIT 20`)
	if err != nil {
		return err
	}
	defer rows.Close()
	w := tw(out)
	fmt.Fprintln(w, "任务\t技能\t状态\t原因\tepoch\t创建时间")
	for rows.Next() {
		var id, sk, st, rc string
		var ep int64
		var at time.Time
		if err := rows.Scan(&id, &sk, &st, &rc, &ep, &at); err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", id, sk, st, rc, ep, at.Format(time.RFC3339))
	}
	return w.Flush()
}

// inspect prints the step ledger, model calls and removed claims of a task. It only reads (no model calls).
func inspect(ctx context.Context, a *app.App, idStr string, out io.Writer) error {
	id, err := parseID(idStr, "任务")
	if err != nil {
		return err
	}
	var status, skill string
	var epoch int64
	var code *string
	if err := a.Pool.QueryRow(ctx, `SELECT status, skill_id, epoch, reason_code FROM research_tasks WHERE id=$1`, id).
		Scan(&status, &skill, &epoch, &code); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("任务不存在")
		}
		return err
	}
	fmt.Fprintf(out, "任务 %s  技能 %s  状态 %s  epoch %d  原因 %s\n\n", id, skill, status, epoch, deref(code))
	w := tw(out)
	fmt.Fprintln(w, "步骤\t状态\t开始\t结束\t错误")
	rows, err := a.Pool.Query(ctx, `SELECT step_key, status, started_at, finished_at, coalesce(error,'') FROM task_steps WHERE task_id=$1 AND epoch=$2 ORDER BY id`, id, epoch)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, s, e string
		var st, fi *time.Time
		if err := rows.Scan(&k, &s, &st, &fi, &e); err != nil {
			rows.Close()
			return err
		}
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
		if err := rows.Scan(&k, &s, &ad, &tok, &c); err != nil {
			rows.Close()
			return err
		}
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
		if err := rows.Scan(&r, &why, &text); err != nil {
			rows.Close()
			return err
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", r, why, text)
	}
	rows.Close()
	return w.Flush()
}

func ts(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339Nano)
}

func deref(s *string) string {
	if s == nil {
		return "-"
	}
	return *s
}
