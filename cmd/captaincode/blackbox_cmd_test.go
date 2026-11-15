package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

func TestBlackboxCheckJSONWithoutConfiguration(t *testing.T) {
	t.Setenv("CAPTAIN_DECISION_MODEL", "not-a-configured-model")
	dir := t.TempDir()
	snapshot := map[string]any{
		"schema": 1, "id": "fixture", "source": "synthetic", "revision": "rev-1",
		"categories": map[string]bool{"dependency": true, "protected_path": false, "secret_exposure": true, "pattern": false},
		"packages":   []map[string]string{{"id": "pkg", "ecosystem": "npm", "name": "example-package", "version": "1.2.3", "kind": "known_malware", "severity": "critical"}},
	}
	body, _ := json.Marshal(snapshot)
	path := filepath.Join(dir, "snapshot.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	action := `{"schema":1,"host":"opencode","phase":"pre","tool":"bash","kind":"shell","parse_status":"parsed","packages":[{"ecosystem":"npm","name":"example-package","version":"1.2.3"}]}`
	var output bytes.Buffer
	if err := runBlackboxCheck([]string{"--snapshot", path}, bytes.NewBufferString(action), &output); err != nil {
		t.Fatal(err)
	}
	var result captaincode.BlackboxResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "incomplete" || len(result.Findings) != 1 || result.Findings[0].RuleID != "pkg" {
		t.Fatalf("result = %+v", result)
	}
}

func TestBlackboxCheckRefusesBadArgumentsAndInput(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"check", "--snapshot", "x", "extra"}} {
		if err := runBlackbox(args, bytes.NewReader(nil), &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
	if err := runBlackboxCheck([]string{}, bytes.NewReader(nil), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted check without a snapshot")
	}
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"id":"fixture","source":"synthetic","revision":"rev-1","categories":{"dependency":false,"protected_path":false,"secret_exposure":true,"pattern":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runBlackboxCheck([]string{"--snapshot", path}, bytes.NewBufferString(`{"schema":1,"unknown":true}`), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted unknown action field")
	}
}
