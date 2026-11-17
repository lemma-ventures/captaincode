package main

import (
	"path/filepath"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) linkTurnSummaries() ([]captaincode.LinkTurnSummary, error) {
	linked, err := captaincode.LinkFolders()
	if err != nil {
		return nil, err
	}
	roots := make(map[string]bool, len(linked))
	for _, folder := range linked {
		roots[filepath.Clean(folder.Root)] = true
	}
	entries := sqList("done")
	out := make([]captaincode.LinkTurnSummary, 0, len(entries))
	for i := len(entries) - 1; i >= 0 && len(out) < 64; i-- {
		entry := entries[i]
		if entry.PromptID == "" {
			continue
		}
		summary := entry.Summary
		if summary == nil && entry.Done == "headless" {
			built := captaincode.NewLinkTurnSummary(entry.PromptID, entry.Result, entry.DoneAt)
			summary = &built
		}
		if summary == nil {
			continue
		}
		root, err := captaincode.LinkCanonicalRoot(entry.Dir)
		if err != nil || !roots[root] {
			continue
		}
		if captaincode.LinkTurnSummaryValid(*summary) != nil {
			continue
		}
		out = append(out, *summary)
	}
	return out, nil
}
