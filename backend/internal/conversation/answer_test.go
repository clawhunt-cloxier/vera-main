package conversation

import (
	"errors"
	"testing"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/harness/model"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
)

func TestConversationDispatchLimitPersists(t *testing.T) {
	pool := dbtest.New(t)
	var args resolveArgs
	if err := pool.QueryRow(t.Context(), `WITH u AS (
		INSERT INTO users(issuer,subject) VALUES('test','chat-router-bound') RETURNING id
	), w AS (
		INSERT INTO workspaces(owner_user_id) SELECT id FROM u RETURNING id
	), c AS (
		INSERT INTO conversations(workspace_id,title) SELECT id,'test' FROM w RETURNING id,workspace_id
	)
	INSERT INTO conversation_turns(workspace_id,conversation_id,client_message_id,request_hash,ordinal,channel,body,state)
	SELECT workspace_id,id,uuidv7(),'test',1,'web','test','pending' FROM c
	RETURNING workspace_id,conversation_id,id,revision`).Scan(&args.Workspace, &args.Conversation, &args.Turn, &args.Revision); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s := Service{Engine: &harness.Engine{Pool: pool}}
		if err := s.reserveAgentDispatch(t.Context(), args, 2); err != nil {
			t.Fatal(err)
		}
	}
	redelivery := Service{Engine: &harness.Engine{Pool: pool}}
	if err := redelivery.reserveAgentDispatch(t.Context(), args, 2); !errors.Is(err, model.ErrBudget) {
		t.Fatalf("redelivery exceeded persisted dispatch limit: %v", err)
	}
}

func TestConversationAnswerBoundary(t *testing.T) {
	for _, topic := range educationTopics {
		for _, aspect := range []string{"definition", "method", "limits", "overview"} {
			text, finance := checkedAnswer(modelAnswer{Kind: "education", Concepts: []educationSelection{{Topic: topic.ID, Aspect: aspect}}}, "")
			if text == educationReply || !finance {
				t.Fatalf("finance explanation did not pass: %s %s", topic.ID, aspect)
			}
		}
	}
	for _, tc := range []educationSelection{
		{Topic: "recommend_a_stock", Aspect: "definition"},
		{Topic: "pe", Aspect: "演示制造正适合上车"},
	} {
		if text, finance := checkedAnswer(modelAnswer{Kind: "education", Concepts: []educationSelection{tc}}, ""); text != educationReply || finance {
			t.Fatalf("unexpected text: %q", text)
		}
	}
	if text, finance := checkedAnswer(modelAnswer{Kind: "decline_advice"}, ""); text != AdviceBoundary || finance {
		t.Fatalf("unexpected refusal: %q", text)
	}
}

func TestConversationSubjectSpans(t *testing.T) {
	for _, sample := range []struct {
		text, mention string
		want          bool
	}{
		{"看看演示制造的未偿融资", "演示制造", true},
		{"看看600000.sh。", "600000.SH", true},
		{"看看600000.SH. 近期数据", "600000.SH", true},
		{"看看X600000.SH", "600000.SH", false},
		{"看看600000.SH", "600000", false},
		{"看看600000.SH并比较其他标的", "600000.SZ", false},
	} {
		if got := containsSubject(sample.text, sample.mention); got != sample.want {
			t.Errorf("containsSubject(%q, %q) = %t, want %t", sample.text, sample.mention, got, sample.want)
		}
	}
}

func TestConversationTradingDayScope(t *testing.T) {
	for _, sample := range []struct {
		text                 string
		requested, effective int
		want                 bool
	}{
		{"看看近２０个交易日", 20, 20, true},
		{"看看近２０个交易日", 20, 5, false},
		{"近２０个交易日怎么算", 0, 20, true},
		{"近２０个交易日怎么算", 0, 5, false},
		{"看２０２５年２月的成交量", 0, 20, false},
	} {
		if got := matchesTradingDays(sample.text, sample.requested, sample.effective); got != sample.want {
			t.Errorf("matchesTradingDays(%q, %d, %d) = %t, want %t", sample.text, sample.requested, sample.effective, got, sample.want)
		}
	}
}
