package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// shouldAutoApply is false inside `go test` unless the test opts in.
// A developer shell that exports CAPTAIN_AUTO_UPGRADE must not retarget
// pins or run CLI updaters while the suite calls refreshPerf.
func shouldAutoApply() bool {
	if flag.Lookup("test.v") != nil && os.Getenv("CAPTAIN_AUTO_UPGRADE_IN_TEST") != "1" {
		return false
	}
	return true
}

func (b *brain) modelCatalogue() (map[string][]string, error) {
	if b != nil && b.roster.catalogue != nil {
		return b.roster.catalogue()
	}
	return modelsDevCatalogue()
}

// noteLegUpgrades proposes standing flags once, and writes pins only when
// CAPTAIN_AUTO_UPGRADE allows it and the brain is idle. changed is true when
// this refresh's feed differed from the previous one. A second identical
// refresh stays quiet.
func (b *brain) noteLegUpgrades(changed bool) {
	if !changed && b.roster.upgradesNoted {
		b.maybeAutoApply(false)
		return
	}
	first := !b.roster.upgradesNoted
	b.roster.upgradesNoted = true
	models, _, _ := captaincode.PerfModels()
	if first && !changed {
		for _, f := range captaincode.PerfFlags(models) {
			b.pushActivity(activity{Kind: "feed", Leg: string(f.Leg), Text: fmt.Sprintf("⇡ %s: %s (%.0f) outscores %s (%.0f) - click ⇡ or `captain upgrade --models`", f.Leg, f.Upgrade.Slug, f.Upgrade.Perf, f.Current.Slug, f.Current.Perf)})
		}
	}
	b.maybeAutoApply(!b.roster.upgradeArmed)
}

// maybeAutoApply writes ready pins, or arms one idle wait. announceWait is
// false on a repeat poll so the sidebar does not hear the same wait twice.
func (b *brain) maybeAutoApply(announceWait bool) {
	if !captaincode.AutoUpgradeFromEnv().Models || !shouldAutoApply() {
		return
	}
	if !b.applyReadyPins() {
		return
	}
	if announceWait {
		b.pushActivity(activity{Kind: "feed", Leg: "ranking", Text: "auto-upgrade waiting for idle (CAPTAIN_AUTO_UPGRADE)"})
	}
	b.armIdleUpgrade()
}

// applyReadyPins writes every unheld model pin. It returns true when a pin
// is ready but the brain is busy, so the caller can wait. It does not start
// that wait itself.
func (b *brain) applyReadyPins() bool {
	if !shouldAutoApply() || !captaincode.AutoUpgradeFromEnv().Models {
		return false
	}
	models, _, _ := captaincode.PerfModels()
	type job struct {
		leg  captaincode.Leg
		spec captaincode.LegSpec
		next captaincode.AAModel
	}
	var jobs []job
	for _, leg := range captaincode.AllLegs {
		spec, ok := captaincode.Spec(leg)
		if !ok {
			continue
		}
		cur, next, ok := captaincode.UpgradePair(models, leg)
		if !ok || captaincode.HoldUpgrade(spec, cur, next) != "" {
			continue
		}
		jobs = append(jobs, job{leg, spec, next})
	}
	if len(jobs) == 0 {
		return false
	}
	if !b.idleForUpgrade() {
		return true
	}
	cat, err := b.modelCatalogue()
	if err != nil {
		fmt.Printf("captain brain: model catalogue: %v\n", err)
		return false
	}
	for _, j := range jobs {
		if !b.idleForUpgrade() {
			return true
		}
		id, ok := resolveProviderModel(cat, j.spec.Provider, j.next.Slug)
		if !ok || !safeModelID(id) {
			b.pushActivity(activity{Kind: "feed", Leg: string(j.leg), Text: fmt.Sprintf("%s: %s is not on %s yet - not retargeted", j.leg, j.next.Slug, j.spec.Provider)})
			continue
		}
		pin := captaincode.LegSpec{ID: j.leg, Model: id, AA: j.next.Slug}
		if !j.spec.Subscription {
			if j.next.PriceIn > 0 {
				pin.PriceIn = j.next.PriceIn
			}
			if j.next.PriceOut > 0 {
				pin.PriceOut = j.next.PriceOut
			}
		}
		if err := captaincode.AddLeg("", pin); err != nil {
			fmt.Printf("captain brain: auto-upgrade %s: %v\n", j.leg, err)
			continue
		}
		line := fmt.Sprintf("%s: retargeted %s → %s while idle", j.leg, j.spec.Model, id)
		fmt.Println("captain brain: " + line)
		b.pushActivity(activity{Kind: "feed", Leg: string(j.leg), Text: line})
	}
	return false
}

func (b *brain) armIdleUpgrade() {
	if !shouldAutoApply() {
		return
	}
	b.roster.upgradeMu.Lock()
	if b.roster.upgradeArmed {
		b.roster.upgradeMu.Unlock()
		return
	}
	b.roster.upgradeArmed = true
	b.roster.upgradeMu.Unlock()
	go func() {
		defer func() {
			b.roster.upgradeMu.Lock()
			b.roster.upgradeArmed = false
			b.roster.upgradeMu.Unlock()
		}()
		var stop <-chan struct{}
		if b.life != nil {
			stop = b.life.Done()
		}
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		deadline := time.NewTimer(perfRefreshInterval())
		defer deadline.Stop()
		for {
			select {
			case <-stop:
				return
			case <-deadline.C:
				return
			case <-tick.C:
				mode := captaincode.AutoUpgradeFromEnv()
				if mode.Models && b.idleForUpgrade() {
					if waiting := b.applyReadyPins(); !waiting {
						if mode.CLI {
							_ = b.upgradeCLIs()
						}
						return
					}
				} else if mode.CLI && !mode.Models && b.idleForUpgrade() {
					if !b.upgradeCLIs() {
						return
					}
				}
			}
		}
	}()
}

func (b *brain) noteCLIUpgrades(out []cliStatus) {
	if b.roster.cliAnnounced == nil {
		b.roster.cliAnnounced = map[string]string{}
	}
	for _, st := range out {
		if !st.Outdated || b.roster.cliAnnounced[st.Name] == st.Latest {
			continue
		}
		b.roster.cliAnnounced[st.Name] = st.Latest
		b.pushActivity(activity{Kind: "feed", Leg: st.Name, Text: fmt.Sprintf("%s %s → %s available - `captain upgrade` when idle", st.Name, st.Installed, st.Latest)})
	}
	if !captaincode.AutoUpgradeFromEnv().CLI || !shouldAutoApply() {
		return
	}
	if b.upgradeCLIs() {
		b.armIdleUpgrade()
	}
}

// runCLIUpdate is the vendor updater. Tests replace it.
var runCLIUpdate = func(name string, args []string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	return runBounded(cmd, 5*time.Minute)
}

// upgradeCLIs runs each installed CLI's own updater. It does not rebuild
// captain and it does not restart the brain. It returns true when the brain
// is busy so the caller can wait. It does not start that wait itself.
func (b *brain) upgradeCLIs() bool {
	if !shouldAutoApply() || !captaincode.AutoUpgradeFromEnv().CLI {
		return false
	}
	if !b.idleForUpgrade() {
		return true
	}
	for _, tool := range upgradeTools {
		if !b.idleForUpgrade() {
			return true
		}
		if _, err := exec.LookPath(tool.name); err != nil {
			continue
		}
		before := toolVersion(tool.name)
		out, err := runCLIUpdate(tool.name, tool.updateArgs)
		after := toolVersion(tool.name)
		switch {
		case err != nil:
			fmt.Printf("captain brain: auto-upgrade %s: %v (%s)\n", tool.name, err, truncate(out, 120))
		case after != before && after != "?":
			line := fmt.Sprintf("%s: %s → %s while idle", tool.name, before, after)
			fmt.Println("captain brain: " + line)
			b.pushActivity(activity{Kind: "feed", Leg: tool.name, Text: line})
		}
	}
	return false
}

func (b *brain) idleForUpgrade() bool {
	if b.inflightRuns.Load() != 0 {
		return false
	}
	b.wmu.Lock()
	for _, l := range b.liveBy {
		if l != nil && l.endedAt.IsZero() {
			b.wmu.Unlock()
			return false
		}
	}
	if b.live != nil && b.live.endedAt.IsZero() {
		b.wmu.Unlock()
		return false
	}
	b.wmu.Unlock()
	b.rmu.Lock()
	for _, th := range b.repeats {
		if th != nil && !th.finished {
			b.rmu.Unlock()
			return false
		}
	}
	b.rmu.Unlock()
	b.pmu.Lock()
	defer b.pmu.Unlock()
	for _, p := range b.parallels {
		if p != nil && !p.done {
			return false
		}
	}
	return true
}

func upgradeProposals() []upgradeProposal {
	models, _, _ := captaincode.PerfModels()
	var out []upgradeProposal
	for _, f := range captaincode.PerfFlags(models) {
		spec, _ := captaincode.Spec(f.Leg)
		p := upgradeProposal{Leg: string(f.Leg), Current: f.Current.Slug, Next: f.Upgrade.Slug, Action: "propose"}
		if cur, next, ok := captaincode.UpgradePair(models, f.Leg); ok {
			if hold := captaincode.HoldUpgrade(spec, cur, next); hold != "" {
				p.Action = "hold"
				p.Detail = hold
			} else if captaincode.AutoUpgradeFromEnv().Models {
				p.Action = "auto"
			}
		}
		out = append(out, p)
	}
	return out
}

type upgradeProposal struct {
	Leg     string `json:"leg"`
	Current string `json:"current"`
	Next    string `json:"next"`
	Action  string `json:"action"`
	Detail  string `json:"detail,omitempty"`
}

func safeModelID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	return !strings.ContainsAny(id, " \t\r\n\\'\"`$;&|<>")
}
