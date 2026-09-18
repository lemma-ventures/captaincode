package captaincode

import (
	"os"
	"regexp"
	"strings"
)

// visionLegs marks legs whose models accept image input. The subscription
// frontier legs all do; the OSS/NIM text models don't. A task that references
// an image must never route to a blind leg (live 2026-07-19: a "see
// screens/bug-logo.png" task hit free/deepseek-flash, which could only answer
// "I can't view the image" and then stalled). Override the set with
// CAPTAIN_VISION_LEGS (comma-separated leg names).
var visionLegs = defaultVisionLegs()

func defaultVisionLegs() map[Leg]bool {
	out := map[Leg]bool{}
	for l, s := range specs {
		if s.Vision && !s.Disabled {
			out[l] = true
		}
	}
	return out
}

// reloadVisionLegs re-reads CAPTAIN_VISION_LEGS (tests call it around Setenv).
func reloadVisionLegs() {
	v := os.Getenv("CAPTAIN_VISION_LEGS")
	if v == "" {
		visionLegs = defaultVisionLegs()
		return
	}
	set := map[Leg]bool{}
	for _, s := range strings.Split(v, ",") {
		if l := Leg(strings.TrimSpace(s)); l != "" {
			set[l] = true
		}
	}
	visionLegs = set
}

// LegSupportsVision reports whether a leg's model accepts image input.
func LegSupportsVision(l Leg) bool { return visionLegs[l] }

// FilterVision keeps only vision-capable legs, preserving order.
func FilterVision(order []Leg) []Leg {
	out := order[:0:0]
	for _, l := range order {
		if visionLegs[l] {
			out = append(out, l)
		}
	}
	return out
}

// imageRef matches concrete image-file references (path.png, shot.jpeg …) and
// screenshot language. Deliberately narrow: "the png encoder" or image_test.go
// must not trip it - a false positive merely routes to a stronger leg, but the
// pattern should still mean "there is a picture to look at".
var imageRef = regexp.MustCompile(`(?i)([^\s]+\.(png|jpe?g|gif|webp|bmp)\b|screen(?:[\s_-]*)(?:shot|capture)|screenshot|screencapture|\bs+e+\s+screen\s+(?:shot|capture))`)

// TaskNeedsVision reports whether the task references an image the worker
// will need to actually look at.
func TaskNeedsVision(task string) bool { return imageRef.MatchString(task) }
