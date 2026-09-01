package status

import (
	"strings"
	"testing"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no ansi",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "color codes",
			input:    "\x1b[32mgreen\x1b[0m text",
			expected: "green text",
		},
		{
			name:     "multiple codes",
			input:    "\x1b[1m\x1b[34mbold blue\x1b[0m",
			expected: "bold blue",
		},
		{
			name:     "cursor movement",
			input:    "\x1b[2Jclear screen",
			expected: "clear screen",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripANSI(tt.input)
			if result != tt.expected {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsPromptLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		agentType string
		expected  bool
	}{
		// Claude prompts
		{name: "claude prompt lowercase", line: "claude>", agentType: "cc", expected: true},
		{name: "claude long-form alias", line: "claude>", agentType: "claude", expected: true},
		{name: "claude prompt with space", line: "claude> ", agentType: "cc", expected: true},
		{name: "Claude prompt uppercase", line: "Claude>", agentType: "cc", expected: true},

		// Codex prompts
		{name: "codex prompt", line: "codex>", agentType: "cod", expected: true},
		{name: "codex long-form alias", line: "codex>", agentType: " CodEx ", expected: true},
		{name: "codex chevron prompt", line: "› Write tests for @filename", agentType: "cod", expected: true},
		// Shell prompts should NOT match for known agent types - a shell $ in cod/cc/gmi means agent exited
		{name: "shell prompt for codex means exited", line: "user@host:~$", agentType: "cod", expected: false},
		{name: "shell prompt for codex alias means exited", line: "user@host:~$", agentType: "codex", expected: false},

		// Gemini prompts
		{name: "gemini prompt", line: "gemini>", agentType: "gmi", expected: true},
		{name: "gemini long-form alias", line: "gemini>", agentType: "gemini", expected: true},
		{name: "Gemini prompt", line: "Gemini>", agentType: "gmi", expected: true},

		// User shell prompts
		{name: "dollar prompt", line: "user@host:~$ ", agentType: "user", expected: true},
		{name: "percent prompt", line: "user@host %", agentType: "user", expected: true},
		{name: "starship prompt", line: "~/project ❯", agentType: "user", expected: true},

		// Generic prompts
		{name: "generic > prompt", line: ">", agentType: "", expected: true},
		{name: "generic > prompt with space", line: "> ", agentType: "", expected: true},

		// Non-prompts
		{name: "regular text", line: "hello world", agentType: "cc", expected: false},
		{name: "empty string", line: "", agentType: "cc", expected: false},
		{name: "whitespace only", line: "   ", agentType: "cc", expected: false},

		// With ANSI codes
		{name: "prompt with ansi", line: "\x1b[32mclaude>\x1b[0m", agentType: "cc", expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsPromptLine(tt.line, tt.agentType)
			if result != tt.expected {
				t.Errorf("IsPromptLine(%q, %q) = %v, want %v", tt.line, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestDetectIdleFromOutput(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		{
			name:      "claude idle at prompt",
			output:    "Some previous output\nMore text\nclaude>",
			agentType: "cc",
			expected:  true,
		},
		{
			name:      "claude working",
			output:    "Processing request...\nGenerating code...\n",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "claude prompt with trailing newlines",
			output:    "Output\nclaude>\n\n",
			agentType: "cc",
			expected:  true,
		},
		{
			name:      "codex at shell prompt means agent exited not idle",
			output:    "Command completed\nuser@host:~$",
			agentType: "cod",
			expected:  false, // shell prompt in cod pane means agent exited, not idle at codex> prompt
		},
		{
			name:      "codex alias at shell prompt still means exited not idle",
			output:    "Command completed\nuser@host:~$",
			agentType: "codex",
			expected:  false,
		},
		{
			name:      "codex at codex prompt",
			output:    "Command completed\ncodex>",
			agentType: "cod",
			expected:  true, // actual codex prompt means idle
		},
		{
			name:      "codex alias at codex prompt",
			output:    "Command completed\ncodex>",
			agentType: " CodEx ",
			expected:  true,
		},
		{
			name:      "codex at chevron prompt",
			output:    "Command completed\n› Write tests for @filename",
			agentType: "cod",
			expected:  true, // codex chevron prompt means idle
		},
		{
			name:      "gemini idle",
			output:    "Response complete.\ngemini>",
			agentType: "gmi",
			expected:  true,
		},
		{
			name:      "empty output",
			output:    "",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "only whitespace",
			output:    "\n\n   \n",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "output with ansi codes",
			output:    "\x1b[32mSuccess!\x1b[0m\n\x1b[34mclaude>\x1b[0m",
			agentType: "cc",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectIdleFromOutput(tt.output, tt.agentType)
			if result != tt.expected {
				t.Errorf("DetectIdleFromOutput(%q, %q) = %v, want %v",
					tt.output, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestGetLastNonEmptyLine(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "simple output",
			output:   "line1\nline2\nline3",
			expected: "line3",
		},
		{
			name:     "trailing newlines",
			output:   "line1\nline2\n\n\n",
			expected: "line2",
		},
		{
			name:     "with ansi",
			output:   "\x1b[32mcolored\x1b[0m\n",
			expected: "colored",
		},
		{
			name:     "empty",
			output:   "",
			expected: "",
		},
		{
			name:     "only whitespace",
			output:   "   \n\t\n  ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetLastNonEmptyLine(tt.output)
			if result != tt.expected {
				t.Errorf("GetLastNonEmptyLine(%q) = %q, want %q",
					tt.output, result, tt.expected)
			}
		})
	}
}

func TestIsPromptLine_LiteralMatch(t *testing.T) {
	// Test that literal matching works (for patterns that use Literal instead of Regex)
	// First add a literal pattern for testing
	originalLen := len(promptPatterns)

	// Add a test pattern with Literal
	promptPatterns = append(promptPatterns, PromptPattern{
		AgentType:   "test",
		Literal:     "test_prompt$",
		Description: "test literal prompt",
	})

	defer func() {
		// Restore original patterns
		promptPatterns = promptPatterns[:originalLen]
	}()

	// Test literal matching
	if !IsPromptLine("command test_prompt$", "test") {
		t.Error("should match literal prompt suffix")
	}
}

func TestIsPromptLine_AgentTypeFiltering(t *testing.T) {
	// Test that patterns are filtered by agent type
	// Note: Generic patterns (empty AgentType) match ALL agent types as fallback
	tests := []struct {
		line      string
		agentType string
		expected  bool
	}{
		// Cursor patterns match cursor agent type
		{"cursor>", "cursor", true},
		// Generic pattern ">$" is a fallback that matches any agent type
		{"cursor>", "cc", true}, // Falls through to generic ">$" pattern

		// Windsurf patterns match windsurf
		{"windsurf>", "windsurf", true},
		// Generic fallback pattern matches
		{"windsurf>", "cod", true}, // Falls through to generic ">$" pattern

		// Aider patterns
		{"aider>", "aider", true},
		// Generic fallback pattern matches
		{"aider>", "gmi", true}, // Falls through to generic ">$" pattern

		// But non-prompt lines don't match
		{"just some text", "cursor", false},
		{"running command...", "windsurf", false},
	}

	for _, tt := range tests {
		t.Run(tt.line+"_"+tt.agentType, func(t *testing.T) {
			result := IsPromptLine(tt.line, tt.agentType)
			if result != tt.expected {
				t.Errorf("IsPromptLine(%q, %q) = %v, want %v",
					tt.line, tt.agentType, result, tt.expected)
			}
		})
	}
}

func TestDetectIdleFromOutput_MultipleLines(t *testing.T) {
	// DetectIdleFromOutput scans up to maxIdleScanLines (12) trailing
	// non-empty lines for a prompt, then rejects the verdict if an active
	// spinner sits below the matched prompt.
	tests := []struct {
		name      string
		output    string
		agentType string
		expected  bool
	}{
		{
			// Prompt in second-to-last non-empty line
			name:      "prompt in second line from end",
			output:    "output\nclaude>\n\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt within the scan window is still detected
			name:      "prompt in third line from end",
			output:    "claude>\nfollowup\nmore",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt a handful of lines back (the old 3-line window missed
			// this; the wider window catches it).
			name:      "prompt 5 lines from end within window",
			output:    "claude>\na\nb\nc\nd",
			agentType: "cc",
			expected:  true,
		},
		{
			// REAL Claude layout (from internal/cli/outputs/): the "❯ " input
			// box is pinned to the BOTTOM, with the status footer below it and
			// the (now finished) turn's content above. No active spinner is the
			// most-recent dynamic marker, so the pane is idle and dispatchable.
			name: "cc finished turn with bottom-pinned box is idle",
			output: "● All changes applied; tests pass.\n" +
				"\n" +
				"✻ Cooked for 2m 10s\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"  ⏵⏵ bypass permissions on (shift+tab to cycle)\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// CRITICAL false-idle guard, REAL layout: the active spinner renders
			// just ABOVE the bottom-pinned input box while a turn is in flight.
			// The box is always drawn, so its presence is NOT idleness — the
			// most-recent dynamic marker is the spinner. MUST NOT report idle.
			name: "cc working with spinner above bottom box",
			output: "● Running the suite.\n" +
				"✻ Scurrying… (ctrl+c to interrupt · 12s · thinking)\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"  ⏵⏵ bypass permissions on\n",
			agentType: "cc",
			expected:  false,
		},
		{
			// "new task?" footer parked below the box after a turn ends, with no
			// active spinner — idle and dispatchable.
			name: "cc new task footer is idle",
			output: "● Done.\n" +
				"✻ Baked for 5m 0s\n" +
				"───────────\n" +
				"❯ \n" +
				"───────────\n" +
				"new task? /clear to save 12.3k tokens\n",
			agentType: "cc",
			expected:  true,
		},
		{
			// Prompt beyond the scan window must NOT be detected — guard against
			// false positives from very-old prompt text deep in scrollback.
			name: "prompt beyond scan window",
			output: "claude>\n" +
				"l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10\n" +
				"l11\nl12\nl13",
			agentType: "cc",
			expected:  false,
		},
		{
			name:      "prompt as last line after work output",
			output:    "exec /bin/bash --norc --noprofile\necho BASH_READY\nPS1='$ '; echo IDLE_MARKER\nIDLE_MARKER\n$",
			agentType: "user",
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DetectIdleFromOutput(tt.output, tt.agentType)
			if result != tt.expected {
				t.Errorf("DetectIdleFromOutput = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestDetectIdleFromOutputPi pins pi's arm. pi appears in no generic detection
// table, so without an arm of its own the line scan answers false for a pane
// sitting at a healthy prompt. classifyState already reads pi's predicates and
// returns StateIdle, but observationConfidence downgrades that idle to 0.5 when
// this function disagrees — under the 0.75 SafeToDispatch floor, which is why a
// live pi pane still timed out with `state=idle confidence=0.50` (bd-3nv,
// reproduced 2026-08-26).
func TestDetectIdleFromOutputPi(t *testing.T) {
	const chrome = "↑3.3M ↓8.2k 0.0%/1.0M (auto)            (litellm) deepseek-v4-pro-high-k3"

	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{"idle chrome only", "some transcript\n\n" + chrome, true},
		{"spinner above the chrome", "some transcript\n ⠹ Working...\n" + chrome, false},
		{"no chrome at all", "still booting\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectIdleFromOutput(tt.output, "pi"); got != tt.want {
				t.Errorf("DetectIdleFromOutput(pi) = %v, want %v", got, tt.want)
			}
		})
	}
}

// agyIdleFooterFixture is a shakedown-style capture of an Antigravity pane
// at its prompt with a memory/model footer pushing the chevron well past the
// parser's 5-line idle window. The fixture is reused by both the
// DetectIdleFromOutput table and the unified-detector table below so the two
// stay in sync — the bd-my3 fix adds a positive idle arm in both directions
// and a regression in either is a regression in the user-visible symptom.
const agyIdleFooterFixture = `gemini-2.5-pro /model | 396.8 MB

Antigravity connected

Task completed successfully.

running migration scripts
analyzing schema

What would you like next?

>>>

Memory: 312.4 MB used / 1.2 GB total
Model: gemini-2.5-pro (medium)
Ready for your next command`

// TestDetectIdleFromOutputAgy pins the bd-my3 positive-idle arm: a healthy
// Antigravity pane at its `>>>` (or `>` or `agy>`) chevron must read idle
// regardless of the scrollback underneath. The previous behavior — the line
// scan walked the last 12 lines, never found a recognized prompt (the chevron
// was beyond the footer in the fixture, and the parser's `>>>` shape was not
// in the gmi pattern set at all), and answered false — caused every
// `ntm assign --pane <agy>` to be refused as busy on a visibly-idle pane.
func TestDetectIdleFromOutputAgy(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "triple-chevron with footer pushing it past the parser's 5-line window",
			output: agyIdleFooterFixture,
			want:   true,
		},
		{
			name:   "single-chevron with the same footer",
			output: strings.Replace(agyIdleFooterFixture, ">>>\n", ">\n", 1),
			want:   true,
		},
		{
			name:   "agy-branded prompt with the same footer",
			output: strings.Replace(agyIdleFooterFixture, ">>>\n", "agy>\n", 1),
			want:   true,
		},
		{
			name:   "triple-chevron with a 30-line scrollback above it",
			output: strings.Repeat("running tests\n", 30) + ">>>\n",
			want:   true,
		},
		{
			name:   "no prompt at all (mid-boot)",
			output: "Starting Antigravity CLI...\nLoading workspace\nConnecting to Google AI Studio\n",
			want:   false,
		},
		{
			name:   "no prompt and scrollback full of working keywords",
			output: "running tests\nrunning tests\nanalyzing schema\ngenerating output\nexecuting tests\n",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectIdleFromOutput(tt.output, "agy"); got != tt.want {
				t.Errorf("DetectIdleFromOutput(agy) = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDetectIdleFromOutputAgyArmsChevronBeyondLineScanWindow is the
// failure mode the agy arm specifically cures. The line scan
// (maxIdleScanLines = 12) walks the trailing 12 non-empty lines and
// calls IsPromptLine. A production agy pane can put its chevron past
// that window with a memory/model footer, and the arm is the only
// positive-idle signal that catches the case. The mutation exercise:
// if the arm's agyTuiIdlePromptRe is removed or muted, this test
// fails on the shakedown-shape fixture (the chevron beyond the
// 12-line window) but the simpler fixtures below the window keep
// passing through the line scan, so the broken arm is exactly what
// the test exercises.
func TestDetectIdleFromOutputAgyArmsChevronBeyondLineScanWindow(t *testing.T) {
	// The chevron is at the top, then 18 non-empty "footer-like"
	// lines below it (memory, model, working-output echoes, etc.).
	// The line scan walks the trailing 12 non-empty lines and never
	// reaches the chevron. Only the agy arm (multiline match
	// anywhere in the output) returns true. This is the exact
	// shape the bd-my3 fix cured.
	lines := []string{
		">>>",
		"",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"running tests",
		"Memory: 312.4 MB used / 1.2 GB total",
		"Model: gemini-2.5-pro (medium)",
		"Ready for your next command",
	}
	capture := strings.Join(lines, "\n")
	nonEmpty := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonEmpty++
		}
	}
	if nonEmpty <= 12 {
		t.Fatalf("test fixture has only %d non-empty lines, want > 12 so the chevron is beyond the line scan's window", nonEmpty)
	}
	if got := DetectIdleFromOutput(capture, "agy"); !got {
		t.Fatalf("DetectIdleFromOutput(agy, chevron-beyond-window) = false, want true — the agy arm must catch the chevron past the line scan's 12-line window (non-empty lines: %d)", nonEmpty)
	}
}

// TestDetectIdleFromOutputAgyIsolatedFromGemini is a regression guard for
// the shared gmi/agy pattern set. Antigravity gets a positive idle arm in
// DetectIdleFromOutput (bd-my3); the legacy Gemini CLI must keep its
// existing behavior so TestSessionObserverAppliesPaneLocalActivityBound
// (which encodes bd-#234's first-observation-is-working verdict) stays
// green. The arm is keyed on AgentTypeAntigravity specifically: the
// production fixture's `>>>` chevron reaches the gmi line scan but the gmi
// pattern set only knows `gemini>`, so the gmi verdict on the agy fixture
// is "not idle" — the agy arm is the reason the same fixture classifies
// idle when fed as `agy`. Pinned here so a future refactor that lifts the
// arm out from under us surfaces as a test diff, not as a production
// regression.
func TestDetectIdleFromOutputAgyIsolatedFromGemini(t *testing.T) {
	if got := DetectIdleFromOutput(agyIdleFooterFixture, "gmi"); got {
		t.Errorf("DetectIdleFromOutput(gmi, agyFixture) = true, want false — gmi does not know the `>>>` chevron and the bd-my3 arm must not have leaked in")
	}
	if got := DetectIdleFromOutput(agyIdleFooterFixture, "agy"); !got {
		t.Errorf("DetectIdleFromOutput(agy, agyFixture) = false, want true — the bd-my3 arm is the reason this fixture now classifies idle")
	}
}
