package captaincode

// Ordering a queue (2026-10-04). Prompts typed while a turn runs used to run
// in the order typed. Three were queued behind an hour-long /frontier run: a
// blind review of a paper, a /btw naming a gap in that paper, and a security
// change that moves the paper's numbers. Typed order ran the review first,
// on a paper the next two prompts would change, and the note as a turn of
// its own. The director now reads the queue once before it runs: work that
// changes an artifact goes before work that reviews it, independent prompts
// keep the user's order, and each note joins the prompt it concerns.

import (
	"fmt"
	"strings"
)

// QueueItem is one queued prompt; a Note (a /btw) amends another prompt and
// does not run on its own.
type QueueItem struct {
	Text string
	Note bool
}

// QueueOrder is the director's plan for a queue, by item index: the order
// the prompts run in, the prompt each note joins, and one line of why.
type QueueOrder struct {
	Order []int
	Notes map[int]int
	Why   string
}

// IsQueueNote reports whether a queued prompt is a note to the work, not
// work: a /btw.
func IsQueueNote(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	return t == "/btw" || strings.HasPrefix(t, "/btw ")
}

// OrderQueue asks the director for the order of a queue. Anything it gets
// wrong - a missing or repeated prompt, a note sent nowhere - falls back on
// the typed order for that part (TypedQueueOrder).
func (m Manager) OrderQueue(items []QueueItem) (QueueOrder, error) {
	var sb strings.Builder
	sb.WriteString("The user queued these prompts while a worker was busy. They will run one after another, each by its own worker, each seeing the answers before it. Decide the order that makes the best use of the workers' time.\n\n")
	sb.WriteString("Rules:\n- Work that changes an artifact (a paper, code, a roadmap, data) runs BEFORE work that reviews, grades, summarizes or tests that artifact, so the review reads the final version.\n- When one prompt needs another's result, the needed one runs first.\n- Prompts that do not depend on each other keep the user's order.\n- A NOTE is not work: it amends the prompt it concerns. Name that prompt.\n\n")
	for i, it := range items {
		kind := "PROMPT"
		if it.Note {
			kind = "NOTE"
		}
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, kind, truncateStr(strings.Join(strings.Fields(it.Text), " "), 700))
	}
	sb.WriteString("\nReply with STRICT JSON only: {\"order\":[<PROMPT numbers, every one exactly once>],\"notes\":{\"<NOTE number>\":<PROMPT number>},\"why\":\"<=160 chars\"}")
	var out struct {
		Order []int          `json:"order"`
		Notes map[string]int `json:"notes"`
		Why   string         `json:"why"`
	}
	if err := m.directorJSON(sb.String(), &out); err != nil {
		return TypedQueueOrder(items), err
	}
	plan := QueueOrder{Notes: map[int]int{}, Why: strings.TrimSpace(out.Why)}
	for _, n := range out.Order {
		plan.Order = append(plan.Order, n-1)
	}
	for k, v := range out.Notes {
		var n int
		if _, err := fmt.Sscanf(k, "%d", &n); err == nil {
			plan.Notes[n-1] = v - 1
		}
	}
	return CheckQueueOrder(plan, items), nil
}

// TypedQueueOrder runs the prompts as typed, each note joining the next
// prompt after it (the previous one when it is last).
func TypedQueueOrder(items []QueueItem) QueueOrder {
	plan := QueueOrder{Notes: map[int]int{}}
	for i, it := range items {
		if !it.Note {
			plan.Order = append(plan.Order, i)
		}
	}
	for i, it := range items {
		if it.Note {
			plan.Notes[i] = nearestPrompt(items, i)
		}
	}
	return plan
}

// CheckQueueOrder keeps the director's order when it names every prompt
// exactly once, and each note's target when it is a prompt; anything else
// falls back on the typed order for that part.
func CheckQueueOrder(plan QueueOrder, items []QueueItem) QueueOrder {
	typed := TypedQueueOrder(items)
	seen := map[int]bool{}
	valid := len(plan.Order) == len(typed.Order)
	for _, i := range plan.Order {
		if i < 0 || i >= len(items) || items[i].Note || seen[i] {
			valid = false
			break
		}
		seen[i] = true
	}
	out := QueueOrder{Order: typed.Order, Notes: map[int]int{}, Why: plan.Why}
	if valid {
		out.Order = plan.Order
	} else {
		out.Why = ""
	}
	for i, it := range items {
		if !it.Note {
			continue
		}
		if t, ok := plan.Notes[i]; ok && t >= 0 && t < len(items) && !items[t].Note {
			out.Notes[i] = t
		} else {
			out.Notes[i] = typed.Notes[i]
		}
	}
	return out
}

func nearestPrompt(items []QueueItem, i int) int {
	for j := i + 1; j < len(items); j++ {
		if !items[j].Note {
			return j
		}
	}
	for j := i - 1; j >= 0; j-- {
		if !items[j].Note {
			return j
		}
	}
	return -1
}
