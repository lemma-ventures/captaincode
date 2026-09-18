package main

// `captain state <folder>` - print the XDG_STATE_HOME the TUI should run
// with for that folder, preparing it on first use (see pkg tuistate.go). The
// launcher exports it, so each folder's TUI has its own prompt history, last
// model and preferences instead of the machine-wide ones.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func cmdState(args []string) {
	dir := ""
	last := false
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: captain state [--last] [<folder>]\n\nPrints the state directory (XDG_STATE_HOME) for a folder's TUI, creating and seeding it on first use:\npreferences and model from the machine-wide state, the prompt history from the folder's own sessions.\n--last prints the id of the folder's newest session instead (what -c should resume), nothing when there is none.")
			return
		case "--last":
			last = true
		default:
			dir = a
		}
	}
	if dir == "" {
		dir = defaultWorkspace().Dir
	}
	dir, _ = filepath.Abs(dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real // opencode keys sessions by the resolved path (a symlinked checkout found nothing, 2026-09-17)
	}
	db := ""
	if home, err := os.UserHomeDir(); err == nil {
		db = filepath.Join(home, ".local", "share", "opencode", "opencode.db")
		if v := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); v != "" {
			db = filepath.Join(v, "opencode", "opencode.db")
		}
	}
	if last {
		fmt.Println(captaincode.LastSessionFromDB(db, dir))
		return
	}
	stateHome, err := captaincode.PrepareTuiState(dir, captaincode.GlobalStateDir(), db)
	if err != nil {
		fatal(err)
	}
	fmt.Println(stateHome)
}
