package review

import (
	"errors"
	"fmt"
	"strings"
)

const ciStepReserveBytes = 1024

var ErrPRReviewBodyTooLarge = errors.New("pr review body required sections exceed the size limit")

// TruncatePRReviewBody drops complete non-CI Pipeline evidence blocks from the
// end of the pipeline. It never cuts Markdown mid-block, preserves sections
// 1–3, and reserves room for pr create to expand the ci placeholder in place.
func TruncatePRReviewBody(body string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", fmt.Errorf("%w: non-positive limit", ErrPRReviewBodyTooLarge)
	}
	current := body
	omitted := 0
	for {
		ci, ok := pipelineCIBlock(current)
		if !ok {
			return "", fmt.Errorf("%w: missing ci Pipeline block", ErrPRReviewBodyTooLarge)
		}
		if len(current)-len(ci.text)+ciStepReserveBytes <= maxBytes {
			return current, nil
		}
		blocks := pipelineBlocks(current)
		candidate := -1
		for i := len(blocks) - 1; i >= 0; i-- {
			if !blocks[i].ci {
				candidate = i
				break
			}
		}
		if candidate < 0 {
			return "", ErrPRReviewBodyTooLarge
		}
		block := blocks[candidate]
		omitted++
		replacement := fmt.Sprintf("_Evidence for %s omitted; see .vas_sentinel/evidence/._\n", block.step)
		current = current[:block.start] + replacement + current[block.end:]
		current = removeTruncationNotice(current)
		ci, _ = pipelineCIBlock(current)
		notice := fmt.Sprintf("_%d evidence blocks omitted for size; the full logs are under .vas_sentinel/evidence/._\n", omitted)
		current = current[:ci.start] + notice + current[ci.start:]
	}
}

type pipelineBlock struct { start, end int; step, text string; ci bool }

func pipelineCIBlock(body string) (pipelineBlock, bool) {
	for _, block := range pipelineBlocks(body) {
		if block.ci { return block, true }
	}
	return pipelineBlock{}, false
}

func removeTruncationNotice(body string) string {
	const suffix = " evidence blocks omitted for size; the full logs are under .vas_sentinel/evidence/._\n"
	at := strings.Index(body, suffix)
	if at < 0 {
		return body
	}
	start := strings.LastIndex(body[:at], "\n") + 1
	return body[:start] + body[at+len(suffix):]
}

func pipelineBlocks(body string) []pipelineBlock {
	var out []pipelineBlock
	for offset := 0; ; {
		start := strings.Index(body[offset:], "<details><summary>")
		if start < 0 { return out }
		start += offset
		endMarker := strings.Index(body[start:], "</details>")
		if endMarker < 0 { return out }
		end := start + endMarker + len("</details>")
		text := body[start:end]
		step := "pipeline step"
		if label := strings.Index(text, "<b>"); label >= 0 {
			if close := strings.Index(text[label+3:], "</b>"); close >= 0 { step = text[label+3 : label+3+close] }
		}
		out = append(out, pipelineBlock{start: start, end: end, step: step, text: text, ci: strings.Contains(text, "<b>ci</b>")})
		offset = end
	}
}
