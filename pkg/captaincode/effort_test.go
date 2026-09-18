package captaincode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffortFollowsThePreferenceThenTheDifficulty(t *testing.T) {
	assert.Equal(t, EffortMax, EffortFor("frontier", ClassTrivial), "/frontier is max whatever the task")
	assert.Equal(t, EffortHigh, EffortFor("quality", ClassTrivial))
	assert.Equal(t, EffortLow, EffortFor("speed", ClassHigh))
	assert.Equal(t, EffortLow, EffortFor("save", ClassHigh))
	assert.Equal(t, EffortHigh, EffortFor("", ClassHigh), "a bare prompt: the difficulty rating decides")
	assert.Equal(t, EffortMedium, EffortFor("", ClassMedium))
	assert.Equal(t, EffortLow, EffortFor("", ClassTrivial))
}

func TestEffortFlagsPerTransport(t *testing.T) {
	assert.Equal(t, "max", EffortMax.ClaudeFlag())
	assert.Equal(t, "xhigh", EffortMax.CodexFlag(), "codex's top rung is xhigh")
	assert.Equal(t, "medium", EffortMedium.CodexFlag())
}

func TestEffortFitsTheModelsVariants(t *testing.T) {
	glm := []string{"low", "high", "max"}
	assert.Equal(t, "max", EffortMax.Variant(glm))
	assert.Equal(t, "high", EffortHigh.Variant(glm))
	assert.Equal(t, "low", EffortMedium.Variant(glm), "no medium rung: the strongest at or below")
	assert.Equal(t, "low", EffortLow.Variant(glm))
	spark := []string{"none", "low", "medium", "high", "xhigh"}
	assert.Equal(t, "xhigh", EffortMax.Variant(spark))
	assert.Equal(t, "medium", EffortMedium.Variant(spark))
	deepseek := []string{"high", "max"}
	assert.Equal(t, "high", EffortLow.Variant(deepseek), "nothing at or below: the weakest offered")
	assert.Equal(t, "", EffortHigh.Variant(nil), "a model without variants gets none")
	assert.Equal(t, "", Effort("").Variant(glm), "no effort decided: the model's default")
}

func TestOpencodeVariantsAreReadOnceFromTheServe(t *testing.T) {
	resetVariantsCacheForTest()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/config/providers", r.URL.Path)
		_, _ = w.Write([]byte(`{"providers":[{"id":"openrouter","models":{"z-ai/glm-5.3":{"variants":{"low":{},"high":{},"max":{}}},"qwen/qwen3.5":{"variants":{}}}}]}`))
	}))
	defer srv.Close()
	assert.ElementsMatch(t, []string{"low", "high", "max"}, opencodeVariants(srv.URL, "openrouter", "z-ai/glm-5.3"))
	assert.Empty(t, opencodeVariants(srv.URL, "openrouter", "qwen/qwen3.5"))
	assert.Empty(t, opencodeVariants(srv.URL, "xai", "grok-build-0.1"))
	assert.Equal(t, 1, calls, "one read per serve")
}
