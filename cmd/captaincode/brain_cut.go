package main

// The cut: how an over-budget replay is fitted without a summary call - when
// it is only a little over budget, when compaction is off, and while a
// summary is still being written (brain_compact.go). The old window kept the
// head and the tail of the raw text and cut the middle out mid-turn; this cut
// works in whole turns and drops the OLDEST history first:
//
//   - the framing before the first user turn is kept (shortened only when it
//     alone would take more than a quarter of the budget);
//   - the opening request is kept verbatim: it carries the session's intent;
//   - the current turn is kept whole (its middle is cut only when it alone
//     cannot fit);
//   - then the newest whole turns, back in time, as many as fit;
//   - what is dropped is the oldest middle of the conversation, and in its
//     place the worker gets the latest summary of it when one exists, and a
//     one-line index of each request that was left out.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lemma-ventures/captaincode/pkg/captaincode"
)

// turnMarkerRe finds the role markers promptFrom writes at the start of each
// message.
var turnMarkerRe = regexp.MustCompile(`(?m)^\[(user|assistant|system)\]\n`)

type turnSpan struct {
	role       string
	start, end int
}

// splitLead separates the framing before the first user turn from the
// conversation that follows it.
func splitLead(prompt string) (lead, conv string) {
	for _, m := range turnMarkerRe.FindAllStringSubmatchIndex(prompt, -1) {
		if prompt[m[2]:m[3]] == "user" {
			return prompt[:m[0]], prompt[m[0]:]
		}
	}
	return "", prompt
}

// splitTurns lists the messages of a conversation.
func splitTurns(conv string) []turnSpan {
	ms := turnMarkerRe.FindAllStringSubmatchIndex(conv, -1)
	out := make([]turnSpan, 0, len(ms))
	for i, m := range ms {
		end := len(conv)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		out = append(out, turnSpan{role: conv[m[2]:m[3]], start: m[0], end: end})
	}
	return out
}

const (
	cutOpeningMax = 4_000 // an opening paste is not intent
	cutAskLine    = 160   // one dropped request, in the index
)

// cutToFit fits prompt into budget chars by dropping the oldest turns.
// summary is the latest summary of the conversation, covering its first
// covered chars after the framing ("" when there is none).
func cutToFit(prompt string, budget int, summary string, covered int) string {
	if len(prompt) <= budget || budget <= 0 {
		return prompt
	}
	lead, conv := splitLead(prompt)
	turns := splitTurns(conv)
	first, last := -1, -1
	for i, t := range turns {
		if t.role == "user" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if last < 0 {
		return windowPrompt(prompt, budget)
	}
	if len(lead) > budget/4 {
		lead = captaincode.CutHead(lead, budget/8) + "\n[captain: the framing was shortened to fit]\n" + captaincode.CutTail(lead, budget/8) + "\n\n"
	}
	opening := ""
	if first < last {
		opening = strings.TrimRight(conv[turns[first].start:turns[first].end], "\n")
		if len(opening) > cutOpeningMax {
			opening = captaincode.CutHead(opening, cutOpeningMax) + "\n[captain: opening turn truncated]"
		}
		opening += "\n\n"
	}
	tail := conv[turns[last].start:]
	reserve := minInt(8_000, budget/20)
	const markerRoom = 400
	room := budget - len(lead) - len(opening) - len(tail) - reserve - markerRoom
	if room < 0 {
		// The current turn alone does not fit: keep how it starts and how it
		// ends, and nothing older but the opening request.
		keep := budget - len(lead) - len(opening) - markerRoom - reserve
		if keep < 2_000 {
			opening, keep = "", budget-len(lead)-markerRoom-reserve
		}
		tail = captaincode.CutHead(tail, keep*2/3) + "\n[captain: the middle of this turn was cut to fit]\n" + captaincode.CutTail(tail, keep/3)
		room = 0
	}
	lo := 0
	if opening != "" {
		lo = first + 1
	}
	from := last
	for i := last - 1; i >= lo; i-- {
		n := turns[i].end - turns[i].start
		if n > room {
			break
		}
		room -= n
		from = i
	}
	render := func(from int) string {
		dropped := turns[lo:from]
		var b strings.Builder
		b.WriteString(lead)
		b.WriteString(opening)
		if len(dropped) > 0 {
			chars := dropped[len(dropped)-1].end - dropped[0].start
			fmt.Fprintf(&b, "[system]\n[captain: %d earlier messages (%dk chars) between the opening request and the recent turns were left out to fit this model's context.]\n", len(dropped), (chars+999)/1000)
			b.WriteString(cutDigest(conv, dropped, summary, covered, reserve))
			b.WriteString("\n\n")
		}
		if from < last {
			b.WriteString(conv[turns[from].start:turns[last].start])
		}
		b.WriteString(tail)
		return b.String()
	}
	out := render(from)
	// The digest rarely needs its whole reserve: put older turns back while
	// the result still fits.
	for from > lo {
		more := render(from - 1)
		if len(more) > budget {
			break
		}
		out, from = more, from-1
	}
	if len(out) > budget {
		return windowPrompt(out, budget) // a last guard; the sums above leave room
	}
	return out
}

// cutDigest is what stands in for the dropped turns: the latest summary when
// it covers some of them, then a line per dropped request it does not cover,
// newest first while they fit, shown in order.
func cutDigest(conv string, dropped []turnSpan, summary string, covered, room int) string {
	var b strings.Builder
	if summary != "" && covered > dropped[0].start {
		s := "[captain: summary of the earlier conversation, from a previous turn]\n" + summary + "\n"
		if len(s) > room*3/4 {
			s = captaincode.CutHead(s, room*3/4) + "\n[captain: summary shortened]\n"
		}
		b.WriteString(s)
		room -= len(s)
	}
	var asks []string
	for i := len(dropped) - 1; i >= 0; i-- {
		t := dropped[i]
		if t.role != "user" || t.start < covered {
			continue
		}
		body := strings.Join(strings.Fields(strings.TrimPrefix(conv[t.start:t.end], "[user]\n")), " ")
		line := "- " + captaincode.CutHead(body, cutAskLine) + "\n"
		if len(line) > room {
			break
		}
		room -= len(line)
		asks = append(asks, line)
	}
	if len(asks) > 0 {
		b.WriteString("[captain: requests in the left-out part, oldest first]\n")
		for i := len(asks) - 1; i >= 0; i-- {
			b.WriteString(asks[i])
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
