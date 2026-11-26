package harness

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice/emquant"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

const (
	ConversationCloseVersion   = "1.2.0-chat-close.1"
	ConversationFixtureVersion = "1.2.0-chat-fixture.1"
	DailyTradingTopic          = "daily_trading"
	FinancingBalanceTopic      = "financing_balance"
)

func isConversationSkill(version string) bool {
	return IsCloseConversationVersion(version) || version == ConversationFixtureVersion || version == ComparisonFixtureVersion || version == emquant.ConversationObservationVersion
}

func conversationObservationSkill() *Skill {
	sk := ObservationBriefSkill()
	sk.Version = emquant.ConversationObservationVersion
	sections := sk.Sections
	sk.Sections = func(p Params) []Section {
		result := sections(p)
		result[0].MaxClaims = 3
		return result
	}
	return sk
}

func ConversationWindows(topic string) []int {
	if topic == ComparisonTopic {
		return []int{5, 10, 20, 60}
	}
	if topic == FinancingBalanceTopic {
		return []int{5, 20, 60, 120}
	}
	return []int{1, 5, 10, 20, 60}
}

func conversationSkill(id, version string) *Skill {
	var topic, title, dataset string
	switch id {
	case "stock_analysis":
		topic, title, dataset = DailyTradingTopic, "日成交", "mkt.daily_bar"
	case "capital_flow":
		topic, title, dataset = FinancingBalanceTopic, "融资余额", "flow.margin"
	default:
		return nil
	}
	section := sec("S2", title, true, []string{topic}, factKinds, 3, 120,
		"给出与本主题直接相关的事实与计算，标明数据实际日期或计算区间。每条结论必须引用事实表，限三条。")
	collect := func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p Params, at time.Time) (*Bundle, error) {
		records, err := (Loader{e.Store}).Load(ctx, at, nil, []string{"ref.trading_calendar"})
		if err != nil {
			return nil, err
		}
		b := newBundle(at, subject, map[uuid.UUID]dataservice.EntityRow{subject.ID: subject}, records)
		b.Days = conversationDays(b, version)
		days := b.Window(p.Window)
		if len(days) == 0 {
			return newBundle(at, subject, b.Entities, nil), nil
		}
		// The manifest contains only the requested window. Unusable rows stay
		// absent so that the chart represents gaps in the published sequence.
		records = slices.DeleteFunc(records, func(r dataservice.StoredRecord) bool {
			return !inWindow(r, "trade_date", days)
		})
		from, err := time.Parse(time.DateOnly, days[0])
		if err != nil {
			return nil, err
		}
		to, err := time.Parse(time.DateOnly, days[len(days)-1])
		if err != nil {
			return nil, err
		}
		data, err := e.Store.Query(ctx, dataservice.Query{DatasetIDs: []string{dataset}, EntityIDs: []uuid.UUID{subject.ID}, From: &from, To: &to, AsOf: at, ModelUse: true})
		if err != nil {
			return nil, err
		}
		records = append(records, slices.DeleteFunc(data, func(r dataservice.StoredRecord) bool {
			_, found := slices.BinarySearch(days, str(r, "trade_date"))
			return !found
		})...)
		return newBundle(at, subject, b.Entities, records), nil
	}
	return &Skill{
		ID: id, Version: version, Title: title + "短研究", Subjects: []string{"stock"},
		Sections: func(Params) []Section { return []Section{section} },
		Validate: func(_ dataservice.EntityRow, raw map[string]any) (Params, error) {
			if err := onlyKeys(raw, "window"); err != nil {
				return Params{}, err
			}
			window, err := intParam(raw, "window", 20, ConversationWindows(topic))
			return Params{Window: window}, err
		},
		Plan: func(ctx context.Context, e *Engine, subject dataservice.EntityRow, p *Params, at time.Time) (map[string]Availability, []string, error) {
			b, err := collect(ctx, e, subject, *p, at)
			if err != nil {
				return nil, nil, err
			}
			n, recent := conversationCoverage(b, dataset)
			a := avail()
			switch {
			case n == 0 || topic == FinancingBalanceTopic && n < 2:
				a = unavailable("所选窗口内缺少可核验的" + title + "记录")
			case !recent:
				a = unavailable("最近两个已结束交易日缺少可核验的" + title)
			case n < p.Window:
				a = limited(fmt.Sprintf("所选 %d 个交易日内有 %d 日可核验的%s记录", p.Window, n, title))
			}
			return map[string]Availability{section.ID: a}, nil, nil
		},
		Collect: collect,
		Compute: func(b *builder, p Params) { b.conversationData(topic, dataset, section.ID, p.Window) },
		Name:    func(subject dataservice.EntityRow, _ Params) string { return subject.Name + " · " + title },
	}
}

func conversationDays(b *Bundle, version string) []string {
	if !IsCloseConversationVersion(version) {
		return b.Days
	}
	completed := b.AsOf.In(cst)
	if completed.Hour() < 15 {
		completed = completed.AddDate(0, 0, -1)
	}
	end, found := slices.BinarySearch(b.Days, completed.Format(time.DateOnly))
	if found {
		end++
	}
	return b.Days[:end]
}

func conversationRecord(r dataservice.StoredRecord, dataset string) bool {
	if dataset == "flow.margin" {
		value, ok := dec(r, "financing_balance")
		return ok && !value.IsNegative()
	}
	a, hasAmount := dec(r, "amount")
	v, hasVolume := dec(r, "volume")
	return !boolean(r, "suspended") && hasAmount && hasVolume && !a.IsNegative() && !v.IsNegative()
}

func conversationCoverage(b *Bundle, dataset string) (int, bool) {
	dates := map[string]bool{}
	for _, r := range b.Rows(dataset, b.Subject.ID) {
		if conversationRecord(r, dataset) {
			dates[str(r, "trade_date")] = true
		}
	}
	for _, day := range b.Window(2) {
		if dates[day] {
			return len(dates), true
		}
	}
	return len(dates), false
}

func (b *builder) conversationData(topic, dataset, section string, n int) {
	subject := b.B.Subject
	byDay := map[string]dataservice.StoredRecord{}
	for _, r := range b.B.Rows(dataset, subject.ID) {
		if conversationRecord(r, dataset) {
			byDay[str(r, "trade_date")] = r
		}
	}
	margin := topic == FinancingBalanceTopic
	title, field, label := "成交额", "amount", "成交额"
	columns := []string{"交易日", "成交量（手）", "成交额（万元）"}
	if margin {
		title, field, label = "融资余额", "financing_balance", "融资余额"
		columns = []string{"交易日", "融资余额（万元）"}
	}
	table := facts.Table{Title: title + "明细", Columns: columns}
	chart := facts.Series{ID: topic, Title: title, Kind: "bar", X: "trade_date", Unit: "万元", Section: section}
	if margin {
		chart.Kind = "area"
	}
	line := facts.SeriesLine{Name: label}
	var first, last facts.Point
	for _, d := range b.B.Window(n) {
		r, ok := byDay[d]
		point := numericPoint(r, d, field)
		var amount, volume *string
		if ok {
			val := point.Value.Shift(-4).String()
			amount = &val
			if !margin {
				v, _ := dec(r, "volume")
				volume = new(string)
				*volume = v.Shift(-2).String()
			}
			if first.Date == "" {
				first = point
			}
			last = point
			b.source(r)
			chart.Evidence = append(chart.Evidence, r.ID)
		}
		if margin {
			table.Rows = append(table.Rows, []*string{&d, amount})
		} else {
			table.Rows = append(table.Rows, []*string{&d, volume, amount})
		}
		line.Points = append(line.Points, [2]*string{&d, amount})
	}
	if last.Date == "" || margin && first.Date == last.Date {
		return
	}
	table.Evidence = slices.Clone(chart.Evidence)
	chart.Lines = []facts.SeriesLine{line}
	b.S.Tables[section] = append(b.S.Tables[section], table)
	b.S.Charts = append(b.S.Charts, chart)
	b.S.Value(topic, subject.Name, label, last.Date, facts.KindMarket, "收盘及已披露数据", "CNY", last.Value, last.EvidenceID)
	if margin {
		input := func(p facts.Point) facts.Input {
			return facts.Input{EvidenceID: p.EvidenceID, Unit: "CNY", Value: p.Value}
		}
		b.S.Computed(topic, subject.Name, "融资余额变化", first.Date+" 至 "+last.Date,
			facts.BalanceChange(input(first), input(last)), subject.Code+"|chat|margin-change")
	} else {
		v, _ := dec(byDay[last.Date], "volume")
		b.S.Value(topic, subject.Name, "成交量", last.Date, facts.KindMarket, "已发布日线", "shares", v, last.EvidenceID)
	}
	if len(chart.Evidence) < n {
		b.S.Limit(fmt.Sprintf("所选 %d 个交易日中有 %d 日可用，空值表示缺少可用记录。", n, len(chart.Evidence)))
	}
}

// ConversationTopics lists the short research available in this deployment.
func (e *Engine) ConversationTopics() []string {
	switch {
	case e.realResearchDisabled():
		return []string{}
	case e.hasCloseSkills(), e.Synthetic && e.Brief == nil:
		topics := []string{DailyTradingTopic, FinancingBalanceTopic}
		if e.comparisonAvailable() {
			topics = append(topics, ComparisonTopic)
		}
		return topics
	case e.Brief != nil:
		return []string{DailyTradingTopic}
	default:
		return []string{}
	}
}

// ConversationResearchSpec describes the scope supported by a registered skill.
type ConversationResearchSpec struct {
	Subjects      []string
	Windows       []int
	DefaultWindow int
	Modes         []string
}

func (e *Engine) ConversationResearchSpec(skill, topic string) (ConversationResearchSpec, bool) {
	if topic != "" {
		if !slices.Contains(e.ConversationTopics(), topic) ||
			(topic == ComparisonTopic && skill != "stock_analysis") ||
			(topic == DailyTradingTopic && skill != "stock_analysis") ||
			(topic == FinancingBalanceTopic && skill != "capital_flow") {
			return ConversationResearchSpec{}, false
		}
		windows := ConversationWindows(topic)
		if e.Brief != nil && !e.hasCloseSkills() {
			windows = []int{20}
		}
		return ConversationResearchSpec{Subjects: []string{"stock"}, Windows: windows, DefaultWindow: 20}, true
	}
	sk := e.Skills[skill]
	if sk == nil || e.realResearchDisabled() || (e.Brief != nil && !e.hasCloseSkills()) {
		return ConversationResearchSpec{}, false
	}
	props, _ := sk.Schema["properties"].(map[string]any)
	window, _ := props["window"].(map[string]any)
	mode, _ := props["mode"].(map[string]any)
	windows, _ := window["enum"].([]int)
	defaultWindow, _ := window["default"].(int)
	modes, _ := mode["enum"].([]string)
	return ConversationResearchSpec{Subjects: sk.Subjects, Windows: windows, DefaultWindow: defaultWindow, Modes: modes}, true
}

// ConversationLaunch binds a new plan to its pending turn. AutoRun is set by the
// current workspace client. The older API can still prepare a plan for confirmation.
type ConversationLaunch struct {
	TurnID   uuid.UUID
	Revision int64
	AutoRun  bool
	Bind     func(pgx.Tx, *Plan) error
}

func (e *Engine) StartConversationResearch(ctx context.Context, ws uuid.UUID, subject dataservice.EntityRow, skill, topic string, window int, mode string, launch ConversationLaunch) (*Plan, error) {
	spec, ok := e.ConversationResearchSpec(skill, topic)
	if !ok || topic == ComparisonTopic {
		return nil, errs.Unprocessable("SKILL_UNAVAILABLE", "当前环境未开放该研究能力")
	}
	if !slices.Contains(spec.Subjects, subject.Kind) {
		return nil, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "该研究能力不支持此对象类型")
	}
	if window != 0 && !slices.Contains(spec.Windows, window) || mode != "" && !slices.Contains(spec.Modes, mode) {
		return nil, errs.Unprocessable("INVALID_PARAMS", "该研究能力不支持所请求的范围")
	}
	if window == 0 {
		window = spec.DefaultWindow
	}
	if topic == "" {
		raw := map[string]any{}
		if window != 0 {
			raw["window"] = float64(window)
		}
		if mode != "" {
			raw["mode"] = mode
		}
		return e.buildPlan(ctx, ws, skill, subject, raw, &launch)
	}
	version := ConversationFixtureVersion
	if e.hasCloseSkills() {
		version = ConversationCloseVersion
		registered, err := e.Store.ResearchSubjectRegistered(ctx, subject.Code)
		if err != nil {
			return nil, err
		}
		if !registered {
			return nil, errs.Unprocessable("SUBJECT_NOT_SUPPORTED", "该证券的收盘数据暂不可用")
		}
		if err := e.Store.WithResearchUse(ctx, func() error { return nil }); err != nil {
			return nil, errs.Unavailable("DATA_SOURCE_UNAVAILABLE", "收盘研究数据源当前不可用").Wrap(err)
		}
	} else if e.Brief != nil {
		if window != 20 {
			return nil, errs.Unprocessable("INVALID_PARAMS", "当前来源提供最近最多 20 个交易日的成交观察")
		}
		return e.buildSkillPlanForTurn(ctx, ws, conversationObservationSkill(), subject, nil, &launch, true)
	}
	sk := conversationSkill(skill, version)
	if sk == nil {
		return nil, errs.Unprocessable("UNKNOWN_SKILL", "不支持的研究技能")
	}
	return e.buildSkillPlanForTurn(ctx, ws, sk, subject, map[string]any{"window": float64(window)}, &launch, true)
}

func (e *Engine) researchBudget(skill, version string) policy.Budget {
	b := e.Policy.Budgets[skill]
	if isConversationSkill(version) {
		b.DeadlineSeconds = min(90, b.DeadlineSeconds)
		b.MaxModelCalls = min(4, b.MaxModelCalls)
	}
	return b
}

func (e *Engine) startConversationTask(ctx context.Context, ws uuid.UUID, sk *Skill, plan *Plan, launch ConversationLaunch, persist func(pgx.Tx) error) error {
	if !launch.AutoRun {
		return db.InWorkspace(ctx, e.Pool, ws, func(tx pgx.Tx) error {
			conv, err := lockPendingTurn(ctx, tx, launch.TurnID, launch.Revision)
			if err != nil {
				return err
			}
			if err := persist(tx); err != nil {
				return err
			}
			if err := launch.Bind(tx, plan); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE conversation_turns SET plan_id=$2, state='confirm', message='研究计划已准备好。', revision=revision+1 WHERE id=$1`, launch.TurnID, plan.ID); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE conversations SET revision=revision+1, updated_at=now(), expires_at=now()+interval '90 days' WHERE id=$1`, conv)
			return err
		})
	}
	return e.confirmTransaction(ctx, ws, plan, func(tx pgx.Tx) error {
		if err := lockResearchAdmission(ctx, tx, ws); err != nil {
			return err
		}
		conv, err := lockPendingTurn(ctx, tx, launch.TurnID, launch.Revision)
		if err != nil {
			return err
		}
		if !time.Now().Before(plan.ExpiresAt) {
			return errs.Conflict("PLAN_EXPIRED", "研究准备超时，请重新发送问题。")
		}
		if err := persist(tx); err != nil {
			return err
		}
		if err := launch.Bind(tx, plan); err != nil {
			return err
		}
		taskID, err := e.enqueueResearchTask(ctx, tx, ws, plan, sk, nil)
		if err != nil {
			return err
		}
		return attachConversationTask(ctx, tx, conv, launch.TurnID, plan.ID, taskID)
	})
}

func lockPendingTurn(ctx context.Context, tx pgx.Tx, turn uuid.UUID, revision int64) (uuid.UUID, error) {
	var conv uuid.UUID
	err := tx.QueryRow(ctx, `SELECT c.id FROM conversations c JOIN conversation_turns t
		ON t.conversation_id=c.id AND t.workspace_id=c.workspace_id
		WHERE t.id=$1 AND c.deleted_at IS NULL AND c.expires_at>clock_timestamp() FOR UPDATE OF c`, turn).Scan(&conv)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, errs.NotFound()
	}
	if err != nil {
		return uuid.Nil, err
	}
	var state string
	var current int64
	err = tx.QueryRow(ctx, `SELECT state, revision FROM conversation_turns WHERE id=$1 AND conversation_id=$2 FOR UPDATE`, turn, conv).Scan(&state, &current)
	if err != nil {
		return uuid.Nil, err
	}
	if current != revision || state != "pending" {
		return uuid.Nil, errs.Conflict("CONVERSATION_CHANGED", "该轮研究已更新")
	}
	return conv, nil
}

func lockTurnConfirmation(ctx context.Context, tx pgx.Tx, turn, plan uuid.UUID, revision int64) (uuid.UUID, uuid.UUID, error) {
	var conv uuid.UUID
	err := tx.QueryRow(ctx, `SELECT c.id FROM conversations c JOIN conversation_turns t
		ON t.conversation_id=c.id AND t.workspace_id=c.workspace_id
		WHERE t.id=$1 AND c.deleted_at IS NULL AND c.expires_at>clock_timestamp() FOR UPDATE OF c`, turn).Scan(&conv)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, errs.NotFound()
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	var state string
	var current int64
	var automatic, newest bool
	var boundPlan, task *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT state, revision, plan_id, task_id, automatic, ordinal=(SELECT max(ordinal) FROM conversation_turns WHERE conversation_id=$2)
		FROM conversation_turns WHERE id=$1 AND conversation_id=$2 FOR UPDATE`, turn, conv).Scan(&state, &current, &boundPlan, &task, &automatic, &newest)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if !automatic && boundPlan != nil && *boundPlan == plan && task != nil {
		return conv, *task, nil
	}
	if automatic || !newest || current != revision || state != "confirm" || boundPlan == nil || *boundPlan != plan {
		return uuid.Nil, uuid.Nil, errs.Conflict("CONVERSATION_CHANGED", "该轮研究已更新，请刷新后确认")
	}
	return conv, uuid.Nil, nil
}

func attachConversationTask(ctx context.Context, tx pgx.Tx, conv, turn, plan, task uuid.UUID) error {
	if _, err := tx.Exec(ctx, `UPDATE conversation_turns SET plan_id=$2,task_id=$3,state='research',message=NULL,revision=revision+1 WHERE id=$1`, turn, plan, task); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE conversations SET revision=revision+1,updated_at=now(),expires_at=now()+interval '90 days',
		scope=(SELECT intent->'scope' FROM conversation_turns WHERE id=$2) WHERE id=$1`, conv, turn)
	return err
}

func lockDeliveryConversation(ctx context.Context, tx pgx.Tx, t *taskRow) error {
	err := lockTaskConversation(ctx, tx, t.Workspace, t.ID, false)
	if err != nil && errs.As(err).Status == 404 {
		return errStale
	}
	return err
}

// LockShortConversation protects a short result while it is being displayed.
func LockShortConversation(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID) error {
	return lockTaskConversation(ctx, tx, ws, task, true)
}

func lockTaskConversation(ctx context.Context, tx pgx.Tx, ws, task uuid.UUID, briefOnly bool) error {
	var turn *uuid.UUID
	var chat bool
	err := tx.QueryRow(ctx, `SELECT p.conversation_turn_id,p.conversation_plan FROM research_tasks r
		JOIN research_plans p ON p.workspace_id=r.workspace_id AND p.id=r.plan_id
		WHERE r.workspace_id=$1 AND r.id=$2 AND (NOT $3 OR p.conversation_brief)`, ws, task, briefOnly).Scan(&turn, &chat)
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound()
	}
	if err != nil {
		return err
	}
	if !chat && !briefOnly {
		return nil
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `SELECT c.id FROM conversation_turns t JOIN conversations c
		ON c.workspace_id=t.workspace_id AND c.id=t.conversation_id
		WHERE t.workspace_id=$1 AND t.id=$2 AND c.deleted_at IS NULL AND c.expires_at>clock_timestamp()
		FOR SHARE OF c`, ws, turn).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errs.NotFound()
	}
	return err
}

// WithConversationSnapshot locks the source, frozen records, and conversation.
func (e *Engine) WithConversationSnapshot(ctx context.Context, ws, task uuid.UUID, use func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, e.Pool, func(tx pgx.Tx) error {
		if err := e.Store.RequireResearchUseTx(ctx, tx); err != nil {
			return err
		}
		var hash string
		err := tx.QueryRow(ctx, `SELECT r.snapshot_hash FROM research_tasks r
			JOIN research_plans p ON p.workspace_id=r.workspace_id AND p.id=r.plan_id
			JOIN conversation_turns t ON t.workspace_id=r.workspace_id AND t.id=p.conversation_turn_id
			JOIN conversations c ON c.workspace_id=r.workspace_id AND c.id=t.conversation_id
			WHERE r.workspace_id=$1 AND r.id=$2 AND r.skill_version=ANY($3::text[]) AND c.deleted_at IS NULL AND c.expires_at>clock_timestamp()
			FOR SHARE OF c`, ws, task, CloseConversationVersions()).Scan(&hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return errs.NotFound()
		}
		if err != nil {
			return err
		}
		if err := e.Store.LockSnapshot(ctx, tx, hash); err != nil {
			return err
		}
		return use(tx)
	})
}
