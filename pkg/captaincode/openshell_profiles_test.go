package captaincode

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zdrEndpoint(model, tag, provider, in, out string) OpenRouterEndpoint {
	e := OpenRouterEndpoint{ModelID: model, Tag: tag, ProviderName: provider, ContextLength: 262144,
		SupportedParameters: []string{"tools", "max_tokens", "temperature", "reasoning"}}
	e.Pricing.Prompt, e.Pricing.Completion = in, out
	return e
}

func TestOpenShellCatalogKeepsOnlyAPIKeyLegsOnZDREndpoints(t *testing.T) {
	legs := []LegSpec{
		{ID: "jev", Transport: TransportSystemOne},
		{ID: "claude", Transport: TransportClaudeCLI},
		{ID: "grok", Transport: TransportOpencode, Provider: "xai", Subscription: true},
		{ID: "step", Transport: TransportOpencode, Provider: "huggingface", Model: "stepfun-ai/Step-3.5-Flash"},
		{ID: "gpt-oss", Transport: TransportOpencode, Provider: "openrouter", Model: "openai/gpt-oss-120b", Ctx: 131072},
		{ID: "glm", Transport: TransportOpencode, Provider: "nim", Model: "z-ai/glm-5.3", Tiers: map[Tier]string{TierCheap: "z-ai/glm-5.3-flash"}},
		{ID: "ds-flash", Transport: TransportOpencode, Provider: "nim", Model: "deepseek-ai/deepseek-v4.1-flash"},
		{ID: "off", Transport: TransportOpencode, Provider: "openrouter", Model: "openai/gpt-oss-120b", Disabled: true},
		{ID: "openshell", Transport: TransportOpencodeShell},
	}
	limited := zdrEndpoint("openai/gpt-oss-120b", "cerebras/fp16", "Cerebras", "0.00000035", "0.00000075")
	small := 4096
	limited.MaxCompletionTokens = &small
	noTools := zdrEndpoint("openai/gpt-oss-120b", "mara", "Mara", "0.0000001", "0.0000005")
	noTools.SupportedParameters = []string{"max_tokens", "temperature"}
	down := zdrEndpoint("openai/gpt-oss-120b", "phala", "Phala", "0.0000001", "0.0000005")
	down.Status = -2
	endpoints := []OpenRouterEndpoint{
		limited, noTools, down,
		zdrEndpoint("openai/gpt-oss-120b", "deepinfra/bf16", "DeepInfra", "0.000000037", "0.00000017"),
		zdrEndpoint("openai/gpt-oss-120b", "groq", "Groq", "nan", "0.0000005"),
		zdrEndpoint("openai/gpt-oss-120b", "evil/../x", "Evil", "0.0000001", "0.0000005"),
		zdrEndpoint("z-ai/glm-5.3", "z-ai/fp8", "Z.AI", "0.0000006", "0.000002"),
		// a dated variant on the same route: the profile takes the higher price
		zdrEndpoint("z-ai/glm-5.3", "z-ai/fp8", "Z.AI", "0.0000008", "0.000001"),
		zdrEndpoint("z-ai/glm-5.3-flash", "fireworks", "Fireworks", "0.0000001", "0.0000003"),
		zdrEndpoint("stepfun-ai/Step-3.5-Flash", "stepfun", "StepFun", "0.0000001", "0.0000003"),
	}
	c := BuildOpenShellCatalog(legs, endpoints, time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC))
	assert.Equal(t, "2026-10-02T12:00:00Z", c.Generated)
	assert.Equal(t, OpenRouterZDRURL, c.Source)
	assert.Equal(t, map[string]OpenShellCatalogProfile{
		"gpt-oss-cerebras-fp16": {Leg: "gpt-oss", Model: "openai/gpt-oss-120b", Route: "cerebras/fp16", ServedBy: "Cerebras",
			Output: 4096, Context: 131072, Ceiling: [2]float64{0.44, 0.94}},
		"gpt-oss-deepinfra-bf16": {Leg: "gpt-oss", Model: "openai/gpt-oss-120b", Route: "deepinfra/bf16", ServedBy: "DeepInfra",
			Output: 16384, Context: 131072, Ceiling: [2]float64{0.05, 0.22}},
		"glm-z-ai-fp8": {Leg: "glm", Model: "z-ai/glm-5.3", Route: "z-ai/fp8", ServedBy: "Z.AI",
			Output: 16384, Context: 262144, Ceiling: [2]float64{1, 2.5}},
		"glm-cheap-fireworks": {Leg: "glm", Tier: TierCheap, Model: "z-ai/glm-5.3-flash", Route: "fireworks", ServedBy: "Fireworks",
			Output: 16384, Context: 262144, Ceiling: [2]float64{0.13, 0.38}},
	}, c.Profiles)
	assert.Equal(t, []string{
		"claude: subscription login, not an API key",
		"grok: subscription login, not an API key",
		"step: provider huggingface has no per-request ZDR control",
		"ds-flash: no OpenRouter ZDR endpoint for deepseek-ai/deepseek-v4.1-flash takes tool calls",
	}, c.Skipped)
}

func TestOpenShellCatalogNamesAreValidProfiles(t *testing.T) {
	assert.Equal(t, "gemini-frontier-google-vertex-global-priority",
		openShellProfileName("gemini", TierFrontier, "google-vertex/global/priority"))
	assert.Equal(t, "gpt-oss-cerebras", openShellProfileName("gpt-oss", "", "cerebras"))
	long := zdrEndpoint("a/m", "abcdefghijklmnopqrstuvwxyz/abcdefghijklmnopqrstuvwxyz/abcdefghij", "P", "0", "0")
	c := BuildOpenShellCatalog([]LegSpec{{ID: "leg", Transport: TransportOpencode, Provider: "openrouter", Model: "a/m"}},
		[]OpenRouterEndpoint{long}, time.Now())
	assert.Empty(t, c.Profiles)
	assert.Contains(t, c.Skipped[0], "is not usable")
}

func TestOpenShellCatalogIsWrittenAndPriced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"), "the public list needs no key")
		json.NewEncoder(w).Encode(map[string]any{"data": []OpenRouterEndpoint{
			zdrEndpoint("openai/gpt-oss-120b", "groq", "Groq", "0.00000015", "0.0000006")}})
	}))
	defer srv.Close()
	endpoints, err := FetchOpenRouterZDR(context.Background(), srv.URL)
	require.NoError(t, err)
	c := BuildOpenShellCatalog([]LegSpec{{ID: "gpt-oss", Transport: TransportOpencode, Provider: "openrouter",
		Model: "openai/gpt-oss-120b"}}, endpoints, time.Now())
	dir := t.TempDir()
	path, err := WriteOpenShellCatalog(dir, c)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	assert.True(t, openShellCatalogPriced(dir, "gpt-oss-groq"))
	assert.False(t, openShellCatalogPriced(dir, "nim"))
	assert.False(t, openShellCatalogPriced("", "gpt-oss-groq"))

	r := &OpenShellRunner{Pilot: dir}
	ctx := context.WithValue(context.Background(), openShellCostLimitKey{}, 1.0)
	worker := validOpenShellTask()
	worker.Profile = "gpt-oss-groq"
	budget, err := r.costBudget(ctx, []OpenShellTeam{{Tasks: []OpenShellTask{worker}}})
	require.NoError(t, err)
	assert.Equal(t, 1.0, budget.WorkerUSD)

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer empty.Close()
	_, err = FetchOpenRouterZDR(context.Background(), empty.URL)
	assert.ErrorContains(t, err, "empty")
}

// The committed catalog is what `captain openshell profiles` writes: every
// entry is a usable profile name on an OpenRouter route with a ceiling.
func TestOpenShellCommittedCatalogIsWellFormed(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "openshell-pilot", openShellCatalogFile))
	require.NoError(t, err)
	var c OpenShellCatalog
	require.NoError(t, json.Unmarshal(data, &c))
	assert.Equal(t, 1, c.Schema)
	assert.NotEmpty(t, c.Profiles)
	for name, p := range c.Profiles {
		assert.Regexp(t, openShellProfile, name)
		assert.Regexp(t, openShellRoute, p.Route, name)
		assert.Regexp(t, openShellModelID, p.Model, name)
		assert.True(t, p.Ceiling[0] >= 0 && p.Ceiling[1] > 0, name)
		assert.True(t, p.Output >= 1 && p.Output <= openShellOutputCap, name)
	}
}

func TestOpenShellQualifyRecordsOnlyThreeOfThree(t *testing.T) {
	r := newFakeOpenShell(t)
	recorded := filepath.Join(r.Pilot, "recorded")

	runs, err := r.Qualify(context.Background(), "steady", io.Discard)
	require.NoError(t, err)
	require.Len(t, runs, openShellQualifyRuns)
	data, err := os.ReadFile(recorded)
	require.NoError(t, err)
	line := strings.Fields(strings.TrimSpace(string(data)))
	require.Len(t, line, 2+openShellQualifyRuns, "record <profile> and one report per run")
	assert.Equal(t, []string{"record", "steady"}, line[:2])
	for i, run := range runs {
		assert.Equal(t, filepath.Join(run.State, "report.json"), line[2+i])
	}

	require.NoError(t, os.Remove(recorded))
	require.NoError(t, os.Remove(filepath.Join(r.Pilot, "runs")))
	runs, err = r.Qualify(context.Background(), "flaky", io.Discard)
	assert.ErrorContains(t, err, "flaky passed 2 of 3 fixture runs; not qualified")
	assert.Len(t, runs, openShellQualifyRuns, "a failure does not stop the remaining runs")
	assert.NoFileExists(t, recorded, "nothing is recorded below 3 of 3")

	_, err = r.Qualify(context.Background(), "../x", io.Discard)
	assert.ErrorContains(t, err, "not a profile name")
}
