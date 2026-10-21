package captaincode

import (
	"os/exec"
	"testing"
)

func TestPresenceTierClassification(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet", dir}, {"-C", dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk"}} {
		if out, e := exec.Command("git", args...).CombinedOutput(); e != nil {
			t.Fatal(string(out), e)
		}
	}
	for _, cmd := range []string{"git push origin main", "git push origin trunk", "git push origin HEAD:trunk", "git -C x push origin feature", "git push --force origin feature", "git push origin feature && rm file", "gh pr merge 1", "npm publish", "gh release create v1", "rm file", "payment transfer 100", "git merge feature"} {
		if !PresenceHighTier(GateAction{Tool: "bash", Command: cmd, Cwd: dir}) {
			t.Fatal(cmd)
		}
	}
	for _, cmd := range []string{"git push origin feature", "git commit -m change", "git add file.go", "go test ./..."} {
		if PresenceHighTier(GateAction{Tool: "bash", Command: cmd, Cwd: dir}) {
			t.Fatal(cmd)
		}
	}
	if !PresenceHighTier(GateAction{Tool: "payments_transfer"}) {
		t.Fatal("payment tool not gated")
	}
}

func TestPresencePublishAndPatchDeletionRequireDecision(t *testing.T) {
	for _, action := range []GateAction{
		{Tool: "bash", Command: "twine upload dist/package.whl"},
		{Tool: "apply_patch", Command: `{"patch":"*** Begin Patch\n*** Delete File: data.txt\n*** End Patch"}`},
		{Tool: "patch", Command: `{"patch":"--- data.txt\n+++ /dev/null\n@@ -1 +0,0 @@\n-data"}`},
		{Tool: "bash", Command: "git branch -d feature"},
	} {
		if !PresenceHighTier(action) {
			t.Errorf("no signed decision for %+v", action)
		}
	}
}
