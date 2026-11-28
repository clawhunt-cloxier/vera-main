package httpapi

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation/conversationtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

func TestConversationStockScopeInheritance(t *testing.T) {
	const (
		manufacturing = "演示制造"
		bank          = "演示银行"
		financingTool = "inspect_financing_balance"
		tradingTool   = "inspect_daily_trading"
		seedFive      = "看看演示制造近 5 个交易日的融资余额"
		seedLong      = "看看演示制造近 120 个交易日的融资余额"
		withoutStock  = "想查最近 60 个交易日的日成交，股票我等下补。"
		catalogChoice = "演示银行（DM0004.DEMO）。"
	)
	type step struct {
		text, tool, source string
		mentions           []string
		days               int
		wantSubject        string
		wantTopic          string
		wantWindow         int
		clarification      string
		wantCandidates     []string
	}
	tests := []struct {
		name     string
		seed     string
		seedDays int
		steps    []step
	}{
		{"keep_window_across_stock_and_indicator_changes", seedFive, 5, []step{{
			text: "换成演示银行，改看日成交，窗口照旧。", tool: tradingTool, source: "message",
			mentions: []string{bank}, wantSubject: bank, wantWindow: 5,
		}}},
		{"echoed_window_is_still_inherited", seedFive, 5, []step{{
			text: "换成演示银行，改看它的每日成交额，交易日范围不变。", tool: tradingTool, source: "message",
			mentions: []string{bank}, days: 5, wantSubject: bank, wantWindow: 5,
		}}},
		{"scope_reference_is_not_one_trading_day", seedFive, 5, []step{{
			text: "换成演示银行的日成交，按同一个交易日窗口看。", tool: tradingTool, source: "message",
			mentions: []string{bank}, days: 1, wantSubject: bank, wantWindow: 5,
		}}},
		{"reject_old_window_until_an_explicit_new_one", seedFive, 5, []step{
			{
				text: "查看日成交，但不沿用同一个交易日窗口。", tool: tradingTool, source: "context",
				wantSubject: manufacturing, clarification: "window",
			},
			{
				text: "演示制造。", tool: tradingTool, source: "message", mentions: []string{manufacturing},
				wantSubject: manufacturing, clarification: "window",
			},
			{
				text: "换成 10 个交易日。", tool: financingTool, source: "context",
				days: 10, wantSubject: manufacturing, wantTopic: harness.DailyTradingTopic, wantWindow: 10,
			},
		}},
		{"explicit_window_replaces_inherited_window", seedFive, 5, []step{{
			text: "改看演示银行近 10 个交易日的日成交。", tool: tradingTool, source: "message",
			mentions: []string{bank}, days: 10, wantSubject: bank, wantWindow: 10,
		}}},
		{"window_reply_keeps_indicator_when_router_selects_another", seedFive, 5, []step{{
			text: "股票和指标照旧，只把时间范围调到 60 个交易日。", tool: tradingTool, source: "context",
			days: 60, wantSubject: manufacturing, wantTopic: harness.FinancingBalanceTopic, wantWindow: 60,
		}}},
		{"exclude_catalog_subject_with_context", seedFive, 5, []step{{
			text: "还是看演示制造的融资余额，演示银行只是备选，这轮先不看。", tool: financingTool, source: "context",
			wantSubject: manufacturing, wantWindow: 5,
		}}},
		{"exclude_catalog_subject_supplied_by_router", seedFive, 5, []step{{
			text: "股票还是演示制造，演示银行只是备选，这轮先不看。继续查融资余额。", tool: financingTool, source: "message",
			mentions: []string{manufacturing, bank}, wantSubject: manufacturing, wantWindow: 5,
		}}},
		{"exclude_subject_by_suffix", seedFive, 5, []step{{
			text: "演示银行暂时不查，演示制造的融资余额照旧。", tool: financingTool, source: "context",
			wantSubject: manufacturing, wantWindow: 5,
		}}},
		{"defer_subject_to_next_research", seedFive, 5, []step{{
			text: "演示银行留到下次再查。这次仍看原来的融资余额。", tool: financingTool, source: "context",
			wantSubject: manufacturing, wantWindow: 5,
		}}},
		{"historical_subject_is_context_only", seedFive, 5, []step{{
			text: "上一轮查的是演示制造。现在只查演示银行的融资余额。", tool: financingTool, source: "message",
			mentions: []string{manufacturing, bank}, wantSubject: bank, wantWindow: 5,
		}}},
		{"backup_followed_by_active_request_still_conflicts", seedFive, 5, []step{{
			text: "继续查演示制造，演示银行只是备选，这轮也看看它。", tool: financingTool, source: "context",
			clarification: "subject", wantCandidates: []string{manufacturing, bank},
		}}},
		{"exclude_inherited_subject_and_resume_after_choice", seedFive, 5, []step{
			{
				text: "先不看演示制造，继续查融资余额。", tool: "respond_to_user",
				wantWindow: 5, clarification: "subject",
			},
			{
				text: "演示银行。", tool: tradingTool, source: "message", mentions: []string{bank},
				days: 20, wantSubject: bank, wantTopic: harness.FinancingBalanceTopic, wantWindow: 5,
			},
		}},
		{"other_positive_subject_still_conflicts", seedFive, 5, []step{{
			text: "继续查演示制造和演示银行的融资余额。", tool: financingTool, source: "context",
			clarification: "subject", wantCandidates: []string{manufacturing, bank},
		}}},
		{"normalize_name_and_code_as_one_subject", seedFive, 5, []step{{
			text: "只看演示银行（DM0004.DEMO），仍查 20 个交易日的日成交。", tool: tradingTool, source: "message",
			mentions: []string{"演示银行 (DM0004.DEMO)"}, days: 20, wantSubject: bank, wantWindow: 20,
		}}},
		{"deduplicate_separate_name_and_code", seedFive, 5, []step{{
			text: "换成演示银行 (DM0004.DEMO)，继续查融资余额。", tool: financingTool, source: "message",
			mentions: []string{bank, "DM0004.DEMO"}, wantSubject: bank, wantWindow: 5,
		}}},
		{"conflicting_name_and_code_require_clarification", seedFive, 5, []step{{
			text: "改查演示银行（DM0001.DEMO）的融资余额。", tool: financingTool, source: "message",
			mentions: []string{bank}, clarification: "subject",
		}}},
		{"unknown_code_cannot_reuse_current_or_known_name", seedFive, 5, []step{{
			text: "这次看演示制造（699999.SH）的融资余额。", tool: financingTool, source: "context",
			clarification: "subject",
		}}},
		{"exclude_both_aliases_in_a_single_declaration", seedFive, 5, []step{{
			text: "先不看演示银行（DM0004.DEMO），继续查演示制造。", tool: financingTool, source: "message",
			mentions: []string{"演示银行(DM0004.DEMO)", manufacturing}, wantSubject: manufacturing, wantWindow: 5,
		}}},
		{"incompatible_window_is_clarified_before_recovery", seedLong, 120, []step{
			{
				text: "日成交呢？", tool: tradingTool, source: "context",
				wantSubject: manufacturing, wantWindow: 120, clarification: "window",
			},
			{
				text: "那就看 60 个交易日。", tool: financingTool, source: "context",
				days: 60, wantSubject: manufacturing, wantTopic: harness.DailyTradingTopic, wantWindow: 60,
			},
		}},
	}

	responses := map[string]model.ToolArguments{}
	reply := func(text, tool, source string, mentions []string, days int) {
		t.Helper()
		if tool == "respond_to_user" {
			responses[text] = model.ToolArguments{Name: tool, Arguments: `{"kind":"clarify","message":"请问要查询哪只股票？"}`}
			return
		}
		if mentions == nil {
			mentions = []string{}
		}
		args, err := json.Marshal(map[string]any{
			"subject_mentions": mentions,
			"subject_source":   source,
			"trading_days":     days,
			"advice":           "none",
		})
		if err != nil {
			t.Fatal(err)
		}
		responses[text] = model.ToolArguments{Name: tool, Arguments: string(args)}
	}
	reply(seedFive, financingTool, "message", []string{manufacturing}, 5)
	reply(seedLong, financingTool, "message", []string{manufacturing}, 120)
	reply(withoutStock, "respond_to_user", "", nil, 0)
	reply(catalogChoice, financingTool, "message", []string{bank}, 20)
	for _, tc := range tests {
		for _, s := range tc.steps {
			reply(s.text, s.tool, s.source, s.mentions, s.days)
		}
	}

	e := setupEngine(t, func(engine *harness.Engine) {
		conversationtest.UseRouter(engine.Gateway, responses)
		engine.Policy.Quota.PlanParsePerUserPerMinute = 0
		engine.Policy.Quota.UserDailyTasks = 40
	})
	startConversationWorker(t, e)
	a := onboardAPIClient(t, e, "stock-scope-inheritance")
	stocks := map[string]uuid.UUID{}
	for _, name := range []string{manufacturing, bank} {
		matches, err := e.engine.Store.ResolveAlias(t.Context(), name, "stock")
		if err != nil || len(matches) != 1 {
			t.Fatalf("resolve %s: %v, %d matches", name, err, len(matches))
		}
		stocks[name] = matches[0].ID
	}
	send := func(t *testing.T, text string, conversationID uuid.UUID) (conversation.Accepted, chatReadTurn) {
		t.Helper()
		in := conversation.Message{Text: text, Automatic: true, ClientMessageID: uuid.New()}
		path := chatPath
		if conversationID != uuid.Nil {
			path += "/" + conversationID.String()
			var thread chatThread
			a.json("GET", path, nil, 200, &thread)
			in.ExpectedRevision = thread.Conversation.Revision
			path += "/turns"
		}
		var accepted conversation.Accepted
		a.json("POST", path, in, 202, &accepted)
		turn := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool {
			return v.State != "pending" && (v.State != "research" || harness.IsTerminal(v.TaskStatus))
		})
		return accepted, turn
	}
	checkScope := func(t *testing.T, turn chatReadTurn, stock, topic string, window int) {
		t.Helper()
		if turn.Scope == nil {
			t.Fatalf("missing scope for %s, %s, %d days: %+v", stock, topic, window, turn)
		}
		if turn.Scope.SubjectID != stocks[stock] || turn.Scope.Topic != topic || turn.Scope.Window != window {
			t.Fatalf("wanted scope %s, %s, %d days, got %+v", stock, topic, window, *turn.Scope)
		}
	}
	t.Run("generic_reply_keeps_missing_stock_scope_for_next_turn", func(t *testing.T) {
		first, pending := send(t, withoutStock, uuid.Nil)
		if pending.State != "clarify" || !pending.NeedsSubject || pending.TaskStatus != "" {
			t.Fatalf("expected saved stock clarification: %+v", pending)
		}
		checkScope(t, pending, "", harness.DailyTradingTopic, 60)
		_, resumed := send(t, catalogChoice, first.ConversationID)
		if !resumed.Result || resumed.TaskStatus != "COMPLETED" {
			t.Fatalf("expected research after a verified name and code: %+v", resumed)
		}
		checkScope(t, resumed, bank, harness.DailyTradingTopic, 60)
	})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first, initial := send(t, tc.seed, uuid.Nil)
			if !initial.Result {
				t.Fatalf("initial research did not deliver a result: %+v", initial)
			}
			checkScope(t, initial, manufacturing, harness.FinancingBalanceTopic, tc.seedDays)
			for _, s := range tc.steps {
				_, turn := send(t, s.text, first.ConversationID)
				topic := harness.FinancingBalanceTopic
				if s.tool == tradingTool {
					topic = harness.DailyTradingTopic
				}
				if s.wantTopic != "" {
					topic = s.wantTopic
				}
				switch s.clarification {
				case "subject":
					if turn.State != "clarify" || turn.TaskStatus != "" || turn.Result || !turn.NeedsSubject {
						t.Fatalf("expected a subject clarification without research: %+v", turn)
					}
					var options []string
					for _, candidate := range turn.Candidates {
						options = append(options, candidate.Name)
					}
					slices.Sort(options)
					if !slices.Equal(options, s.wantCandidates) {
						t.Fatalf("expected candidates %v, got %v", s.wantCandidates, options)
					}
					if s.wantWindow != 0 {
						checkScope(t, turn, "", topic, s.wantWindow)
					}
				case "window":
					if turn.State != "clarify" || turn.TaskStatus != "" || turn.Result || turn.NeedsSubject ||
						!strings.Contains(turn.Message, "交易日") {
						t.Fatalf("expected a window clarification without research: %+v", turn)
					}
					checkScope(t, turn, s.wantSubject, topic, s.wantWindow)
				default:
					if !turn.Result || turn.TaskStatus != "COMPLETED" {
						t.Fatalf("expected a completed research result: %+v", turn)
					}
					checkScope(t, turn, s.wantSubject, topic, s.wantWindow)
				}
			}
		})
	}
}
