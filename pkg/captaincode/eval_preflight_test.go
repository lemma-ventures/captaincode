package captaincode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSuiteVerifiesAllSelectedTasksBeforeDispatch(t *testing.T) {
	repo, rev := fixtureRepo(t)
	for _, kind := range []string{"vacuous", "broken", "unknown", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			s := baseSuite(repo, rev)
			marker := filepath.Join(t.TempDir(), "dispatched")
			s.Arms[0].Run = []string{"touch", marker}
			task := s.Tasks[0]
			task.ID = "invalid"
			task.Checks = []EvalCheck{{Name: "exists", Run: []string{"test", "-f", "answer.txt"}}}
			if kind == "broken" {
				task.Setup = [][]string{{"false"}}
			}
			s.Tasks = append(s.Tasks, task)
			opts := EvalOptions{WorkDir: t.TempDir()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "unknown" {
				opts.Only = []string{"t1", "missing"}
			}
			if kind == "cancelled" {
				cancel()
			}
			res, err := RunSuite(ctx, s, opts)
			if err == nil || res != nil {
				t.Fatalf("invalid preflight must fail: result=%+v error=%v", res, err)
			}
			if kind == "cancelled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("want cancellation, got %v", err)
			}
			if (kind == "vacuous" || kind == "broken") && !strings.Contains(err.Error(), kind) {
				t.Fatalf("missing baseline diagnosis: %v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("arm ran before all fixtures passed preflight: %v", err)
			}
		})
	}
}

func TestRunSuiteExportsSelectedBaselineEvidence(t *testing.T) {
	repo, rev := fixtureRepo(t)
	s := baseSuite(repo, rev)
	invalid := s.Tasks[0]
	invalid.ID = "excluded"
	invalid.Setup = [][]string{{"false"}}
	s.Tasks = append(s.Tasks, invalid)
	res, err := RunSuite(context.Background(), s, EvalOptions{WorkDir: t.TempDir(), Only: []string{"t1"}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var exported EvalResult
	if err := json.Unmarshal(data, &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.Baselines) != 1 || exported.Baselines[0].TaskID != "t1" || !exported.Baselines[0].Runnable() {
		t.Fatalf("missing selected baseline evidence: %+v", exported.Baselines)
	}
	if len(exported.Executions) != 1 || !exported.Executions[0].Accepted() {
		t.Fatalf("valid selection did not execute: %+v", exported.Executions)
	}
	if exported.Baselines[0].Dir == exported.Executions[0].Dir {
		t.Fatal("baseline and worker must use separate snapshots")
	}
}
