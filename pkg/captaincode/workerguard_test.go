package captaincode

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerGuardRefusesUnrequestedPublishing(t *testing.T) {
	refused := []string{
		"git tag -a v0.3.11 -m 'v0.3.11' && git push origin v0.3.11",
		"git tag v1.2.0",
		"git push --tags",
		"git push origin --follow-tags",
		"git push origin refs/tags/v1",
		"git push origin v0.3.11",
		"gh release create v0.3.11 --title v0.3.11 --notes x",
		"gh release edit v0.3.10 --notes x",
		"gh api repos/o/r/releases -f tag_name=v1",
		"npm publish",
		"cargo publish",
		"docker push ghcr.io/o/img:1",
		"goreleaser release --clean",
	}
	for _, c := range refused {
		assert.NotEmpty(t, WorkerGuardRefusal(c, false, false), c)
		assert.Empty(t, WorkerGuardRefusal(c, true, false), "allowed when the user asked: %s", c)
	}
	for _, c := range []string{"git push origin main", "git push", "git tag", "git tag -l 'v0.*'", "git tag --list", "gh release list", "gh release view v1", "gh pr create --fill", "npm install", "docker build ."} {
		assert.Empty(t, WorkerGuardRefusal(c, false, false), c)
	}
}

func TestWorkerGuardRefusesBulkStagingOverOthersWork(t *testing.T) {
	bulk := []string{"git add -A && git status --short", "git add --all", "git add .", "git add -u . ", "git commit -am 'x'", "git commit -a -m x", "git commit --all -m x", "git -C /w add -A"}
	for _, c := range bulk {
		assert.NotEmpty(t, WorkerGuardRefusal(c, false, true), c)
		assert.Empty(t, WorkerGuardRefusal(c, false, false), "a clean checkout: %s", c)
	}
	for _, c := range []string{"git add pkg/a.go docs/b.md", "git add ./pkg/a.go", "git commit -m 'add all the legs'", "git commit --amend --no-edit", "git commit -m x"} {
		assert.Empty(t, WorkerGuardRefusal(c, false, true), c)
	}
}

func TestAsksToPublish(t *testing.T) {
	for _, s := range []string{"cut a release", "publish it on npm", "tag v1.2 and push", "release v0.3.12", "ship v2", "bump the version"} {
		assert.True(t, AsksToPublish(s), s)
	}
	for _, s := range []string{"build it", "commit, push on gh and overleaf", "add the legs", "fix the tests"} {
		assert.False(t, AsksToPublish(s), s)
	}
}

func TestGuardContractCarriesTheTurnsFacts(t *testing.T) {
	assert.Contains(t, GuardContract(true, false), PublishGrantMarker)
	no := GuardContract(false, true)
	assert.NotContains(t, no, PublishGrantMarker)
	assert.Contains(t, no, "Publishing: not requested")
	assert.Contains(t, no, DirtyCheckoutMarker)
	env := strings.Join(WorkerGuardEnv("x"+GuardContract(false, true)), "\n")
	assert.Contains(t, env, MayPublishEnv+"=0")
	assert.Contains(t, env, DirtyBeforeEnv+"=1")
	assert.Contains(t, env, "PATH="+ShimDir())
}

// A leg capped at medium work is excluded from high-class ranking.
func TestMaxClassCapsWhatRoutingGivesALeg(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/legs.json"
	require.NoError(t, os.WriteFile(path, []byte(`{"legs":[{"id":"ds4-flash","max_class":"medium"}]}`), 0o600))
	_, err := LoadRegistry(path)
	require.NoError(t, err)
	t.Cleanup(func() { LoadRegistry(dir + "/none.json") })
	assert.Empty(t, ClassCap(LegDS4Flash, ClassMedium))
	assert.Contains(t, ClassCap(LegDS4Flash, ClassHigh), "capped at medium")
	for _, r := range ValueRank(ClassHigh, DomainCode, []Leg{LegDS4Flash}, nil, 1000, nil) {
		assert.Contains(t, r.Excluded, "capped at medium")
	}
	require.NoError(t, os.WriteFile(path, []byte(`{"legs":[{"id":"ds4-flash","max_class":"huge"}]}`), 0o600))
	_, err = LoadRegistry(path)
	assert.Error(t, err)
}
