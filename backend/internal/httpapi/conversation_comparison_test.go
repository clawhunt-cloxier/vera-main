package httpapi

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation/conversationtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
)

func TestConversationStockComparison(t *testing.T) {
	const comparisonKeep = `,"industry_index":"keep","broad_index_add":[],"broad_index_remove":[],"clear_broad_indexes":"keep","default_broad_index":"keep","window_change":"keep"`
	const (
		initial = "演示制造近 10 个交易日的日成交"
		peer    = "相对同行呢？"
		peerBad = "和同行比，近 15 个交易日的股价"
		fixDays = "20 个交易日"
		named   = "比较演示银行和演示制造近 20 个交易日的股价"
		change  = "同一组，改成 60 个交易日"
		keep    = "就按这六十天，重新比较这两只"
		formula = "演示制造的这个涨跌幅差怎么算？"
		ignored = "比较演示银行、演示制造和演示设备甲"
		ticker  = "比较演示银行、演示制造和 699999.SH"
		bad     = "演示银行和演示制造过去一周的股价对比"
	)
	e := setupEngine(t, func(engine *harness.Engine) {
		conversationtest.UseRouter(engine.Gateway, map[string]model.ToolArguments{
			initial: {Name: "inspect_daily_trading", Arguments: `{"subject_mentions":["演示制造"],"subject_source":"message","trading_days":10,"advice":"none"}`},
			peer:    {Name: "compare_stock_performance", Arguments: `{"kind":"industry_peers","stock_mentions":[],"subject_source":"context","trading_days":0,"advice":"none"` + comparisonKeep + `}`},
			peerBad: {Name: "compare_stock_performance", Arguments: `{"kind":"industry_peers","stock_mentions":[],"subject_source":"context","trading_days":-1,"advice":"none"` + comparisonKeep + `}`},
			fixDays: {Name: "compare_stock_performance", Arguments: `{"kind":"previous","stock_mentions":[],"subject_source":"context","trading_days":20,"advice":"none"` + comparisonKeep + `}`},
			named:   {Name: "compare_stock_performance", Arguments: `{"kind":"stocks","stock_mentions":["演示制造","演示银行"],"subject_source":"message","trading_days":20,"advice":"none"` + comparisonKeep + `}`},
			change:  {Name: "compare_stock_performance", Arguments: `{"kind":"previous","stock_mentions":[],"subject_source":"context","trading_days":60,"advice":"none"` + comparisonKeep + `}`},
			keep:    {Name: "compare_stock_performance", Arguments: `{"kind":"previous","stock_mentions":[],"subject_source":"context","trading_days":0,"advice":"none"` + comparisonKeep + `}`},
			formula: {Name: "reference_research", Arguments: `{"kind":"calculation","subject_mention":"演示制造","advice":"none"}`},
			ignored: {Name: "compare_stock_performance", Arguments: `{"kind":"stocks","stock_mentions":["演示银行","演示制造"],"subject_source":"message","trading_days":0,"advice":"none"` + comparisonKeep + `}`},
			ticker:  {Name: "compare_stock_performance", Arguments: `{"kind":"stocks","stock_mentions":["演示银行","演示制造"],"subject_source":"message","trading_days":0,"advice":"none"` + comparisonKeep + `}`},
			bad:     {Name: "compare_stock_performance", Arguments: `{"kind":"stocks","stock_mentions":["演示银行","演示制造"],"subject_source":"message","trading_days":-1,"advice":"none"` + comparisonKeep + `}`},
		})
	})
	startConversationWorker(t, e)
	a := onboardAPIClient(t, e, "comparison-owner")
	b := onboardAPIClient(t, e, "comparison-other")
	contract := newContract(t)
	send := func(question string, conv uuid.UUID) (conversation.Accepted, chatReadTurn) {
		t.Helper()
		var accepted conversation.Accepted
		in := conversation.Message{Text: question, Automatic: true, ClientMessageID: uuid.New()}
		path := chatPath
		if conv != uuid.Nil {
			path += "/" + conv.String()
			var thread chatThread
			a.json("GET", path, nil, 200, &thread)
			path += "/turns"
			in.ExpectedRevision = thread.Conversation.Revision
		}
		a.json("POST", path, in, 202, &accepted)
		v := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(turn chatReadTurn) bool {
			return turn.State != "pending" && (turn.State != "research" || harness.IsTerminal(turn.TaskStatus))
		})
		return accepted, v
	}
	verifyResult := func(accepted conversation.Accepted, turn chatReadTurn, window, lines int) {
		t.Helper()
		if !turn.Result || turn.Scope == nil || turn.Scope.Topic != harness.ComparisonTopic || turn.Scope.Window != window ||
			turn.Scope.Comparison == nil {
			t.Fatalf("comparison result missing or wrong scope: %+v", turn)
		}
		path := chatPath + "/" + accepted.ConversationID.String() + "/turns/" + turn.ID.String()
		contract.check(t, schema("ConversationTurn"), a.json("GET", path, nil, 200, nil))
		var result struct {
			Version string             `json:"skill_version"`
			Claims  []harness.DocClaim `json:"claims"`
			Series  []facts.Series     `json:"series"`
			Tables  []facts.Table      `json:"tables"`
		}
		contract.check(t, schema("ConversationResult"), a.json("GET", path+"/result", nil, 200, &result))
		if result.Version != harness.ComparisonFixtureVersion || len(result.Series) != 1 || len(result.Series[0].Lines) != lines ||
			len(result.Tables) != 1 || len(result.Tables[0].Rows) != lines || len(result.Claims) != lines {
			t.Fatalf("incomplete comparison: %+v", result)
		}
		id := result.Claims[1].CalcRefs[0]
		var calc Calculation
		a.json("GET", "/api/v1/calculations/"+id.String(), nil, 200, &calc)
		if calc.FormulaID != "F13.excess_return" || len(calc.Inputs) != 4 {
			t.Fatalf("wrong relative return calculation: %+v", calc)
		}
		ev := result.Claims[1].EvidenceRefs[0]
		a.json("GET", "/api/v1/evidence/"+ev.String(), nil, 200, nil)
		b.json("GET", "/api/v1/evidence/"+ev.String(), nil, 404, nil)
		b.json("GET", "/api/v1/calculations/"+id.String(), nil, 404, nil)
		b.json("GET", path+"/result", nil, 404, nil)
	}
	first, initialTurn := send(initial, uuid.Nil)
	if !initialTurn.Result {
		t.Fatalf("first result missing: %+v", initialTurn)
	}
	peers, peerTurn := send(peer, first.ConversationID)
	verifyResult(peers, peerTurn, 10, 4)
	if peerTurn.Scope.SubjectID != initialTurn.Scope.SubjectID || peerTurn.Scope.Comparison.SampleDate == "" {
		t.Fatalf("peer selection lost the anchor or sample date: %+v", peerTurn.Scope)
	}
	if _, rejection := send(peerBad, first.ConversationID); rejection.State != "clarify" || rejection.TaskStatus != "" {
		t.Fatalf("unsupported peer window queued research: %+v", rejection)
	}
	resumed, resumedTurn := send(fixDays, first.ConversationID)
	verifyResult(resumed, resumedTurn, 20, 4)
	explicit, original := send(named, uuid.Nil)
	verifyResult(explicit, original, 20, 2)
	bank, err := e.engine.Store.ResolveAlias(t.Context(), "演示银行", "stock")
	if err != nil || len(bank) != 1 || original.Scope.SubjectID != bank[0].ID {
		t.Fatalf("comparison baseline must follow the order of the user's message: %+v (%v)", original.Scope, err)
	}
	revised, updated := send(change, explicit.ConversationID)
	verifyResult(revised, updated, 60, 2)
	if updated.Scope.SubjectID != original.Scope.SubjectID || !slices.Equal(updated.Scope.Comparison.OtherIDs, original.Scope.Comparison.OtherIDs) {
		t.Fatalf("comparison set changed: %+v vs %+v", updated.Scope, original.Scope)
	}
	kept, keptTurn := send(keep, explicit.ConversationID)
	verifyResult(kept, keptTurn, 60, 2)
	if _, reference := send(formula, explicit.ConversationID); reference.State != "reference" || reference.Reference == nil || *reference.Reference != keptTurn.ID {
		t.Fatalf("the secondary subject did not reference the original calculation: %+v", reference)
	}
	for _, input := range []string{ignored, ticker, bad} {
		if _, rejection := send(input, uuid.Nil); rejection.State != "clarify" || rejection.TaskStatus != "" {
			t.Fatalf("unverified scope queued research: %+v", rejection)
		}
	}
}
