package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/shopspring/decimal"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/conversation"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/facts"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/identity"
)

const chatPath = "/api/v1/conversations"

type chatThread struct {
	Conversation conversation.Summary `json:"conversation"`
	Turns        []chatReadTurn       `json:"turns"`
}

type chatReadTurn struct {
	ID           uuid.UUID           `json:"id"`
	State        string              `json:"state"`
	Revision     int64               `json:"revision"`
	Plan         *ResearchPlan       `json:"plan"`
	TaskRevision *int64              `json:"task_revision"`
	TaskStatus   string              `json:"task_status"`
	Result       bool                `json:"result_ready"`
	Reference    *uuid.UUID          `json:"reference_turn_id"`
	Kind         string              `json:"reference_kind"`
	Candidates   []Entity            `json:"candidates"`
	Scope        *conversation.Scope `json:"scope"`
}

func chatAwait(t *testing.T, c *client, conv, turn uuid.UUID, ready func(chatReadTurn) bool) chatReadTurn {
	t.Helper()
	var last chatReadTurn
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); {
		c.json("GET", chatPath+"/"+conv.String()+"/turns/"+turn.String(), nil, 200, &last)
		if ready(last) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("last turn: %+v", last)
	return chatReadTurn{}
}

func TestConversationAPIWithRiver(t *testing.T) {
	e := setup(t)
	contract := newContract(t)
	ctx := context.Background()
	startConversationWorker(t, e)
	codes, err := identity.CreateInvites(ctx, e.pool, 2, 1, time.Hour, "conversation test", "test")
	if err != nil {
		t.Fatal(err)
	}
	a, b := e.login(t, "chat-alice"), e.login(t, "chat-bob")
	var me Me
	a.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, &me)
	b.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[1], "persona": "analyst"}, 200, nil)
	if len(me.ConversationTopics) != 2 {
		t.Fatalf("capabilities: %v", me.ConversationTopics)
	}

	first := conversation.Message{ClientMessageID: uuid.New(), Text: "演示制造近 20 个交易日的融资余额怎么变"}
	var accepted, replay conversation.Accepted
	body := a.json("POST", chatPath, first, 202, &accepted)
	contract.check(t, schema("ConversationAccepted"), body)
	a.json("POST", chatPath, first, 202, &replay)
	if replay != accepted {
		t.Fatalf("message replay: %+v %+v", accepted, replay)
	}
	threadPath := chatPath + "/" + accepted.ConversationID.String()
	turnPath := threadPath + "/turns/" + accepted.TurnID.String()
	if status, _, headers := a.do("POST", chatPath, first, nil); status != 202 || headers.Get("Location") != turnPath {
		t.Fatalf("first message accepted without a matching Location: %d, %s", status, headers.Get("Location"))
	}
	a.json("POST", chatPath, map[string]any{"client_message_id": uuid.New(), "text": first.Text, "expected_revision": 1}, 400, nil)
	a.json("POST", threadPath+"/turns", map[string]any{"client_message_id": uuid.New(), "text": "这个数怎么算的"}, 400, nil)
	conflicting := first
	conflicting.Text = "换成另一个请求"
	a.json("POST", chatPath, conflicting, 409, nil)
	b.json("GET", threadPath, nil, 404, nil)
	b.json("GET", turnPath, nil, 404, nil)
	b.json("DELETE", threadPath, nil, 404, nil)
	b.json("POST", threadPath+"/turns", conversation.Message{ClientMessageID: uuid.New(), Text: "还有呢", ExpectedRevision: 1}, 404, nil)
	history := b.json("GET", chatPath, nil, 200, nil)
	if strings.Contains(string(history), accepted.ConversationID.String()) {
		t.Fatal("another workspace saw the conversation")
	}

	planTurn := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if planTurn.Plan == nil || !planTurn.Plan.Executable || planTurn.Scope.Window != 20 {
		t.Fatalf("plan: %+v", planTurn)
	}
	body = a.json("GET", turnPath, nil, 200, nil)
	contract.check(t, schema("ConversationTurn"), body)
	a.json("GET", "/api/v1/research-plans/"+planTurn.Plan.ID.String(), nil, 404, nil)
	confirm := conversationAction{Action: "confirm", PlanID: &planTurn.Plan.ID, PlanHash: planTurn.Plan.PlanHash, ExpectedRevision: planTurn.Revision}
	a.json("POST", turnPath+"/actions", conversationAction{Action: "confirm", ExpectedRevision: planTurn.Revision}, 400, nil)
	a.json("POST", turnPath+"/actions", conversationAction{Action: "choose", ExpectedRevision: planTurn.Revision}, 400, nil)
	b.json("POST", turnPath+"/actions", confirm, 404, nil)
	codesCh := make(chan int, 2)
	for range 2 {
		go func() { code, _, _ := a.do("POST", turnPath+"/actions", confirm, nil); codesCh <- code }()
	}
	for range 2 {
		if code := <-codesCh; code != http.StatusNoContent {
			t.Fatalf("confirm code %d", code)
		}
	}
	var tasks int
	if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM research_tasks WHERE plan_id=$1", planTurn.Plan.ID).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("task count %d: %v", tasks, err)
	}
	done := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return harness.IsTerminal(v.TaskStatus) })
	if !done.Result {
		t.Fatalf("research did not deliver: %+v", done)
	}
	type projection struct {
		ID       uuid.UUID          `json:"report_id"`
		Original uuid.UUID          `json:"original_turn_id"`
		Version  int                `json:"version"`
		Claims   []harness.DocClaim `json:"claims"`
		Series   []facts.Series     `json:"series"`
		Tables   []facts.Table      `json:"tables"`
	}
	var result projection
	body = a.json("GET", turnPath+"/result", nil, 200, &result)
	contract.check(t, schema("ConversationResult"), body)
	b.json("GET", turnPath+"/result", nil, 404, nil)
	if len(result.Claims) == 0 || len(result.Series) != 1 || len(result.Tables) != 1 {
		t.Fatalf("missing brief: %+v", result)
	}
	points, table := result.Series[0].Lines[0].Points, result.Tables[0]
	if len(points) != 20 || len(table.Rows) != 20 {
		t.Fatalf("wrong window: %d %d", len(points), len(table.Rows))
	}
	for i, p := range points {
		if p[1] != nil && (table.Rows[i][1] == nil || *p[1] != *table.Rows[i][1]) {
			t.Fatalf("chart table mismatch at %d", i)
		}
	}
	var evidenceID uuid.UUID
	for _, id := range table.Evidence {
		evidenceID = id
	}
	var frozen string
	if err := e.pool.QueryRow(ctx, `SELECT payload->>'financing_balance' FROM records WHERE id=$1`, evidenceID).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	if amount := points[len(points)-1][1]; amount == nil || decimal.RequireFromString(*amount).Shift(4).String() != decimal.RequireFromString(frozen).String() {
		t.Fatalf("chart value does not match frozen balance %s", frozen)
	}
	reportPath := fmt.Sprintf("/api/v1/reports/%s/versions/%d", result.ID, result.Version)
	a.json("GET", reportPath+"/series/"+result.Series[0].ID, nil, 200, nil)
	a.json("GET", "/api/v1/evidence/"+evidenceID.String(), nil, 200, nil)
	b.json("GET", "/api/v1/evidence/"+evidenceID.String(), nil, 404, nil)
	var calcID uuid.UUID
	for _, c := range result.Claims {
		if len(c.CalcRefs) > 0 {
			calcID = c.CalcRefs[0]
			break
		}
	}
	if calcID == uuid.Nil {
		t.Fatal("the financing balance brief omitted the calculation")
	}
	a.json("GET", "/api/v1/calculations/"+calcID.String(), nil, 200, nil)
	b.json("GET", "/api/v1/calculations/"+calcID.String(), nil, 404, nil)
	if reports := a.json("GET", "/api/v1/reports", nil, 200, nil); strings.Contains(string(reports), result.ID.String()) {
		t.Fatal("short report is in the formal report list")
	}

	send := func(question string, reply *uuid.UUID) (conversation.Accepted, string) {
		t.Helper()
		var thread chatThread
		body := a.json("GET", threadPath, nil, 200, &thread)
		contract.check(t, schema("ConversationThread"), body)
		var v conversation.Accepted
		a.json("POST", threadPath+"/turns", conversation.Message{ClientMessageID: uuid.New(), Text: question, ReplyTo: reply, ExpectedRevision: thread.Conversation.Revision}, 202, &v)
		return v, threadPath + "/turns/" + v.TurnID.String()
	}
	ref, refPath := send("这个数怎么算的", &accepted.TurnID)
	refTurn := chatAwait(t, a, ref.ConversationID, ref.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if refTurn.State != "reference" || refTurn.Kind != "calculation" || refTurn.Reference == nil || *refTurn.Reference != accepted.TurnID {
		t.Fatalf("reference: %+v", refTurn)
	}
	bodied := a.json("GET", refPath+"/result", nil, 200, nil)
	if !json.Valid(bodied) {
		t.Fatal("invalid projection")
	}
	var original projection
	if err := json.Unmarshal(bodied, &original); err != nil || original.ID != result.ID {
		t.Fatalf("anchor changed report: %s", bodied)
	}

	change, changePath := send("改成 60 个交易日", nil)
	changeTurn := chatAwait(t, a, change.ConversationID, change.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if changeTurn.Plan == nil || changeTurn.Scope.Window != 60 || changeTurn.Scope.SubjectID != planTurn.Scope.SubjectID {
		t.Fatalf("context plan: %+v", changeTurn)
	}
	a.json("POST", changePath+"/actions", conversationAction{Action: "cancel", ExpectedRevision: changeTurn.Revision}, 204, nil)
	changed := chatAwait(t, a, change.ConversationID, change.TurnID, func(v chatReadTurn) bool { return v.State == "unavailable" })
	if changed.Result {
		t.Fatal("cancelled plan has a result")
	}

	daily, dailyPath := send("这只股票的日成交呢", nil)
	dayTurn := chatAwait(t, a, daily.ConversationID, daily.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if dayTurn.Plan == nil || dayTurn.Scope.Topic != harness.DailyTradingTopic {
		t.Fatalf("daily plan: %+v", dayTurn)
	}
	a.json("POST", dailyPath+"/actions", conversationAction{Action: "confirm", PlanID: &dayTurn.Plan.ID, PlanHash: dayTurn.Plan.PlanHash, ExpectedRevision: dayTurn.Revision}, 204, nil)
	if v := chatAwait(t, a, daily.ConversationID, daily.TurnID, func(v chatReadTurn) bool { return harness.IsTerminal(v.TaskStatus) }); !v.Result {
		t.Fatalf("daily research: %+v", v)
	}

	ambiguous, ambiguousPath := send("演示制造和演示设备甲的成交额", nil)
	pick := chatAwait(t, a, ambiguous.ConversationID, ambiguous.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if pick.State != "clarify" || len(pick.Candidates) != 2 {
		t.Fatalf("ambiguous security: %+v", pick)
	}
	a.json("POST", ambiguousPath+"/actions", conversationAction{Action: "choose", SubjectID: &planTurn.Scope.SubjectID, ExpectedRevision: pick.Revision}, 204, nil)
	picked := chatAwait(t, a, ambiguous.ConversationID, ambiguous.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if picked.Plan == nil || picked.Plan.Subject.ID != planTurn.Scope.SubjectID {
		t.Fatalf("user candidate selection was not honored: %+v", picked)
	}
	if err := e.engine.River.QueuePause(ctx, "research", nil); err != nil {
		t.Fatal(err)
	}
	a.json("POST", ambiguousPath+"/actions", conversationAction{Action: "confirm", PlanID: &picked.Plan.ID, PlanHash: picked.Plan.PlanHash, ExpectedRevision: picked.Revision}, 204, nil)
	queued := chatAwait(t, a, ambiguous.ConversationID, ambiguous.TurnID, func(v chatReadTurn) bool { return v.TaskStatus == "QUEUED" })
	a.json("POST", ambiguousPath+"/actions", conversationAction{Action: "cancel", ExpectedRevision: queued.Revision, TaskRevision: queued.TaskRevision}, 204, nil)
	cancelled := chatAwait(t, a, ambiguous.ConversationID, ambiguous.TurnID, func(v chatReadTurn) bool { return v.TaskStatus == "CANCELLED" })
	if cancelled.Result {
		t.Fatal("cancelled task has a deliverable")
	}
	if err := e.engine.River.QueueResume(ctx, "research", nil); err != nil {
		t.Fatal(err)
	}

	full, fullPath := send("为演示制造生成完整个股研报", nil)
	fullPlan := chatAwait(t, a, full.ConversationID, full.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if fullPlan.Plan == nil || fullPlan.Scope.Topic != "" {
		t.Fatalf("expected a formal report plan: %+v", fullPlan)
	}
	a.json("POST", fullPath+"/actions", conversationAction{Action: "confirm", PlanID: &fullPlan.Plan.ID, PlanHash: fullPlan.Plan.PlanHash, ExpectedRevision: fullPlan.Revision}, 204, nil)
	if v := chatAwait(t, a, full.ConversationID, full.TurnID, func(v chatReadTurn) bool { return harness.IsTerminal(v.TaskStatus) }); !v.Result {
		t.Fatalf("formal report: %+v", v)
	}
	var fullResult projection
	a.json("GET", fullPath+"/result", nil, 200, &fullResult)
	fullReportPath := fmt.Sprintf("/api/v1/reports/%s/versions/%d", fullResult.ID, fullResult.Version)
	code, pdf, headers := a.do("GET", fullReportPath+"/pdf", nil, nil)
	if code != 200 || headers.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Fatalf("formal PDF: %d %v", code, headers)
	}
	var quotaBefore Me
	a.json("GET", "/api/v1/me", nil, 200, &quotaBefore)
	a.json("DELETE", threadPath, nil, 204, nil)
	a.json("GET", turnPath+"/result", nil, 404, nil)
	a.json("GET", refPath+"/result", nil, 404, nil)
	a.json("GET", reportPath, nil, 404, nil)
	a.json("GET", reportPath+"/series/"+result.Series[0].ID, nil, 404, nil)
	a.json("GET", "/api/v1/calculations/"+calcID.String(), nil, 404, nil)
	// The independently retained formal report also cites this vendor record.
	a.json("GET", "/api/v1/evidence/"+evidenceID.String(), nil, 200, nil)
	history = a.json("GET", chatPath, nil, 200, nil)
	if strings.Contains(string(history), accepted.ConversationID.String()) {
		t.Fatal("deleted thread in history")
	}
	awaitConversationPurge(t, e, accepted.ConversationID)
	var quotaAfter Me
	a.json("GET", "/api/v1/me", nil, 200, &quotaAfter)
	if quotaAfter.Quota.DailyRemaining != quotaBefore.Quota.DailyRemaining {
		t.Fatal("purging chat restored spent research quota")
	}
	a.json("GET", fullReportPath, nil, 200, nil)
	code, pdf, headers = a.do("GET", fullReportPath+"/pdf", nil, nil)
	if code != 200 || headers.Get("Content-Type") != "application/pdf" || !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Fatalf("retained formal PDF: %d %v", code, headers)
	}

	var expiring conversation.Accepted
	a.json("POST", chatPath, conversation.Message{ClientMessageID: uuid.New(), Text: "演示制造的日成交"}, 202, &expiring)
	otherPath := chatPath + "/" + expiring.ConversationID.String()
	a.json("GET", otherPath, nil, 200, nil)
	if _, err := e.pool.Exec(ctx, "UPDATE conversations SET expires_at=now()-interval '1 second' WHERE id=$1", expiring.ConversationID); err != nil {
		t.Fatal(err)
	}
	a.json("GET", otherPath, nil, 404, nil)
	awaitConversationPurge(t, e, expiring.ConversationID)
}

func TestConversationFormalDeliveryAfterDeletionOrExpiry(t *testing.T) {
	ctx := context.Background()
	e := setup(t)
	// Omit the periodic job and research dispatch so we can interleave user
	// commands with a specific execution attempt and assert before cleanup.
	register := conversation.Register(e.engine)
	worker, err := e.engine.NewRiver(e.pool, true, 1, func(workers *river.Workers, cfg *river.Config) {
		register(workers, cfg)
		cfg.PeriodicJobs = nil
		delete(cfg.Queues, "research")
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := worker.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	user := e.login(t, "chat-formal-cancel")
	codes, err := identity.CreateInvites(ctx, e.pool, 1, 1, time.Hour, "conversation test", "test")
	if err != nil {
		t.Fatal(err)
	}
	user.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, nil)
	for _, scenario := range []string{"queued_delete", "expired_before_delivery"} {
		t.Run(scenario, func(t *testing.T) {
			a := &client{t: t, e: e, http: user.http, csrf: user.csrf}
			first := conversation.Message{ClientMessageID: uuid.New(), Text: "为演示制造生成完整个股研报"}
			var accepted conversation.Accepted
			a.json("POST", chatPath, first, 202, &accepted)
			path := chatPath + "/" + accepted.ConversationID.String()
			turnPath := path + "/turns/" + accepted.TurnID.String()
			v := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
			if v.Plan == nil || !v.Plan.Executable {
				t.Fatalf("formal plan: %+v", v)
			}
			a.json("POST", turnPath+"/actions", conversationAction{Action: "confirm", PlanID: &v.Plan.ID, PlanHash: v.Plan.PlanHash, ExpectedRevision: v.Revision}, 204, nil)
			var ws, task uuid.UUID
			if err := e.pool.QueryRow(ctx, "SELECT workspace_id,task_id FROM conversation_turns WHERE id=$1", accepted.TurnID).Scan(&ws, &task); err != nil {
				t.Fatal(err)
			}
			state := func(want string, wantEpoch int64) {
				t.Helper()
				var status string
				var epoch int64
				var report *uuid.UUID
				err := e.pool.QueryRow(ctx, "SELECT status,epoch,report_id FROM research_tasks WHERE id=$1", task).Scan(&status, &epoch, &report)
				if err != nil || status != want || epoch != wantEpoch || report != nil {
					t.Fatalf("state=%s epoch=%d report=%v: %v", status, epoch, report, err)
				}
			}
			state("QUEUED", 1)
			if scenario == "expired_before_delivery" {
				if _, err := e.pool.Exec(ctx, "UPDATE conversations SET expires_at=now()-interval '1 second' WHERE id=$1", accepted.ConversationID); err != nil {
					t.Fatal(err)
				}
				a.json("GET", path, nil, 404, nil)
				if err := e.engine.Run(ctx, ws, task, 1); err != nil {
					t.Fatal(err)
				}
				state("ASSEMBLING", 1)
			}
			a.json("DELETE", path, nil, 204, nil)
			if scenario == "queued_delete" {
				state("CANCELLED", 2)
				a.json("POST", chatPath, first, 404, nil)
			} else {
				var status string
				var epoch int64
				var report *uuid.UUID
				if err := e.pool.QueryRow(ctx, "SELECT status,epoch,report_id FROM research_tasks WHERE id=$1", task).Scan(&status, &epoch, &report); err != nil ||
					(status != "CANCEL_REQUESTED" && status != "CANCELLED") || epoch != 2 || report != nil {
					t.Fatalf("expired task after deletion: %s, epoch %d, report %v: %v", status, epoch, report, err)
				}
			}
			if err := e.engine.Run(ctx, ws, task, 1); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := e.pool.QueryRow(ctx, "SELECT count(*) FROM report_versions WHERE task_id=$1", task).Scan(&count); err != nil || count != 0 {
				t.Fatalf("hidden conversation delivered a report: %d %v", count, err)
			}
		})
	}
}

type delayedConversationModel struct {
	started chan struct{}
	resume  chan struct{}
}

func (delayedConversationModel) Name() string { return "openai" }

func (d delayedConversationModel) Do(_ context.Context, call model.Call) (model.Raw, error) {
	d.started <- struct{}{}
	<-d.resume // The request was sent and the outcome arrives after local cancellation.
	if call.Messages[0].Content == "unknown" {
		return model.Raw{}, &model.Error{Unknown: true, Msg: "synthetic vendor error detail"}
	}
	return model.Raw{Content: `{"text":"synthetic vendor response"}`, FinishReason: "stop",
		Usage: model.Usage{PromptTokens: 100, CompletionTokens: 20}}, nil
}

func TestConversationPurgeFencesLateModelCalls(t *testing.T) {
	e := setup(t)
	startConversationWorker(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := e.engine.River.QueuePause(ctx, "research", nil); err != nil {
		t.Fatal(err)
	}
	a := e.login(t, "chat-late-model")
	codes, err := identity.CreateInvites(ctx, e.pool, 1, 1, time.Hour, "conversation test", "test")
	if err != nil {
		t.Fatal(err)
	}
	a.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, nil)
	var accepted conversation.Accepted
	a.json("POST", chatPath, conversation.Message{ClientMessageID: uuid.New(), Text: "演示制造的融资余额"}, 202, &accepted)
	path := chatPath + "/" + accepted.ConversationID.String()
	v := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if v.Plan == nil || !v.Plan.Executable {
		t.Fatalf("short plan: %+v", v)
	}
	a.json("POST", path+"/turns/"+accepted.TurnID.String()+"/actions", conversationAction{
		Action: "confirm", PlanID: &v.Plan.ID, PlanHash: v.Plan.PlanHash, ExpectedRevision: v.Revision,
	}, 204, nil)
	var ws, task uuid.UUID
	var epoch int64
	// Keep the real execution job parked and simulate the stage with two in-flight model reservations.
	if err := e.pool.QueryRow(ctx, `UPDATE research_tasks SET status='ANALYZING' WHERE plan_id=$1
		RETURNING workspace_id,id,epoch`, v.Plan.ID).Scan(&ws, &task, &epoch); err != nil {
		t.Fatal(err)
	}
	adapter := delayedConversationModel{started: make(chan struct{}, 2), resume: make(chan struct{})}
	var priceTable model.PriceTable
	if err := json.Unmarshal([]byte(`{"models":{"test":{"peak":{"cache_miss_input":"1","output":"1"},"off_peak":{"cache_miss_input":"1","output":"1"}}}}`), &priceTable); err != nil {
		t.Fatal(err)
	}
	gw := model.NewGateway(e.pool, map[string]model.Profile{"writer": {Name: "writer", Adapter: "openai", ModelID: "test"}},
		map[string]model.Adapter{"openai": adapter}, &priceTable, nil, e.engine.Policy, decimal.NewFromInt(30))
	req := model.Request{Workspace: ws, Task: &task, TaskEpoch: epoch, Profile: "writer", StepKey: "stale", MaxOutput: 100,
		Budget: e.engine.Policy.Budgets["capital_flow"]}
	callCtx, stopCalls := context.WithCancel(ctx)
	defer stopCalls()
	finished := make(chan error, 2)
	for _, step := range []string{"paid", "unknown"} {
		go func() {
			call := req
			call.StepKey, call.Messages = step, []model.Message{{Role: "user", Content: step}}
			_, _, err := gw.Complete(callCtx, call)
			finished <- err
		}()
	}
	var released bool
	defer func() {
		if !released {
			close(adapter.resume)
		}
	}()
	for range 2 {
		select {
		case <-adapter.started:
		case err := <-finished:
			t.Fatalf("model did not reach simulated vendor: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	a.json("DELETE", path, nil, 204, nil)
	checkStale := func(stage string) {
		t.Helper()
		if _, _, err := gw.Complete(ctx, req); !errors.Is(err, model.ErrStaleTask) {
			t.Fatalf("stale reservation %s: %v", stage, err)
		}
	}
	checkStale("after cancellation")
	if err := e.engine.River.QueueResume(ctx, "research", nil); err != nil {
		t.Fatal(err)
	}
	for start := time.Now(); ; {
		var status string
		if err := e.pool.QueryRow(ctx, "SELECT status FROM research_tasks WHERE id=$1", task).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "CANCELLED" {
			break
		}
		if time.Since(start) > 8*time.Second {
			t.Fatalf("task cancellation did not finish: %s", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	awaitConversationPurge(t, e, accepted.ConversationID)
	checkStale("after cleanup")
	stopCalls()
	close(adapter.resume)
	released = true
	for range 2 {
		select {
		case err := <-finished:
			var unknown *model.Error
			if err != nil && (!errors.As(err, &unknown) || !unknown.Unknown) {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var paid, held, present int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='SUCCEEDED' AND cost_usd=0.000120 AND (usage->>'prompt_tokens')::int=100),
		count(*) FILTER (WHERE status='UNKNOWN' AND cost_usd IS NULL AND reserved_usd>0), count(*)
		FROM model_calls WHERE task_id=$1 AND content_erased_at IS NOT NULL AND response IS NULL AND error IS NULL`, task).Scan(&paid, &held, &present); err != nil ||
		paid != 1 || held != 1 || present != 2 {
		t.Fatalf("retained ledger: paid=%d held=%d erased=%d: %v", paid, held, present, err)
	}
	ids, err := gw.ReleaseUnknown(ctx, time.Now().Add(time.Minute))
	var ledgerCount, reconciled int
	if err != nil || len(ids) != 1 {
		t.Fatalf("unknown reconciliation: %v %v", ids, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status='RECONCILED' AND cost_usd=0 AND response IS NULL AND error IS NULL)
		FROM model_calls WHERE task_id=$1`, task).Scan(&ledgerCount, &reconciled); err != nil || ledgerCount != 2 || reconciled != 1 {
		t.Fatalf("final ledger: calls=%d reconciled=%d: %v", ledgerCount, reconciled, err)
	}
}

func TestCloseConversationSourceWithRiver(t *testing.T) {
	ctx := context.Background()
	e := setupEngine(t, func(engine *harness.Engine) {
		// Deliberately reclassify the test rows in an isolated database to
		// exercise vendor permission gates without any vendor credentials.
		for _, sql := range []string{
			"INSERT INTO providers(id,name,kind,synthetic,allow_model_processing) VALUES('tushare','Mock close source for test','vendor',false,true) ON CONFLICT DO NOTHING",
			"UPDATE records SET provider_id='tushare' WHERE provider_id='fixture'",
			"UPDATE entities SET synthetic=false",
		} {
			if _, err := engine.Pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
		}
		engine.Store = dataservice.NewResearchStore(engine.Pool, engine.Store.Catalog(), "tushare", []string{"DM0001.DEMO"}, true)
		engine.Synthetic, engine.RealResearch = false, true
		engine.Skills = harness.UnifiedCloseSkills()
		engine.WithCloseResearchUse = engine.Store.WithResearchUse
		engine.CloseResearchCutoff = func(context.Context) (time.Time, error) { return engine.Now(), nil }
	})
	startConversationWorker(t, e)
	a := e.login(t, "chat-vendor")
	codes, err := identity.CreateInvites(ctx, e.pool, 1, 1, time.Hour, "conversation test", "test")
	if err != nil {
		t.Fatal(err)
	}
	a.json("POST", "/api/v1/onboarding", map[string]string{"invite_code": codes[0], "persona": "analyst"}, 200, nil)
	var accepted conversation.Accepted
	a.json("POST", chatPath, conversation.Message{ClientMessageID: uuid.New(), Text: "演示制造近 20 个交易日融资余额"}, 202, &accepted)
	threadPath := chatPath + "/" + accepted.ConversationID.String()
	turnPath := threadPath + "/turns/" + accepted.TurnID.String()
	turn := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if turn.Plan == nil || turn.Plan.SkillVersion != harness.ConversationCloseVersion || !turn.Plan.Executable {
		t.Fatalf("close plan: %+v", turn)
	}
	changeGrant := func(allowed bool) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, "UPDATE providers SET allow_model_processing=$1 WHERE id='tushare'", allowed); err != nil {
			t.Fatal(err)
		}
	}
	confirm := conversationAction{Action: "confirm", PlanID: &turn.Plan.ID, PlanHash: turn.Plan.PlanHash, ExpectedRevision: turn.Revision}
	changeGrant(false)
	a.json("GET", turnPath, nil, 503, nil)
	a.json("POST", turnPath+"/actions", confirm, 503, nil)
	changeGrant(true)
	a.json("POST", turnPath+"/actions", confirm, 204, nil)
	if v := chatAwait(t, a, accepted.ConversationID, accepted.TurnID, func(v chatReadTurn) bool { return harness.IsTerminal(v.TaskStatus) }); !v.Result {
		t.Fatalf("vendor result: %+v", v)
	}

	var result struct {
		ID      uuid.UUID          `json:"report_id"`
		Version int                `json:"version"`
		Claims  []harness.DocClaim `json:"claims"`
		Series  []facts.Series     `json:"series"`
	}
	a.json("GET", turnPath+"/result", nil, 200, &result)
	var thread chatThread
	a.json("GET", threadPath, nil, 200, &thread)
	var ref conversation.Accepted
	a.json("POST", threadPath+"/turns", conversation.Message{ClientMessageID: uuid.New(), ReplyTo: &accepted.TurnID, ExpectedRevision: thread.Conversation.Revision, Text: "这个数怎么算的"}, 202, &ref)
	if v := chatAwait(t, a, ref.ConversationID, ref.TurnID, func(v chatReadTurn) bool { return v.State != "pending" }); v.State != "reference" {
		t.Fatalf("anchor: %+v", v)
	}
	reportPath := fmt.Sprintf("/api/v1/reports/%s/versions/%d", result.ID, result.Version)
	var calc uuid.UUID
	for _, claim := range result.Claims {
		if len(claim.CalcRefs) > 0 {
			calc = claim.CalcRefs[0]
			break
		}
	}
	if len(result.Series) != 1 || len(result.Series[0].Evidence) == 0 || calc == uuid.Nil {
		t.Fatalf("missing evidence: %+v", result)
	}
	evidence := result.Series[0].Evidence[0]
	paths := []string{turnPath + "/result", threadPath + "/turns/" + ref.TurnID.String() + "/result", reportPath,
		reportPath + "/series/" + result.Series[0].ID, "/api/v1/evidence/" + evidence.String(), "/api/v1/calculations/" + calc.String()}
	for _, path := range paths {
		a.json("GET", path, nil, 200, nil)
	}
	changeGrant(false)
	for _, path := range paths {
		a.json("GET", path, nil, 503, nil)
	}
	changeGrant(true)
	// A second, narrower snapshot cites some of the same days. Quarantining
	// an older day hides the first result without masking the usable citation.
	var narrow conversation.Accepted
	a.json("POST", chatPath, conversation.Message{ClientMessageID: uuid.New(), Text: "演示制造近 5 个交易日融资余额"}, 202, &narrow)
	narrowThread := chatPath + "/" + narrow.ConversationID.String()
	narrowTurn := narrowThread + "/turns/" + narrow.TurnID.String()
	v := chatAwait(t, a, narrow.ConversationID, narrow.TurnID, func(v chatReadTurn) bool { return v.State != "pending" })
	if v.Plan == nil || !v.Plan.Executable {
		t.Fatalf("narrow plan: %+v", v)
	}
	a.json("POST", narrowTurn+"/actions", conversationAction{Action: "confirm", PlanID: &v.Plan.ID, PlanHash: v.Plan.PlanHash, ExpectedRevision: v.Revision}, 204, nil)
	if v := chatAwait(t, a, narrow.ConversationID, narrow.TurnID, func(v chatReadTurn) bool { return harness.IsTerminal(v.TaskStatus) }); !v.Result {
		t.Fatalf("narrow result: %+v", v)
	}
	var narrowResult struct{ Series []facts.Series }
	a.json("GET", narrowTurn+"/result", nil, 200, &narrowResult)
	if len(narrowResult.Series) != 1 || len(narrowResult.Series[0].Evidence) == 0 {
		t.Fatalf("narrow evidence: %+v", narrowResult)
	}
	shared := narrowResult.Series[0].Evidence[0]
	if !slices.Contains(result.Series[0].Evidence, shared) {
		t.Fatal("expected shared evidence across snapshots")
	}
	sibling := slices.IndexFunc(result.Series[0].Evidence, func(id uuid.UUID) bool {
		return !slices.Contains(narrowResult.Series[0].Evidence, id)
	})
	if sibling < 0 {
		t.Fatal("expected an earlier day exclusive to the original snapshot")
	}
	if _, err := e.pool.Exec(ctx, "UPDATE records SET quality_state='QUARANTINED' WHERE id=$1", result.Series[0].Evidence[sibling]); err != nil {
		t.Fatal(err)
	}
	for _, path := range slices.Delete(slices.Clone(paths), 4, 5) {
		a.json("GET", path, nil, 503, nil)
	}
	sharedPath := "/api/v1/evidence/" + shared.String()
	a.json("GET", sharedPath, nil, 200, nil)
	if _, err := e.pool.Exec(ctx, "UPDATE records SET quality_state='QUARANTINED' WHERE id=$1", shared); err != nil {
		t.Fatal(err)
	}
	a.json("GET", sharedPath, nil, 503, nil)
	if _, err := e.pool.Exec(ctx, "UPDATE conversations SET expires_at=now()-interval '1 second' WHERE id=$1", accepted.ConversationID); err != nil {
		t.Fatal(err)
	}
	changeGrant(false)
	for _, path := range slices.Delete(slices.Clone(paths), 4, 5) {
		a.json("GET", path, nil, 404, nil)
	}
	a.json("DELETE", narrowThread, nil, 204, nil)
	a.json("GET", sharedPath, nil, 404, nil)
}

func startConversationWorker(t *testing.T, e *env) {
	t.Helper()
	worker, err := e.engine.NewRiver(e.pool, true, 1, conversation.Register(e.engine))
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Stop(stop); err != nil {
			t.Error(err)
		}
	})
}

type retentionProbe struct{}

func (retentionProbe) Kind() string { return "conversation.retention" }

func awaitConversationPurge(t *testing.T, e *env, conversationID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.engine.River.Insert(ctx, retentionProbe{}, nil); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(8 * time.Second); time.Now().Before(end); {
		var found bool
		if err := e.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM conversations WHERE id=$1)", conversationID).Scan(&found); err != nil {
			t.Fatal(err)
		}
		if !found {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("retention did not purge %s", conversationID)
}
