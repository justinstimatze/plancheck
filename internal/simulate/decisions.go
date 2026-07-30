// decisions.go extracts the choices the spike had to invent.
//
// To write code at all, the agent must answer questions the plan left open:
// enum or bool, which package a new type lives in, which layer owns a
// conversion. It answers them silently. Where a different answer would have
// moved the file set, that silence hides a fork — and the alternative branch
// often names a file that does not exist yet, so no compiler, reference
// graph, or co-modification signal can ever reach it.
package simulate

import (
	"strings"
	"unicode"

	"github.com/justinstimatze/plancheck/internal/types"
)

// maxOpenDecisions caps how many forks we report. Past a handful the plan
// needs a conversation, not a longer list.
const maxOpenDecisions = 5

// Bounds on the parsed fields. Every string here is model-written and ends up
// in three places that cannot defend themselves: a terminal, a JSON response,
// and the summary injected into the calling agent's context. A decision that
// needs more than a couple of sentences to state is one the plan should be
// discussing anyway, so clamping costs nothing real.
const (
	maxDecisionField = 300
	maxAffects       = 8
)

// openDecisionsPrompt asks the agent to surface the choices it invented.
// The filter that matters is "would a different answer move the file set" —
// it drops naming and style bikesheds and keeps the forks plancheck exists
// to catch, and it keeps the response short enough to parse cheaply.
const openDecisionsPrompt = "Last question, then we're done.\n\n" +
	"Writing that code forced you to answer things the plan never specified — " +
	"whether a value is an enum or a bool, where a new type lives, whether to extend " +
	"an existing struct or add one, which layer owns a conversion.\n\n" +
	"List ONLY the ones where a different answer would have changed WHICH FILES you touched. " +
	"Skip naming and formatting choices. If there were none, reply exactly NONE.\n\n" +
	"Use exactly this format, one block per choice, no prose around them:\n\n" +
	"DECISION: <what the plan left open, phrased as a question>\n" +
	"CHOSE: <what you picked>\n" +
	"ALTERNATIVE: <the other plausible answer>\n" +
	"AFFECTS: <comma-separated files that differ between the two>"

// extractOpenDecisions parses the DECISION/CHOSE/ALTERNATIVE/AFFECTS blocks.
// A block needs at least a question and a choice to count; the agent
// sometimes omits AFFECTS when the fork is contained in files it already
// wrote, which is still worth reporting.
func extractOpenDecisions(text string) []types.OpenDecision {
	var out []types.OpenDecision
	var cur types.OpenDecision
	seen := make(map[string]bool)

	flush := func() {
		defer func() { cur = types.OpenDecision{} }()
		if cur.Question == "" || cur.Chose == "" {
			return
		}
		// The model sometimes restates a decision it already gave, especially
		// when it reformats mid-response. Keep the first.
		key := strings.ToLower(cur.Question)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, cur)
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "DECISION:"):
			flush()
			cur.Question = clampField(strings.TrimPrefix(line, "DECISION:"))
		case strings.HasPrefix(line, "CHOSE:"):
			cur.Chose = clampField(strings.TrimPrefix(line, "CHOSE:"))
		case strings.HasPrefix(line, "ALTERNATIVE:"):
			if alt := clampField(strings.TrimPrefix(line, "ALTERNATIVE:")); !strings.EqualFold(alt, "none") {
				cur.Alternative = alt
			}
		case strings.HasPrefix(line, "AFFECTS:"):
			for _, f := range strings.Split(strings.TrimPrefix(line, "AFFECTS:"), ",") {
				if len(cur.Affects) >= maxAffects {
					break
				}
				if f = cleanAffectedPath(f); f != "" {
					cur.Affects = append(cur.Affects, f)
				}
			}
		}
	}
	flush()
	return out
}

// clampField makes one model-written field safe to print and to hand back to
// another agent: length bounded, truncation marked rather than silent, and two
// classes of character removed. ASCII control codes, because an ANSI escape
// reaching a terminal through `plancheck check` would let spike output move the
// cursor and repaint the findings above it. Unicode format characters, because
// bidi overrides and zero-width joiners let a string render as something other
// than what the next reader — human or agent — is actually acting on.
func clampField(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	// Count runes, not bytes — byte slicing would split a multi-byte rune and
	// put invalid UTF-8 into the JSON response.
	if r := []rune(s); len(r) > maxDecisionField {
		s = strings.TrimSpace(string(r[:maxDecisionField])) + "…"
	}
	return s
}

// cleanAffectedPath normalizes one entry of an AFFECTS list. Unlike
// cleanGoPath this keeps bare filenames and _test.go paths — the list is
// context for a human reading the fork, not a file block to apply, and a
// decision that changes which tests exist is worth showing.
func cleanAffectedPath(f string) string {
	f = strings.TrimPrefix(clampField(f), "./")
	f = strings.Trim(f, "`* ")
	if !strings.HasSuffix(f, ".go") || strings.ContainsAny(f, " \t") {
		return ""
	}
	return f
}
