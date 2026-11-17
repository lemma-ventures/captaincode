package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	pres "github.com/lemma-ventures/captaincode/internal/presence"
	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) linkPromptAccept(prompt captaincode.LinkPrompt) error {
	signed, err := prompt.Signed()
	if err != nil {
		return pres.Err("invalid_prompt")
	}
	if existing, ok := sqPrompt(prompt.PromptID); ok {
		if existing.Text != signed.Text || existing.Dir == "" || existing.Folder != signed.Envelope.Folder || pres.HashText(existing.Text) != signed.Envelope.TextSHA256 {
			return pres.Err("prompt_conflict")
		}
		return nil
	}
	if !linkPromptSingleTurn(signed.Text) {
		return pres.Err("read_only_single_turn_required")
	}
	linked, err := captaincode.LinkFolders()
	if err != nil {
		return err
	}
	config, err := pres.DefaultStore().Read()
	if err != nil {
		return err
	}
	dir := config.Folders[signed.Envelope.Folder]
	canonicalDir, err := captaincode.LinkCanonicalRoot(dir)
	if err != nil {
		return pres.Err("unknown_folder")
	}
	hold := false
	found := false
	for _, folder := range linked {
		if filepath.Clean(folder.Root) != canonicalDir {
			continue
		}
		found = true
		hold = folder.Mode == captaincode.LinkModeQueue
	}
	if !found {
		return pres.Err("folder_not_linked")
	}
	grant, err := pres.DefaultStore().VerifyPromptReadOnly(signed)
	if err != nil {
		return err
	}
	b.inbox.mu.Lock()
	defer b.inbox.mu.Unlock()
	b.inbox.seq++
	now := time.Now()
	item := inboxItem{ID: fmt.Sprintf("in_%d_%d", now.Unix(), b.inbox.seq), Text: signed.Text, At: now, Dir: grant.Dir, Presence: &grant, Origin: "hub (read-only)"}
	entry := sqEntry{ID: item.ID, PromptID: prompt.PromptID, Folder: signed.Envelope.Folder, Grant: grant.ID, Dir: grant.Dir, Text: signed.Text, At: now, Hold: hold}
	if err := sqWrite("pending", entry); err != nil {
		return err
	}
	if b.inbox.items == nil {
		b.inbox.items = map[string][]inboxItem{}
	}
	b.inbox.items[grant.Dir] = append(b.inbox.items[grant.Dir], item)
	fmt.Printf("captain link: accepted read-only prompt %s for %s\n", prompt.PromptID, signed.Envelope.Folder)
	return nil
}

func linkPromptSingleTurn(text string) bool {
	if captaincode.StartsLoop(text) {
		return false
	}
	head := strings.ToLower(captaincode.HoistLeading(strings.TrimSpace(text)))
	if strings.HasPrefix(head, "/") {
		return false
	}
	return true
}

func sqPrompt(promptID string) (sqEntry, bool) {
	for _, state := range []string{"pending", "done"} {
		for _, entry := range sqList(state) {
			if entry.PromptID == promptID {
				return entry, true
			}
		}
	}
	return sqEntry{}, false
}
