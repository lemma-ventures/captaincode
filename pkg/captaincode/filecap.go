package captaincode

// maxRecordedFiles bounds every changed-file list the ledger keeps. The list
// is evidence (which files a turn touched, what a later commit kept), and 200
// paths is already more than any review reads. Unbounded, one solo turn in a
// repository with an untracked 20k-file folder (local worktrees) recorded
// 20,613 paths three times over - outcome, attempt state, handoff - and 306
// such rows made state.json 184 MB. Every Save re-wrote it under the brain's
// lock, the sidebar polls queued behind it, and the brain stopped answering
// (2026-10-18).
const maxRecordedFiles = 200

// capFiles keeps the first maxRecordedFiles paths (callers pass them sorted).
func capFiles(files []string) []string {
	if len(files) <= maxRecordedFiles {
		return files
	}
	return append([]string(nil), files[:maxRecordedFiles]...)
}

// capFileLists bounds the changed-file lists already in the ledger, so a
// state.json written before the cap shrinks on its next save.
func (l *Ledger) capFileLists() {
	for i := range l.Outcomes {
		l.Outcomes[i].ChangedFiles = capFiles(l.Outcomes[i].ChangedFiles)
	}
	for i := range l.AttemptStates {
		l.AttemptStates[i].ChangedFiles = capFiles(l.AttemptStates[i].ChangedFiles)
	}
	for i := range l.Handoffs {
		h := &l.Handoffs[i]
		for j := range h.Artifacts {
			h.Artifacts[j].ChangedFiles = capFiles(h.Artifacts[j].ChangedFiles)
		}
		if h.Integration != nil {
			h.Integration.ChangedFiles = capFiles(h.Integration.ChangedFiles)
		}
	}
}
