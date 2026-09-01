package swarm

// delivery_readback_integration_test.go — bd-ljd integration coverage on real
// tmux panes. Two fixtures reproduce the two sides of the incident:
//
//   - the wedge: an agent-shaped pane with echo off that renders the exact
//     visual state the o62l shakedown found (pi banner, 0.0% context meter,
//     no prompt) and silently drains every typed byte. send-keys succeeds,
//     the marching orders are never seen. The read-back must re-send exactly
//     once and then report the pane by name.
//
//   - the friendly pane: an echo-on pane that shows what it receives. Its
//     capture shows the orders landed, so the read-back must confirm without
//     a single re-send — a duplicate prompt to a working agent is its own
//     damage.
//
// Delivery accounting uses a drain log: every byte the fixture receives is
// appended to a file, so the test counts actual deliveries rather than
// inferring them from the pane surface.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func bdLjdRequireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not present; skipping bd-ljd integration fixture")
	}
	if testing.Short() {
		t.Skip("tmux integration skipped under -short")
	}
}

func bdLjdWriteScript(t *testing.T, content string) string {
	t.Helper()
	path := fmt.Sprintf("%s/bd-ljd-%d.sh", t.TempDir(), time.Now().UnixNano())
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fixture script: %v", err)
	}
	return path
}

// bdLjdWaitForHeader polls the session's first pane until the fixture header
// has rendered, returning the pane id. Mirrors the bd-ift fixture boot wait.
func bdLjdWaitForHeader(t *testing.T, session, marker string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		panes, err := tmux.GetPanes(session)
		if err == nil && len(panes) > 0 {
			captured, captureErr := tmux.CapturePaneOutput(panes[0].ID, 40)
			if captureErr == nil && strings.Contains(captured, marker) {
				return panes[0].ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fixture never rendered its header %q", marker)
	return ""
}

func bdLjdCountInFile(t *testing.T, path, needle string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture drain log: %v", err)
	}
	return strings.Count(string(raw), needle)
}

func bdLjdNewInjector() *PromptInjector {
	injector := NewPromptInjector()
	injector.DeliverySettleDelay = 20 * time.Millisecond
	injector.DeliveryPollInterval = 25 * time.Millisecond
	injector.DeliveryConfirmWindow = 200 * time.Millisecond
	injector.EnterDelay = 10 * time.Millisecond
	return injector
}

func bdLjdSinglePanePlan(session string) *SwarmPlan {
	return &SwarmPlan{
		CreatedAt: time.Now().UTC(),
		Sessions: []SessionSpec{{
			Name:      session,
			AgentType: "pi",
			PaneCount: 1,
			Panes:     []PaneSpec{{Index: 1, AgentType: "pi", Project: session}},
		}},
	}
}

// TestSpawnDeliveryWedgeTriggersOneRetryThenLoudReport is acceptance
// criterion 1 end-to-end: a pane whose input is blocked receives the marching
// orders (send-keys returns success), the read-back finds no evidence, the
// delivery is retried exactly once, and the pane is reported loudly by name.
func TestSpawnDeliveryWedgeTriggersOneRetryThenLoudReport(t *testing.T) {
	bdLjdRequireTmux(t)
	projectDir := t.TempDir()
	drainLog := fmt.Sprintf("%s/drain.log", t.TempDir())

	// The wedge renders the incident's exact visual state: a pi banner and a
	// 0.0% context meter, with echo off so the typed orders never appear.
	// Every byte is drained into the log so the test can count deliveries.
	wedge := fmt.Sprintf(`#!/bin/sh
stty -echo 2>/dev/null
printf ' pi v0.84.2\n'
printf ' escape interrupt · ctrl+c/ctrl+d clear/exit\n'
printf 'TUI: trust dialog - press ESC to dismiss\n'
printf '/home/gabriel/repositories/daytrace\n'
printf '0.0%%%%/262k (auto)                              (litellm) kimi-k2.7\n'
while IFS= read -r -n 1 -s _byte; do
  printf '%%s' "$_byte" >> %q
done
`, drainLog)
	script := bdLjdWriteScript(t, wedge)

	session := fmt.Sprintf("bd-ljd-wedge-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	if err := tmux.SendKeys(session, script, true); err != nil {
		t.Fatalf("start wedge fixture: %v", err)
	}
	bdLjdWaitForHeader(t, session, "trust dialog")

	prompt := bdLjdProbePrompt(fmt.Sprintf("wedge-%d", time.Now().UnixNano()))
	injector := bdLjdNewInjector()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := injector.InjectSwarmWithContext(ctx, bdLjdSinglePanePlan(session), prompt)
	if err != nil {
		t.Fatalf("InjectSwarmWithContext: %v", err)
	}
	if result == nil || len(result.Results) != 1 {
		t.Fatalf("expected one result, got %+v", result)
	}

	res := result.Results[0]
	if !res.Success {
		t.Fatalf("the wedge swallows input while send-keys reports success; send reported failure: %+v", res)
	}
	if res.DeliveryConfirmed {
		t.Fatalf("wedged pane must not be reported confirmed: %+v", res)
	}
	if !res.DeliveryRetried {
		t.Fatalf("the unconfirmable pane must be retried once: %+v", res)
	}
	if !strings.Contains(res.DeliveryError, session) && !strings.Contains(res.DeliveryError, res.SessionPane) {
		t.Fatalf("the loud report must name the pane: %q", res.DeliveryError)
	}
	if result.Unconfirmed != 1 {
		t.Fatalf("Unconfirmed = %d, want 1", result.Unconfirmed)
	}
	panes := result.UnconfirmedPanes()
	if len(panes) != 1 || !strings.Contains(panes[0], session) {
		t.Fatalf("UnconfirmedPanes = %v, want the wedge pane", panes)
	}

	// Exactly one retry: the wedge drained the prompt exactly twice — the
	// initial delivery and one re-send, never a third.
	if got := bdLjdCountInFile(t, drainLog, prompt); got != 2 {
		t.Fatalf("wedge must have received the prompt exactly 2 times (initial + one retry), got %d", got)
	}
}

// TestSpawnDeliveryConfirmedPaneIsNeverResent is acceptance criterion 2's
// planted negative, end-to-end: a pane whose capture shows the orders landed
// is confirmed by the read-back and must not receive a second copy.
func TestSpawnDeliveryConfirmedPaneIsNeverResent(t *testing.T) {
	bdLjdRequireTmux(t)
	projectDir := t.TempDir()
	drainLog := fmt.Sprintf("%s/echo.log", t.TempDir())

	// The friendly fixture echoes what it receives (echo stays on) and logs
	// each received line, so the capture shows the orders landed. The pi
	// status line keeps the readiness wait cheap and the meter at 0.0%, so
	// the confirmation here can only come from the orders-text signal.
	friendly := fmt.Sprintf(`#!/bin/sh
printf 'Agent ready\n'
printf '0.0%%%%/262k (auto)\n'
printf '❯ '
while IFS= read -r _line; do
  printf '%%s\n' "$_line" >> %q
  printf '\nRECEIVED: %%s\n❯ ' "$_line"
done
`, drainLog)
	script := bdLjdWriteScript(t, friendly)

	session := fmt.Sprintf("bd-ljd-echo-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	if err := tmux.SendKeys(session, script, true); err != nil {
		t.Fatalf("start friendly fixture: %v", err)
	}
	bdLjdWaitForHeader(t, session, "Agent ready")

	prompt := bdLjdProbePrompt(fmt.Sprintf("echo-%d", time.Now().UnixNano()))
	injector := bdLjdNewInjector()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := injector.InjectSwarmWithContext(ctx, bdLjdSinglePanePlan(session), prompt)
	if err != nil {
		t.Fatalf("InjectSwarmWithContext: %v", err)
	}
	if result == nil || len(result.Results) != 1 {
		t.Fatalf("expected one result, got %+v", result)
	}

	res := result.Results[0]
	if !res.Success {
		t.Fatalf("healthy pane send must succeed: %+v", res)
	}
	if !res.DeliveryConfirmed {
		t.Fatalf("echoing pane must be confirmed by the read-back: %+v", res)
	}
	if res.DeliverySignal != swarmDeliverySignalOrdersText {
		t.Fatalf("confirmation signal = %q, want orders_text", res.DeliverySignal)
	}
	if res.DeliveryRetried {
		t.Fatalf("a confirmed pane must never be re-sent: %+v", res)
	}
	if result.Unconfirmed != 0 {
		t.Fatalf("Unconfirmed = %d, want 0", result.Unconfirmed)
	}

	if got := bdLjdCountInFile(t, drainLog, prompt); got != 1 {
		t.Fatalf("healthy pane must receive the orders exactly once, got %d", got)
	}
}
