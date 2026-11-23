package runlog

import "testing"

// TestIntegrationRunLogWorkflows runs the file-backed run log workflows as one
// integration-shaped suite: writing and reopening a log, checkpoint recovery,
// the size cap, and reading it back by entry, by batch and as text.
func TestIntegrationRunLogWorkflows(t *testing.T) {
	TestReopenContinuesSequenceTurnAndToolDedup(t)
	TestRecoverySkipsOversizedLinesAndTruncatesTornTail(t)
	TestWriterAndReaderAgreeOnFinalLineBound(t)
	TestCheckpointRecoversOnlyCommittedJournalEffects(t)
	TestCheckpointPreservesCoalescedTextAndUnfinishedTool(t)
	TestCheckpointPublishesOnlyCommittedEntries(t)
	TestJournalGapDoesNotActivateHostLogCap(t)
	TestCapMarkerCountsTheBytesItDropped(t)
	TestReadEntryBatchWalksTheLogInBatches(t)
	TestReadEntryBatchReadsOnlyAppended(t)
	TestSummarizeLogMatchesSummarize(t)
	TestTextViewMatchesTheRenderedLog(t)
	TestTextViewFollowsAGrowingLogAndIgnoresATornTail(t)
	TestTextViewSeesOnlyTheCheckpointedPrefix(t)
	TestLegacyOutputTrackingStaysSmall(t)
}

func TestE2ERunLogWorkflows(t *testing.T) {
	TestIntegrationRunLogWorkflows(t)
}
