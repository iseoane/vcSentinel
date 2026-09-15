package review

import (
	"errors"
	"fmt"
	"strings"
)

// CIStepReserveBytes is the room kept for pr create to expand the ci step in
// place (piece 5 replaces that one block and nothing else). The reserve is a
// bound, not a guess: pr create caps the run URL at 200 bytes and the failed
// job names at five names of seventy bytes, rendering "… and N more" beyond
// that, and the surrounding markup is fixed.
const CIStepReserveBytes = 1024

var ErrPRReviewBodyTooLarge = errors.New("pr review body required sections exceed the size limit")

// pipelineStep is one Pipeline entry before it becomes Markdown. The Pipeline
// is built as data and serialised once, at the end, which is what makes
// truncation safe: dropping a step is dropping an element of a slice, so it
// cannot reach another section, cannot stop at the wrong closing tag, and
// cannot mistake a step for the ci one because its own text happens to
// contain "<b>ci</b>". An earlier version truncated by re-parsing the
// serialised body and had all three of those defects.
type pipelineStep struct {
	Icon     string
	Step     string
	Status   string
	Summary  string
	Evidence string
	CI       bool
	// dropped marks a step whose evidence was already replaced by the
	// pointer to the log on disk. Without it the truncation loop keeps
	// choosing the same step forever, because the replacement text is
	// itself non-empty evidence.
	dropped bool
}

func (s pipelineStep) render() string {
	return pipelineDetails(s.Icon, s.Step, s.Summary, s.Evidence)
}

// omitted returns the step with its evidence replaced by a pointer to the log
// on disk. The step itself never disappears: a reader must still see that it
// ran, and what its outcome was.
func (s pipelineStep) omitted() pipelineStep {
	s.Evidence = "_Evidence omitted for size; the full log is under .vas_sentinel/evidence/._\n"
	s.dropped = true
	return s
}

// renderPipelineSteps serialises the steps in order.
func renderPipelineSteps(steps []pipelineStep) string {
	var b strings.Builder
	for _, step := range steps {
		b.WriteString(step.render())
	}
	return b.String()
}

// truncatePipeline drops evidence from non-CI steps, last one first, until the
// whole body fits. It returns the serialised Pipeline section and the number
// of steps whose evidence was dropped.
//
// `fixed` is everything that precedes the Pipeline: sections 1 to 3, Testing,
// and the attestation. Those are never touched — if they alone do not fit,
// that is an error, not something to trim.
func truncatePipeline(fixed string, steps []pipelineStep, maxBytes int) (string, int, error) {
	if maxBytes <= 0 {
		return "", 0, fmt.Errorf("%w: non-positive limit", ErrPRReviewBodyTooLarge)
	}
	if !hasCIStep(steps) {
		return "", 0, fmt.Errorf("%w: missing ci Pipeline step", ErrPRReviewBodyTooLarge)
	}

	working := make([]pipelineStep, len(steps))
	copy(working, steps)
	omitted := 0
	for {
		rendered := renderPipelineSteps(working)
		notice := truncationNotice(omitted)
		// The ci step is measured at its reserved size INSTEAD OF its current
		// one, not on top of it: pr create replaces that one block, so the
		// placeholder it overwrites does not survive into the published body.
		// Counting both rejected bodies that fit perfectly well once expanded.
		if publishedSize(fixed, rendered, notice, working) <= maxBytes {
			return rendered + notice, omitted, nil
		}
		candidate := -1
		for i := len(working) - 1; i >= 0; i-- {
			if !working[i].CI && !working[i].dropped && strings.TrimSpace(working[i].Evidence) != "" {
				candidate = i
				break
			}
		}
		if candidate < 0 {
			return "", 0, ErrPRReviewBodyTooLarge
		}
		working[candidate] = working[candidate].omitted()
		omitted++
	}
}

// publishedSize is the size the body will have after pr create expands the ci
// step: everything as rendered, minus the ci placeholder, plus the reserve
// that bounds its replacement. It is the one place that arithmetic lives, so
// the loop and the caller's final guard cannot disagree about it.
func publishedSize(fixed, rendered, notice string, steps []pipelineStep) int {
	size := len(fixed) + len(rendered) + len(notice) + CIStepReserveBytes
	for _, step := range steps {
		if step.CI {
			size -= len(step.render())
			break
		}
	}
	return size
}

func truncationNotice(omitted int) string {
	if omitted == 0 {
		return ""
	}
	return fmt.Sprintf("_%d evidence blocks omitted for size; the full logs are under .vas_sentinel/evidence/._\n", omitted)
}

func hasCIStep(steps []pipelineStep) bool {
	for _, step := range steps {
		if step.CI {
			return true
		}
	}
	return false
}
