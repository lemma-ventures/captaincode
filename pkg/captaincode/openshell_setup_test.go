package captaincode

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShellConfirmGatesTheSandbox(t *testing.T) {
	r := openShellLegEnv(t)
	t.Setenv("CAPTAIN_OPENSHELL_ALLOWED", "")
	t.Setenv("CAPTAIN_OPENSHELL_VERIFY", "")
	_, _, err := openShellConfig(context.Background(), r.Repo, "fix a.txt")
	require.Error(t, err)
	assert.ErrorContains(t, err, "confirm the file list")
	assert.ErrorContains(t, err, "CAPTAIN_OPENSHELL_ALLOWED")
	assert.ErrorContains(t, err, "a.txt")
	assert.Empty(t, openShellStates(t, r))

	_, team, err := openShellConfig(context.Background(), r.Repo, `confirm allowed=a.txt verify=["go","test","./..."] fix a.txt`)
	require.NoError(t, err)
	require.Len(t, team.Tasks, 1)
	assert.Equal(t, []string{"a.txt"}, team.Tasks[0].Allowed)
	assert.Equal(t, []string{"go", "test", "./..."}, team.Tasks[0].Verify)
	assert.Equal(t, "fix a.txt", team.Tasks[0].Prompt)

	_, _, err = openShellConfig(context.Background(), r.Repo, `confirm allowed=missing.go verify=["go","test","./..."] fix it`)
	require.ErrorContains(t, err, "not a file in the pinned commit")
	assert.Empty(t, openShellStates(t, r))
}
