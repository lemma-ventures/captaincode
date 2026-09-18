package captaincode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoTimeoutMul(t *testing.T) {
	assert.Equal(t, 10, NoTimeoutMul("review the codebase --notimeout"))
	assert.Equal(t, 10, NoTimeoutMul("--no-timeout do the thing"), "hyphenated variant")
	assert.Equal(t, 10, NoTimeoutMul("[user]\nfix it all --NoTimeout"))
	assert.Equal(t, 1, NoTimeoutMul("review the codebase"))
	assert.Equal(t, 1, NoTimeoutMul("check the file nota--notimeoutx"), "word boundary")
	// A replayed conversation with the flag in an EARLIER turn must not
	// re-trigger it on later turns.
	replay := "[user]\nbig job --notimeout\n[assistant]\ndone\n[user]\nnow a quick fix"
	assert.Equal(t, 1, NoTimeoutMul(replay))
	assert.Equal(t, 10, NoTimeoutMul(replay+" --notimeout"))
}

func TestStripNoTimeout(t *testing.T) {
	assert.Equal(t, "review the codebase", StripNoTimeout("review the codebase --notimeout"))
	s := StripNoTimeout("[user]\na --notimeout\n[user]\nb --no-timeout end")
	assert.NotContains(t, s, "notimeout")
	assert.Contains(t, s, "b")
	assert.Contains(t, s, "end")
}

func TestProgressCtx(t *testing.T) {
	t.Setenv("CAPTAIN_WORKER_CLI_IDLE_TIMEOUT", "150ms")

	t.Run("progressing run outlives the base cap", func(t *testing.T) {
		ctx, cancel, prog, capped := progressCtx(200*time.Millisecond, 2*time.Second)
		defer cancel()
		deadline := time.Now().Add(600 * time.Millisecond)
		for time.Now().Before(deadline) {
			prog.touch()
			time.Sleep(30 * time.Millisecond)
		}
		require.NoError(t, ctx.Err(), "active run must survive 3× the base cap")
		assert.False(t, capped.Load())
	})

	t.Run("quiet run dies at base+idle", func(t *testing.T) {
		ctx, cancel, _, capped := progressCtx(150*time.Millisecond, 5*time.Second)
		defer cancel()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("quiet run should have been capped well before the ceiling")
		}
		assert.True(t, capped.Load())
	})

	t.Run("an explicit ceiling kills even a progressing run", func(t *testing.T) {
		ctx, cancel, prog, capped := progressCtx(100*time.Millisecond, 400*time.Millisecond)
		defer cancel()
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					prog.touch()
					time.Sleep(20 * time.Millisecond)
				}
			}
		}()
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("ceiling must be absolute")
		}
		close(done)
		assert.True(t, capped.Load())
	})
}
