package api

import (
	domain "github.com/chaitin/agent-compose/pkg/model"
	"testing"
)

func TestRunLogHealthRemainsSeparateFromExecutionStatus(t *testing.T) {
	run := domain.ProjectRunRecord{Status: domain.ProjectRunStatusSucceeded, LogIncomplete: true}
	summary := ProjectRunSummaryToProto(run)
	detail := ProjectRunDetailToProto(run)
	if !summary.GetLogIncomplete() || !detail.GetSummary().GetLogIncomplete() || summary.GetStatus() != ProjectRunStatusToProto(run.Status) {
		t.Fatalf("summary=%+v detail=%+v", summary, detail)
	}
	run.LogIncomplete = false
	if ProjectRunSummaryToProto(run).GetLogIncomplete() {
		t.Fatal("healthy run marked incomplete")
	}
}
