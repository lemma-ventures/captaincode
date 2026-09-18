package main

// /init in the TUI - a control word like /wf: the fork forwards the message
// verbatim, the brain intercepts it before any dispatch and regenerates the
// captain configs instead of sending "init" to a worker. The report is the
// answer. "/init check" reports without writing.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func (b *brain) handleInit(w http.ResponseWriter, req oaiChatReq, raw string) bool {
	cmd := strings.TrimSpace(raw)
	if cmd != "/init" && !strings.HasPrefix(cmd, "/init ") {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, "/init"))
	apply := rest != "check" && rest != "--check"

	emit, _, finish := newCompletionWriter(w, req, "init")
	defer finish()
	report, changed, err := captaincode.RunInit(captaincode.InitOptions{Apply: apply})
	if err != nil {
		fmt.Printf("captain brain: /init failed - %v\n", err)
		emit("captain init failed: " + err.Error())
		finish()
		return true
	}
	fmt.Printf("captain brain: /init ran (apply=%v changed=%v)\n", apply, changed)
	emit(report)
	finish()
	return true
}
