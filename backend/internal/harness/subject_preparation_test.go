package harness

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/db/dbtest"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/errs"
	"github.com/clawhunt-cloxier/vera-main/backend/internal/platform/policy"
)

func TestConversationPreparationKeepsStandaloneDailyLimitSeparate(t *testing.T) {
	ctx := t.Context()
	pool := dbtest.New(t)
	ws := workspace(t, pool)
	engine := &Engine{
		Pool:                  pool,
		Store:                 dataservice.NewOnDemandResearchStore(pool, nil, nil, true),
		RealResearch:          true,
		PrepareTuShareSubject: func(context.Context, string) error { return nil },
		Policy:                &policy.Policy{Quota: policy.Quota{UserDailyTasks: 1, PlatformInFlightTasks: 3}},
	}
	if _, err := engine.NewRiver(pool, false, 1); err != nil {
		t.Fatal(err)
	}

	var conv, turn uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO conversations(workspace_id,title) VALUES($1,'test') RETURNING id`, ws).Scan(&conv); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO conversation_turns
		(workspace_id,conversation_id,client_message_id,request_hash,ordinal,channel,body,state)
		VALUES($1,$2,$3,'test',1,'web','test','pending') RETURNING id`, ws, conv, uuid.New()).Scan(&turn); err != nil {
		t.Fatal(err)
	}

	pending := func(result SubjectPreparation, err error) {
		t.Helper()
		if err != nil || result.Status != "pending" {
			t.Fatalf("expected a pending preparation, got %+v (%v)", result, err)
		}
	}
	pending(engine.RequestConversationSubjectPreparation(ctx, ws, "000001.SZ", turn, 1))
	pending(engine.RequestSubjectPreparation(ctx, ws, "000001.SZ")) // joins the existing job
	pending(engine.RequestSubjectPreparation(ctx, ws, "000002.SZ")) // has its own daily allowance
	if _, err := engine.RequestSubjectPreparation(ctx, ws, "000003.SZ"); errs.As(err).Code != "PREPARATION_DAILY_QUOTA" {
		t.Fatalf("standalone daily limit: %v", err)
	}
	pending(engine.RequestConversationSubjectPreparation(ctx, ws, "000003.SZ", turn, 1))
	if _, err := engine.RequestConversationSubjectPreparation(ctx, ws, "000004.SZ", turn, 1); errs.As(err).Code != "PREPARATION_QUOTA" {
		t.Fatalf("shared queue limit: %v", err)
	}
}
