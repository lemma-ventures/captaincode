package captaincode

// The judge (SCORING.md Phase 2). The director graded every sampled run,
// its own leg's included, on a 0-10 scale nobody could separate the top
// legs on. Now a run is judged, when its model's estimate is uncertain, by
// one cheap leg from another vendor, on a rubric, without being told which
// model wrote the answer. Its pass/fail is a weak observation in the model
// estimate (model_estimate.go), weighted by how often that judge agreed
// with tests.

import (
	"sort"
	"strings"
)

// vendorPatterns maps model-id substrings to the company behind the model.
// A leg's vendor is its model's, not its transport's: cursor runs grok.
var vendorPatterns = []struct{ needle, vendor string }{
	{"claude", "anthropic"}, {"anthropic", "anthropic"},
	{"grok", "xai"}, {"x-ai", "xai"}, {"xai/", "xai"},
	{"gemini", "google"}, {"gemma", "google"},
	{"gpt", "openai"}, {"codex", "openai"}, {"openai/", "openai"},
	{"glm", "zhipu"}, {"z-ai", "zhipu"},
	{"kimi", "moonshot"}, {"moonshot", "moonshot"},
	{"deepseek", "deepseek"},
	{"minimax", "minimax"},
	{"qwen", "alibaba"},
	{"step", "stepfun"},
	{"nemotron", "nvidia"},
	{"composer", "cursor"},
}

// VendorOf is the company behind the model a leg runs.
func VendorOf(l Leg) string {
	model := strings.ToLower(ModelID(l))
	if spec, ok := Spec(l); ok && spec.Model != "" {
		model = strings.ToLower(spec.Model)
	}
	for _, p := range vendorPatterns {
		if strings.Contains(model, p.needle) {
			return p.vendor
		}
	}
	return string(l)
}

// judgeMinPrior keeps weak legs off the bench: a judge must be a capable
// model itself.
const judgeMinPrior = 7.0

// PickJudge is the leg that judges a run by worker: open, paid per call
// (a subscription leg's window belongs to the workers), from another
// vendor, and the strongest such leg by quality prior. ok is false when
// none qualifies; the run then goes unjudged.
func PickJudge(worker Leg, open func(Leg) bool) (Leg, bool) {
	vendor := VendorOf(worker)
	var cands []Leg
	for _, l := range AllLegs {
		spec, ok := Spec(l)
		if !ok || spec.Subscription || !ServesTasks(l) || l == LegFree {
			continue
		}
		if VendorOf(l) == vendor || QualityPrior(l) < judgeMinPrior || (open != nil && !open(l)) {
			continue
		}
		cands = append(cands, l)
	}
	if len(cands) == 0 {
		return "", false
	}
	sort.SliceStable(cands, func(i, j int) bool { return QualityPrior(cands[i]) > QualityPrior(cands[j]) })
	return cands[0], true
}

// JudgeRubric is what the judge is asked, ahead of any test result.
const JudgeRubric = "Rubric - pass only if the answer meets every requirement the task states and, for code, would build and run as described; fail if it misses a requirement, claims work it did not do, or leaves the task unfinished. Ignore length, tone and formatting."
