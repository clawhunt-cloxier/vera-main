package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/clawhunt-cloxier/vera-main/backend/internal/dataservice"
)

// BriefQuoteSource is controlled deployment configuration, not a client or
// model choice. The confirmation transaction verifies the exact source grant.
type BriefQuoteSource struct {
	ProviderID string
	Product    string
}

// RegisterBriefIntent is called by a trusted 0.4 confirmation transaction
// after creating the task and marking the plan consumed, before enqueueing a
// worker. It derives subjects and datasets from the exact stored plan rather
// than accepting collection instructions from the request or model. The caller
// must commit or roll back task creation, this registration and enqueue together.
// The 0.4 confirmation path calls this before inserting its River jobs.
func RegisterBriefIntent(ctx context.Context, tx pgx.Tx, workspace, task uuid.UUID, epoch int64, confirmedHash string, source BriefQuoteSource) error {
	if tx == nil || workspace == uuid.Nil || task == uuid.Nil || epoch < 1 || len(confirmedHash) != 64 || source.ProviderID == "" || source.Product == "" {
		return errors.New("harness: invalid brief confirmation")
	}
	var planHash, skill, version, taskSkill, taskVersion, status, mode, planMode string
	var subject dataservice.EntityRow
	var taskSubject uuid.UUID
	var actualEpoch int64
	var planAsOf, taskAsOf time.Time
	var planParams, taskParams, planSections, taskSections []byte
	var executable, unexpired, beforeDeadline bool
	var snapshotAsOf *time.Time
	var quoteRequirement *string
	err := tx.QueryRow(ctx, `SELECT p.plan_hash,p.skill_id,p.skill_version,p.as_of,p.params,p.sections,p.executable,p.expires_at>clock_timestamp(),p.snapshot_mode,
		t.skill_id,t.skill_version,t.subject_id,t.as_of,t.params,t.sections,t.epoch,t.status,t.deadline>clock_timestamp(),t.snapshot_mode,t.snapshot_as_of,
		e.id,e.kind,e.code,e.synthetic,p.quote_requirement
		FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id
		JOIN entities e ON e.id=p.subject_id
		WHERE t.workspace_id=$1 AND t.id=$2 AND p.consumed_task_id=t.id
		FOR UPDATE OF t,p FOR SHARE OF e`, workspace, task).
		Scan(&planHash, &skill, &version, &planAsOf, &planParams, &planSections, &executable, &unexpired, &planMode,
			&taskSkill, &taskVersion, &taskSubject, &taskAsOf, &taskParams, &taskSections, &actualEpoch, &status, &beforeDeadline, &mode, &snapshotAsOf,
			&subject.ID, &subject.Kind, &subject.Code, &subject.Synthetic, &quoteRequirement)
	if err != nil {
		return fmt.Errorf("harness: confirmed brief task unavailable: %w", err)
	}
	// Re-read the clock after the row lock: lock waits must not extend a plan or
	// task past its expiry. The row locks remain held until the caller commits.
	if err := tx.QueryRow(ctx, `SELECT p.expires_at>clock_timestamp(),t.deadline>clock_timestamp()
		FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE t.id=$1 AND t.workspace_id=$2`, task, workspace).Scan(&unexpired, &beforeDeadline); err != nil {
		return err
	}
	if !executable || !unexpired || !beforeDeadline || actualEpoch != epoch || (status != "QUEUED" && status != "COLLECTING") || mode != "collect_latest" || planMode != mode || snapshotAsOf != nil || taskSubject != subject.ID || !taskAsOf.Equal(planAsOf) || skill != "stock_analysis" || taskSkill != skill || (version != BriefDemoVersion && version != RealtimeBriefVersion) || taskVersion != version || planHash != confirmedHash {
		return errors.New("harness: brief plan or task changed before confirmation")
	}
	var pp, tp map[string]any
	var ps, ts []PlanSection
	if json.Unmarshal(planParams, &pp) != nil || json.Unmarshal(taskParams, &tp) != nil || pp == nil || tp == nil || len(pp) != 0 || len(tp) != 0 || json.Unmarshal(planSections, &ps) != nil || json.Unmarshal(taskSections, &ts) != nil {
		return errors.New("harness: invalid brief plan parameters or sections")
	}
	definition, err := NewRealtimeBriefPlan(subject, planAsOf, true)
	if version == RealtimeBriefVersion {
		if quoteRequirement == nil {
			return errors.New("harness: missing confirmed quote requirement")
		}
		definition, err = NewQuoteBriefPlan(subject, planAsOf, true, *quoteRequirement)
	} else if quoteRequirement != nil {
		return errors.New("harness: legacy brief cannot acquire a new quote requirement")
	}
	if err != nil {
		return err
	}
	expectedHash, err := definition.Hash()
	if err != nil || expectedHash != planHash || !reflect.DeepEqual(ps, definition.Sections) || !confirmedBriefSections(ts, definition.Sections) {
		return errors.New("harness: brief confirmation does not match the approved plan")
	}
	if err := requireBriefSource(ctx, tx, source, subject.Code, definition.QuoteRequirement); err != nil {
		return err
	}
	// Source-lock waits may consume the remaining validity window. The source
	// helper already validates all three intended uses and rechecks the policy.
	if err := tx.QueryRow(ctx, `SELECT p.expires_at>clock_timestamp(),t.deadline>clock_timestamp()
		FROM research_tasks t JOIN research_plans p ON p.id=t.plan_id AND p.workspace_id=t.workspace_id WHERE t.id=$1 AND t.workspace_id=$2`, task, workspace).Scan(&unexpired, &beforeDeadline); err != nil {
		return err
	}
	if !unexpired || !beforeDeadline {
		return errors.New("harness: brief plan or task expired during confirmation")
	}
	ids := make([]string, 0, len(definition.Datasets))
	required := make([]string, 0, len(definition.Datasets))
	for _, dataset := range definition.Datasets {
		ids = append(ids, dataset.ID)
		if dataset.Required {
			required = append(required, dataset.ID)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO quote_task_intents(workspace_id,task_id,epoch,plan_hash,protocol,requirement,entity_ids)
		VALUES($1,$2,$3,$4,'0.4',$5,ARRAY[$6]::uuid[]) ON CONFLICT DO NOTHING`, workspace, task, epoch, planHash, definition.QuoteRequirement, subject.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_snapshot_requirements(workspace_id,task_id,epoch,dataset_ids,required_ids)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, workspace, task, epoch, ids, required); err != nil {
		return err
	}
	var storedHash, storedRequirement string
	var subjects []uuid.UUID
	var storedIDs, storedRequired []string
	if err := tx.QueryRow(ctx, `SELECT i.plan_hash,i.requirement,i.entity_ids,r.dataset_ids,r.required_ids
		FROM quote_task_intents i JOIN task_snapshot_requirements r USING(workspace_id,task_id,epoch)
		WHERE i.workspace_id=$1 AND i.task_id=$2 AND i.epoch=$3 AND i.protocol='0.4'`, workspace, task, epoch).
		Scan(&storedHash, &storedRequirement, &subjects, &storedIDs, &storedRequired); err != nil {
		return err
	}
	if storedHash != planHash || storedRequirement != definition.QuoteRequirement || !slices.Equal(subjects, []uuid.UUID{subject.ID}) || !slices.Equal(storedIDs, ids) || !slices.Equal(storedRequired, required) {
		return errors.New("harness: stored brief intent conflicts with approved plan")
	}
	return nil
}

// Optional sections may only differ by the explicit user cancellation marker.
func confirmedBriefSections(actual, expected []PlanSection) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i, got := range actual {
		want := expected[i]
		if reflect.DeepEqual(got, want) {
			continue
		}
		if want.Required || got.ID != want.ID || got.Title != want.Title || got.Required != want.Required || got.Avail != "unavailable" || got.Reason == nil || *got.Reason != "用户取消" {
			return false
		}
	}
	return true
}
