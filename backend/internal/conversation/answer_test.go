package conversation

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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

func TestConversationReplyFormat(t *testing.T) {
	for _, sample := range []struct {
		text  string
		allow bool
	}{
		{"零息债券持有到期时一次性兑付，期间没有票息。", true},
		{"反T指先卖出再买回，仍要考虑 T+1 规则和交易成本。", true},
		{"可以解释投资评级和目标价的含义。", true},
		{"The fund trades at a discount when its market price is below its net asset value.", true},
		{"换个话题聊聊吧", true},
		{"", false},
		{"无效字节\xff", false},
		{"替换字符\ufffd", false},
		{"不可见\u200b字符", false},
		{"反向\u202e字符", false},
		{"控制\x00字符", false},
		{strings.Repeat("长", 651), false},
		{strings.Repeat("换行\n", 7), false},
	} {
		if got := validReplyFormat(sample.text); got != sample.allow {
			t.Errorf("validReplyFormat(%q) = %t, want %t", sample.text, got, sample.allow)
		}
	}
}

func TestConversationTitleBoundary(t *testing.T) {
	for _, title := range []string{"融资余额变化", "Closed-End Fund Discounts", "开场问候", "反T操作原理"} {
		if !validTitle(title) {
			t.Errorf("rejected title %q", title)
		}
	}
	for _, title := range []string{"融资余额\n变化", "<script>", "ＡＢＣ５０", "--", ""} {
		if validTitle(title) {
			t.Errorf("accepted invalid title %q", title)
		}
	}
}

func TestConversationLegacyTitleClaimSurvivesInterruption(t *testing.T) {
	pool := dbtest.New(t)
	s := Service{Engine: &harness.Engine{Pool: pool}}
	const firstMessage = "请给我介绍零息债券，消息里有一段模拟私人备注"
	args := titleArgs{Legacy: true}
	if err := pool.QueryRow(t.Context(), `WITH u AS (
		INSERT INTO users(issuer,subject) VALUES('test','legacy-title-claim') RETURNING id
	), w AS (
		INSERT INTO workspaces(owner_user_id) SELECT id FROM u RETURNING id
	), c AS (
		INSERT INTO conversations(workspace_id,title,revision) SELECT id,left($1,48),7 FROM w RETURNING id,workspace_id
	)
	INSERT INTO conversation_turns(workspace_id,conversation_id,client_message_id,request_hash,ordinal,channel,body,state)
	SELECT workspace_id,id,uuidv7(),'test',1,'web',$1,'unavailable' FROM c
	RETURNING workspace_id,conversation_id,id`, firstMessage).Scan(&args.Workspace, &args.Conversation, &args.FirstTurn); err != nil {
		t.Fatal(err)
	}
	if first, err := s.claimTitle(t.Context(), args); err != nil || first != firstMessage {
		t.Fatalf("first-message claim returned %q, %v", first, err)
	}
	// Simulate checking the persisted state after a worker dies before dispatch.
	if _, err := s.claimTitle(t.Context(), args); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("redelivery should not claim an already reserved title: %v", err)
	}
	var title string
	var revision int64
	if err := pool.QueryRow(t.Context(), `SELECT title,revision FROM conversations WHERE id=$1`, args.Conversation).Scan(&title, &revision); err != nil {
		t.Fatal(err)
	}
	if title != defaultConversationTitle || revision != 7 {
		t.Fatalf("interrupted title retained a legacy excerpt or changed the revision: title=%q revision=%d", title, revision)
	}
}

func TestConversationPublicKnowledgeMessages(t *testing.T) {
	const private = "演示私人持仓备注"
	messages := publicKnowledgeMessages(knowledgeDraft{Topic: "zero_coupon_bond", Focus: "cash_flows", Message: private})
	if len(messages) != 2 || messages[1].Role != "user" || messages[1].Content != "零息债券：现金流的结构和发生时间。" || strings.Contains(messages[0].Content, private) {
		t.Fatalf("search payload was not built from the public catalog: %+v", messages)
	}
	for _, args := range []knowledgeDraft{
		{Topic: private, Focus: "cash_flows"},
		{Topic: "zero_coupon_bond", Focus: "cash_flows " + private},
		{Topic: "zero_coupon_bond", Focus: ""},
	} {
		if messages := publicKnowledgeMessages(args); messages != nil {
			t.Errorf("invalid identifiers produced a search payload: %+v", messages)
		}
	}
}

func TestConversationPublicCitationsBoundEncodedValues(t *testing.T) {
	longHost := strings.Repeat(strings.Repeat("x", 60)+".", 3) + "example"
	tooLongEscaped := "https://example.com/" + strings.Repeat("é", 400)
	got := publicCitations([]model.WebCitation{
		{URL: tooLongEscaped, Title: "过长的转义路径"},
		{URL: "https://" + longHost + "/"},
	})
	if len(got) != 1 || got[0].URL != "https://"+longHost+"/" || got[0].Title != "网页来源" {
		t.Fatalf("citation exceeded the contract's limits: %+v", got)
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
