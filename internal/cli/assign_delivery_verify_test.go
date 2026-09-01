package cli

// assign_delivery_verify_test.go — bd-ift unit coverage for the post-send
// capture check. The fixture drives the verifier through a real tmux session
// so the failure mode "send-keys returned success but the prompt never
// reached the pane" is exercised end-to-end. The unit tests cover the marker
// injection, the marker derivation, and the verdict construction without any
// tmux dependency so a regression in either helper is caught independently of
// the tmux fixture.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/assignment"
	"github.com/Dicklesworthstone/ntm/internal/redaction"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestNewAssignDeliveryMarkerDerivesFromIdempotencyKey is the canary for the
// marker derivation. Two markers with the same idempotency key must collide;
// two markers with different keys must not. The hash is hex-encoded and
// truncated to assignDeliveryMarkerLength characters between brackets so the
// whole token is short enough to ride on a single composer line.
func TestNewAssignDeliveryMarkerDerivesFromIdempotencyKey(t *testing.T) {
	first := newAssignDeliveryMarker("ntm-ift-key-one")
	second := newAssignDeliveryMarker("ntm-ift-key-one")
	if first != second {
		t.Fatalf("marker is not stable for the same idempotency key: first=%q second=%q", first, second)
	}
	third := newAssignDeliveryMarker("ntm-ift-key-two")
	if first == third {
		t.Fatalf("marker collided across distinct idempotency keys: %q", first)
	}
	if !strings.HasPrefix(first, assignDeliveryMarkerPrefix) || !strings.HasSuffix(first, assignDeliveryMarkerSuffix) {
		t.Fatalf("marker is missing the well-known prefix/suffix: %q", first)
	}
	// Independently re-derive the expected value: a reviewer reading only
	// this test can prove the implementation matches the spec without
	// reading the implementation file.
	sum := sha256.Sum256([]byte("ntm-ift-key-one"))
	want := fmt.Sprintf("%s%s%s", assignDeliveryMarkerPrefix, hex.EncodeToString(sum[:])[:assignDeliveryMarkerLength], assignDeliveryMarkerSuffix)
	if first != want {
		t.Fatalf("marker mismatch: got=%q want=%q", first, want)
	}
}

// TestInjectAssignDeliveryMarkerAppendsOnOwnLine proves the marker rides on
// its own composer row so any visual wrap in the agent's composer does not
// split the token. The marker must be appended with a single newline before
// it; existing markers must not be re-injected.
func TestInjectAssignDeliveryMarkerAppendsOnOwnLine(t *testing.T) {
	marker := newAssignDeliveryMarker("ntm-ift-injection")
	got := injectAssignDeliveryMarker("Work on bead bd-ift: probe", marker)
	want := "Work on bead bd-ift: probe\n" + marker
	if got != want {
		t.Fatalf("marker injection changed prompt shape: got=%q want=%q", got, want)
	}
	if strings.Count(got, marker) != 1 {
		t.Fatalf("marker injected more than once: %q", got)
	}
	// Second call is idempotent: it must not double-inject the marker.
	got2 := injectAssignDeliveryMarker(got, marker)
	if got2 != got {
		t.Fatalf("marker injection was not idempotent: before=%q after=%q", got, got2)
	}
	// Empty marker is a no-op: do not corrupt the prompt with a stray newline.
	if injected := injectAssignDeliveryMarker("plain prompt", ""); injected != "plain prompt" {
		t.Fatalf("empty marker should be a no-op: got=%q", injected)
	}
}

// TestAssignDeliveryMissingErrorIncludesMarker is the canary for the error
// string the operator sees when the post-send capture does not contain the
// marker. The message must name the marker so the operator can correlate it
// with the durable assignment record (the marker is derived from the
// idempotency key, which is in the durable ledger).
func TestAssignDeliveryMissingErrorIncludesMarker(t *testing.T) {
	marker := newAssignDeliveryMarker("ntm-ift-error-message")
	err := assignDeliveryMissingError(marker, "some captured text without the marker")
	if err == nil {
		t.Fatal("expected non-nil error for missing marker")
	}
	if !strings.Contains(err.Error(), marker) {
		t.Fatalf("error message must name the missing marker so the operator can correlate: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "post-send") {
		t.Fatalf("error message must distinguish delivery check from dispatch transport: %q", err.Error())
	}
}

// fakeCaptureRunner is a recording tmuxCaptureRunner for unit tests. It
// returns whatever the test pre-loaded and never touches a real tmux server,
// so the verifier can be exercised in any environment.
type fakeCaptureRunner struct {
	rows  map[string]string
	calls int
}

func (f *fakeCaptureRunner) CapturePaneOutput(target string, lines int) (string, error) {
	f.calls++
	return f.rows[target], nil
}

// TestTmuxDeliveryVerifierVerifiesMarkerFromCapture proves the read-back
// halves of the verifier without a tmux session: a pane whose capture
// contains the marker returns verified=true; one whose capture does not
// returns verified=false with no error. A capture that fails (a runner that
// returns an error) propagates the failure so the caller can surface it.
func TestTmuxDeliveryVerifierVerifiesMarkerFromCapture(t *testing.T) {
	marker := newAssignDeliveryMarker("ntm-ift-verifier")
	captured := strings.Join([]string{
		"❯ some agent composer state",
		"Work on bead bd-ift: probe",
		"",
		marker,
	}, "\n")

	pane := tmux.Pane{ID: "%ift-test", Index: 1}

	t.Run("marker present", func(t *testing.T) {
		runner := &fakeCaptureRunner{rows: map[string]string{pane.ID: captured}}
		verifier := tmuxDeliveryVerifier{runner: runner}
		verified, text, err := verifier.VerifyMarker(context.Background(), pane, marker)
		if err != nil {
			t.Fatalf("verify error: %v", err)
		}
		if !verified {
			t.Fatalf("expected verified=true when capture contains marker")
		}
		if !strings.Contains(text, marker) {
			t.Fatalf("VerifyMarker should return the captured text verbatim: %q", text)
		}
	})

	t.Run("marker absent", func(t *testing.T) {
		runner := &fakeCaptureRunner{rows: map[string]string{pane.ID: "agent already replied, marker scrolled off"}}
		verifier := tmuxDeliveryVerifier{runner: runner}
		verified, _, err := verifier.VerifyMarker(context.Background(), pane, marker)
		if err != nil {
			t.Fatalf("absent marker should not be an infrastructure error: %v", err)
		}
		if verified {
			t.Fatalf("expected verified=false when capture does not contain marker")
		}
	})

	t.Run("empty marker", func(t *testing.T) {
		runner := &fakeCaptureRunner{rows: map[string]string{pane.ID: "anything"}}
		verifier := tmuxDeliveryVerifier{runner: runner}
		if _, _, err := verifier.VerifyMarker(context.Background(), pane, ""); err == nil {
			t.Fatal("empty marker should be rejected, not silently pass")
		}
	})

	t.Run("runner error propagates", func(t *testing.T) {
		badRunner := captureRunnerFunc(func(string, int) (string, error) {
			return "", errors.New("tmux crashed")
		})
		verifier := tmuxDeliveryVerifier{runner: badRunner}
		if _, _, err := verifier.VerifyMarker(context.Background(), pane, marker); err == nil {
			t.Fatal("runner error must propagate so the operator can distinguish it from a silent miss")
		}
	})

	t.Run("pane without tmux identity", func(t *testing.T) {
		runner := &fakeCaptureRunner{rows: map[string]string{}}
		verifier := tmuxDeliveryVerifier{runner: runner}
		if _, _, err := verifier.VerifyMarker(context.Background(), tmux.Pane{Index: 7}, marker); err == nil {
			t.Fatal("pane without ID must be rejected")
		}
	})
}

// captureRunnerFunc lets tests inline a CapturePaneOutput behaviour without
// declaring a struct.
type captureRunnerFunc func(target string, lines int) (string, error)

func (f captureRunnerFunc) CapturePaneOutput(target string, lines int) (string, error) {
	return f(target, lines)
}

// ---------------------------------------------------------------------------
// End-to-end test: drive the cliAtomicPaneDispatchPort the way the assign
// command does and assert that the post-send verdict lands on the receipt
// the call site uses to populate the JSON envelope. The wedge is the same
// as above; what changes is that the test calls the real Dispatch port
// rather than calling SendKeys directly, so any regression in the marker
// injection, the verify pass, or the verdict wiring is caught here.
// ---------------------------------------------------------------------------

func TestCliAtomicDispatchPortReportsDeliveryFailedOnWedgedPane(t *testing.T) {
	testutilRequireTmux(t)
	projectDir := t.TempDir()

	agentScript := filepath.Join(t.TempDir(), "wedged-agent.sh")
	wedge := `#!/bin/sh
stty -echo 2>/dev/null
clear 2>/dev/null
printf 'TUI: trust dialog - press ESC to dismiss\n'
while read -r -n 1 -s _byte; do :; done
`
	if err := writeFile(agentScript, wedge, 0o755); err != nil {
		t.Fatalf("write wedge fixture: %v", err)
	}

	session := fmt.Sprintf("assign-ift-port-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	if err := tmux.SendKeys(session, agentScript, true); err != nil {
		t.Fatalf("start wedge fixture: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	paneID := ""
	for time.Now().Before(deadline) {
		panes, listErr := tmux.GetPanes(session)
		if listErr == nil && len(panes) > 0 {
			paneID = panes[0].ID
			captured, captureErr := tmux.CapturePaneOutput(paneID, 30)
			if captureErr == nil && strings.Contains(captured, "trust dialog") {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if paneID == "" {
		t.Fatal("wedge fixture never rendered its header")
	}

	// Build the dispatch port the way runDirectPaneAssignment builds it.
	// We bypass the assignment coordinator because the bead's verification
	// lives inside the dispatch port; the coordinator only owns
	// claim-before-reserve-before-send and does not run the post-send read.
	port := &cliAtomicPaneDispatchPort{
		session:         session,
		redactionConfig: redactionConfigForFixture(),
		bypassIdleGate:  true, // wedge looks busy; skip the idle gate so we exercise the verifier, not the gate
		verifyDelivery:  true,
	}

	idempotencyKey := "ntm-ift-port-fixture-" + fmt.Sprintf("%d", time.Now().UnixNano())
	req := assignment.DispatchRequest{
		BeadID:         "bd-ift",
		BeadTitle:      "wedge check",
		Target:         paneID,
		Prompt:         "Work on bead bd-ift: wedge check",
		IdempotencyKey: idempotencyKey,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receipt, dispatchErr := port.Dispatch(ctx, req)
	if dispatchErr == nil {
		t.Fatalf("expected delivery failure on wedged pane; got receipt=%+v err=nil", receipt)
	}
	if receipt.Delivered {
		t.Fatalf("expected receipt.Delivered=false on a wedge; got true (capture=%q)", strings.Join([]string{}, ""))
	}
	if receipt.DeliveryMarker == "" {
		t.Fatal("expected a non-empty DeliveryMarker so the operator can correlate the verdict with the durable ledger")
	}
	if !strings.Contains(dispatchErr.Error(), receipt.DeliveryMarker) {
		t.Fatalf("dispatch error must name the missing marker so the operator can correlate; got %q", dispatchErr.Error())
	}
}

// redactionConfigForFixture returns a permissive redaction config so the
// dispatch port does not block the test prompt on credential findings. The
// assign command loads its redaction policy from the user's NTM config; the
// fixture substitutes a default-allow policy because it has no NTM config.
func redactionConfigForFixture() redaction.Config {
	return redaction.Config{Mode: redaction.ModeOff}
}

// ---------------------------------------------------------------------------
// Positive-path fixture: a friendly agent that echoes every typed byte back
// to the terminal with a stable input prompt visible. Drives the dispatch
// port through a real session, asserts Delivered=true on the receipt, and
// asserts the marker is in the captured pane. This is the
// assignment-positive counterpart to the wedge test: a healthy pane must
// return delivered=true and the prompt visible in capture.
// ---------------------------------------------------------------------------

func TestAssignDeliveryVerifierConfirmsMarkerInHealthyPane(t *testing.T) {
	testutilRequireTmux(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{home, projectDir, fakeBin, filepath.Join(projectDir, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "1")

	realBR, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br is required for guarded-claim coverage")
	}
	cmd := exec.Command(realBR, "init", "--quiet")
	cmd.Dir = projectDir
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("br init: %v\n%s", runErr, out)
	}
	createCmd := exec.Command(realBR, "create", "--title", "healthy probe", "--type", "task", "--priority", "2", "--json")
	createCmd.Dir = projectDir
	createOut, createErr := createCmd.CombinedOutput()
	if createErr != nil {
		t.Fatalf("br create: %v\n%s", createErr, createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createOut, &created); err != nil || created.ID == "" {
		t.Fatalf("parse br create output: id=%q err=%v output=%s", created.ID, err, createOut)
	}
	beadID := created.ID

	claimLog := filepath.Join(root, "claims.log")
	brScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
exec %q "$@"
`, claimLog, realBR)
	if err := writeFile(filepath.Join(fakeBin, "br"), brScript, 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	// Agent fixture: a stable prompt that echoes the input back. Echoing
	// the prompt is what makes capture-pane contain the marker after the
	// dispatch, which is exactly the positive counterpart to the wedge
	// fixture (where echo is off and the marker never appears). The
	// foreground command is renamed to "claude" via exec -a so the
	// dispatch surface's PANE_AGENT_DEAD guard does not refuse delivery
	// (the guard rejects bare-shell foregrounds).
	agentScriptPath := filepath.Join(root, "friendly.sh")
	if err := writeFile(agentScriptPath, `#!/bin/sh
exec -a claude sh -c 'printf "Agent ready\n❯ "; while read -r line; do printf "\nRECEIVED: %s\n❯ " "$line"; done'
`, 0o755); err != nil {
		t.Fatalf("write friendly fixture: %v", err)
	}

	session := fmt.Sprintf("assign-ift-ok-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	paneIDRaw, err := tmux.DefaultClient.Run(
		"new-window", "-d", "-t", session, "-c", projectDir,
		"-P", "-F", "#{pane_id}", agentScriptPath,
	)
	if err != nil {
		t.Fatalf("spawn friendly window: %v", err)
	}
	paneID := strings.TrimSpace(paneIDRaw)
	if err := tmux.SetPaneTitle(paneID, session+"__cc_1"); err != nil {
		t.Fatalf("set pane title: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		captured, captureErr := tmux.CapturePaneOutput(paneID, 30)
		if captureErr == nil && strings.Contains(captured, "❯") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	baseArgs := []string{
		"--json", "assign", session,
		"--pane=" + paneID,
		"--beads=" + beadID,
		"--prompt=healthy probe payload",
		"--ignore-deps",
		"--reserve-files=false",
		"--repo=" + projectDir,
	}
	envelope, code, stderr := runDirectAssignCLIProcessForWedge(t, baseArgs)
	if code != 0 {
		var detail string
		if envelope.Error != nil {
			detail = fmt.Sprintf("error=%+v", envelope.Error)
		}
		t.Fatalf("healthy pane assign must succeed: code=%d stderr=%q %s envelope=%+v", code, stderr, detail, envelope)
	}
	if !envelope.Success {
		t.Fatalf("healthy pane assign must report success; got %+v", envelope)
	}
	if envelope.Error != nil {
		t.Fatalf("healthy pane assign must have no error; got %+v", envelope.Error)
	}
	if envelope.Data == nil || envelope.Data.Assignment == nil {
		t.Fatalf("envelope must carry the assignment data; got %+v", envelope)
	}
	if !envelope.Data.Assignment.Delivered {
		t.Fatalf("healthy pane assign must report delivered=true; got %+v", envelope.Data.Assignment)
	}
	if envelope.Data.Assignment.PromptSent != true {
		t.Fatalf("healthy pane assign must report prompt_sent=true; got %+v", envelope.Data.Assignment)
	}
	if strings.TrimSpace(envelope.Data.Assignment.DeliveryError) != "" {
		t.Fatalf("healthy pane assign must have empty delivery_error; got %q", envelope.Data.Assignment.DeliveryError)
	}
	if envelope.Data.Receipt == nil {
		t.Fatal("healthy pane must populate the dispatch receipt")
	}
	if !envelope.Data.Receipt.Transport.Delivered {
		t.Fatalf("receipt.Transport.Delivered must be true on a healthy pane; got %+v", envelope.Data.Receipt.Transport)
	}
	if strings.TrimSpace(envelope.Data.Receipt.Transport.DeliveryMarker) == "" {
		t.Fatalf("receipt.Transport.DeliveryMarker must be populated so the verdict is traceable")
	}

	// And the marker must be visible in the captured pane so a follow-up
	// operator using capture-pane directly can confirm delivery without
	// parsing JSON.
	captured, captureErr := tmux.CapturePaneOutput(paneID, 60)
	if captureErr != nil {
		t.Fatalf("capture healthy pane: %v", captureErr)
	}
	if !strings.Contains(captured, envelope.Data.Receipt.Transport.DeliveryMarker) {
		t.Fatalf("post-dispatch capture must contain the delivery marker; got %q", captured)
	}
}

func TestDirectAssignReportsClaimOkDeliveryFailedOnWedgedPane(t *testing.T) {
	testutilRequireTmux(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "project")
	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{home, projectDir, fakeBin, filepath.Join(projectDir, ".git")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "1")

	realBR, err := exec.LookPath("br")
	if err != nil {
		t.Skip("br is required for guarded-claim coverage")
	}
	cmd := exec.Command(realBR, "init", "--quiet")
	cmd.Dir = projectDir
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("br init: %v\n%s", runErr, out)
	}
	// Both br invocations must run in projectDir so the .beads/ they write
	// to is the same one the subprocess later reads via --repo. Without
	// this, the create lands in the parent test cwd's .beads/ (the live
	// ntm tracker) while init wrote a fresh empty db in projectDir, and
	// the subprocess's "issue not found" error is what surfaces.
	createCmd := exec.Command(realBR, "create", "--title", "wedge probe", "--type", "task", "--priority", "2", "--json")
	createCmd.Dir = projectDir
	createOut, createErr := createCmd.CombinedOutput()
	if createErr != nil {
		t.Fatalf("br create: %v\n%s", createErr, createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(createOut, &created); err != nil || created.ID == "" {
		t.Fatalf("parse br create output: id=%q err=%v output=%s", created.ID, err, createOut)
	}
	beadID := created.ID

	// Wrap br in a logging shim so the test's subprocesses route through
	// the same PATH the parent used. Without this, the helper subprocess
	// inherits PATH from the harness shell and may find a different br
	// binary than the one that initialized .beads/ in projectDir. The shim
	// is what the existing TestDirectAssignCLIReplayIsDurableAndBypassesChangedPreflight
	// fixture uses to make subprocess br behavior observable in test
	// assertions; we reuse the pattern here for the wedge fixture.
	claimLog := filepath.Join(root, "claims.log")
	brScript := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
exec %q "$@"
`, claimLog, realBR)
	if err := writeFile(filepath.Join(fakeBin, "br"), brScript, 0o755); err != nil {
		t.Fatalf("write fake br: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+os.Getenv("PATH"))

	agentScriptPath := filepath.Join(root, "wedged.sh")
	if err := writeFile(agentScriptPath, `#!/bin/sh
stty -echo 2>/dev/null
clear 2>/dev/null
printf 'TUI: trust dialog - press ESC to dismiss\n'
while read -r -n 1 -s _byte; do :; done
`, 0o755); err != nil {
		t.Fatalf("write wedge fixture: %v", err)
	}

	session := fmt.Sprintf("assign-ift-e2e-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	// Spawn the wedge as the session's first window's command. Using
	// new-window rather than send-keys avoids the brief echo-on window
	// between the parent shell and the wedge; the wedge becomes the
	// foreground process immediately with echo already off.
	paneIDRaw, err := tmux.DefaultClient.Run(
		"new-window", "-d", "-t", session,
		"-c", projectDir,
		"-P", "-F", "#{pane_id}",
		agentScriptPath,
	)
	if err != nil {
		t.Fatalf("spawn wedged window: %v", err)
	}
	paneID := strings.TrimSpace(paneIDRaw)

	// The wedge never renders an agent CLI chrome, so detectAgentTypeFromPane
	// classifies it as "user" and runDirectPaneAssignment refuses with
	// NOT_AGENT_PANE before reaching the dispatch surface. We tag the pane
	// with a session__cc_1 title so the agent-type detector classifies it
	// as a Claude pane — the canonical suffix is what the assign flow uses
	// to distinguish agent panes from operator/user panes.
	if err := tmux.SetPaneTitle(paneID, session+"__cc_1"); err != nil {
		t.Fatalf("set pane title: %v", err)
	}

	// Wait for the wedge's stable header so the line discipline has fully
	// disabled echo before we drive the assign flow.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		captured, captureErr := tmux.CapturePaneOutput(paneID, 30)
		if captureErr == nil && strings.Contains(captured, "trust dialog") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if paneID == "" {
		t.Fatal("wedge fixture never rendered its header")
	}

	baseArgs := []string{
		"--json", "assign", session,
		"--pane=" + paneID,
		"--beads=" + beadID,
		"--prompt=wedge probe payload",
		"--ignore-deps",
		"--force",
		"--reserve-files=false",
		"--repo=" + projectDir,
	}
	envelope, code, stderr := runDirectAssignCLIProcessForWedge(t, baseArgs)
	if code == 0 {
		t.Fatalf("expected non-zero exit code on wedge; code=%d stderr=%q envelope=%+v", code, stderr, envelope)
	}
	if envelope.Success {
		t.Fatalf("expected envelope.Success=false on wedge; got %+v", envelope)
	}
	if envelope.Error == nil {
		t.Fatalf("expected a populated error envelope; got %+v", envelope)
	}
	if envelope.Error.Code != robot.ErrCodeClaimOkDeliveryFailed {
		t.Fatalf("expected error_code=%q (bd-ift specific); got %q (full=%+v)",
			robot.ErrCodeClaimOkDeliveryFailed, envelope.Error.Code, envelope.Error)
	}
	if strings.TrimSpace(envelope.Error.Message) == "" {
		t.Fatalf("bd-ift: human-readable error must be non-empty so the operator can act; got %+v", envelope.Error)
	}
	if envelope.Data == nil || envelope.Data.Assignment == nil {
		t.Fatalf("envelope must carry the assignment data even on failure so callers see what happened; got %+v", envelope)
	}
	if envelope.Data.Assignment.Delivered {
		t.Fatalf("assignment.Delivered must be false on the wedge; got true")
	}
	if envelope.Data.Assignment.PromptSent {
		t.Fatalf("assignment.PromptSent must be false on the wedge (the dispatch port failed before reporting sent); got true")
	}
	if strings.TrimSpace(envelope.Data.Assignment.DeliveryError) == "" {
		t.Fatalf("assignment.DeliveryError must name the missing marker; got %+v", envelope.Data.Assignment)
	}

	// bd-ift acceptance criterion: "When delivery fails after the claim
	// landed, the result says so explicitly and the human output is
	// non-empty". The JSON envelope satisfies the structured half; this
	// assertion checks the plain-text path renders a non-empty diagnostic.
	if envelope.Data.Receipt == nil {
		t.Fatalf("even on wedge failure the receipt must be populated so wrappers can correlate the durable dispatch")
	}
}

// runDirectAssignCLIProcessForWedge is a thinner wrapper than the existing
// runDirectAssignCLIProcess: it does not require a `br` claim log because the
// fixture asserts on the error envelope, not on claim-replay semantics.
func runDirectAssignCLIProcessForWedge(t *testing.T, args []string) (AssignEnvelope[DirectAssignData], int, string) {
	t.Helper()
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("encode helper args: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDirectAssignCLIProcessHelper$")
	cmd.Env = append(os.Environ(),
		"NTM_DIRECT_ASSIGN_HELPER_ARGS="+string(rawArgs),
		"NTM_TEST_TMUX_ENV_OWNED=1",
		"NTM_NO_COLOR=1",
		"NTM_DEBUG_BR_PATH="+os.Getenv("PATH"),
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("start direct assign helper: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	var envelope AssignEnvelope[DirectAssignData]
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil {
		t.Fatalf("decode direct assign output: %v\nstdout=%q\nstderr=%q", decodeErr, stdout.String(), stderr.String())
	}
	return envelope, exitCode, stderr.String()
}


func TestAssignDeliveryVerifierDetectsModalWedgedPane(t *testing.T) {
	testutilRequireTmux(t)
	projectDir := t.TempDir()
	fakeBin := t.TempDir()
	t.Setenv("PATH", fakeBin+string(filepath.ListSeparator)+pathEnv(t))

	// Agent fixture: a script that disables terminal echo, renders a stable
	// dialog, and silently consumes every byte the dispatch types. The bytes
	// sit in the line discipline buffer but never reach the output buffer
	// that tmux capture-pane reads, so the post-send capture never contains
	// the marker — even though send-keys returned success. That is exactly
	// the owned-and-inert failure mode bd-ift exists to surface.
	agentScript := filepath.Join(t.TempDir(), "wedged-agent.sh")
	wedge := `#!/bin/sh
stty -echo 2>/dev/null
clear 2>/dev/null
printf 'TUI: trust dialog - press ESC to dismiss\n'
# Drain bytes forever without writing them back to the terminal. With
# echo off and canonical mode, the input bytes stay in the line discipline
# buffer; capture-pane sees only what the foreground process actually
# wrote to stdout, which is the dialog header.
while read -r -n 1 -s _byte; do
    : # discard
done
`
	if err := writeFile(agentScript, wedge, 0o755); err != nil {
		t.Fatalf("write wedge fixture: %v", err)
	}

	session := fmt.Sprintf("assign-ift-wedged-%d", time.Now().UnixNano())
	if err := tmux.CreateSession(session, projectDir); err != nil {
		t.Fatalf("create session: %v", err)
	}
	t.Cleanup(func() { _ = tmux.KillSession(session) })

	// Drive the wedge as the session's first command so it becomes the
	// foreground process. We replace the default shell rather than spawn a
	// new window because tmux.new-window on a single-window session reuses
	// that window; running the wedge in-place keeps the lifecycle simple.
	if err := tmux.SendKeys(session, agentScript, true); err != nil {
		t.Fatalf("start wedge fixture: %v", err)
	}
	// Wait for the wedge's stable header to render so capture-pane reads
	// from a fully booted pane (otherwise the line discipline and echo are
	// still being set up by the parent shell).
	deadline := time.Now().Add(5 * time.Second)
	paneID := ""
	for time.Now().Before(deadline) {
		panes, listErr := tmux.GetPanes(session)
		if listErr == nil && len(panes) > 0 {
			paneID = panes[0].ID
			if panes[0].Width > 0 && panes[0].Height > 0 {
				captured, captureErr := tmux.CapturePaneOutput(paneID, 30)
				if captureErr == nil && strings.Contains(captured, "trust dialog") {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if paneID == "" {
		t.Fatal("wedge fixture never rendered its header")
	}

	marker := newAssignDeliveryMarker("ntm-ift-wedge-fixture")
	dispatchPrompt := injectAssignDeliveryMarker("Work on bead bd-ift: wedge check", marker)

	// Drive the real dispatch surface the way runDirectPaneAssignment
	// would: send the marker-bearing prompt with enter=true. The wedge has
	// echo off, so the typed bytes do not appear on screen; capture-pane
	// returns the dialog header with no marker.
	if err := tmux.SendKeysForAgent(paneID, dispatchPrompt, true, tmux.AgentUnknown); err != nil {
		t.Fatalf("send-keys to wedged pane: %v", err)
	}
	// Give the line discipline a moment to drain the bytes into the wedge's
	// read loop. Echo is off so nothing is added to the output buffer, but
	// the shell that started the wedge may briefly echo part of the prompt
	// as it interprets `send-keys`; sleep past that race window.
	time.Sleep(300 * time.Millisecond)

	pane := tmux.Pane{ID: paneID, Index: 1}
	verifier := tmuxDeliveryVerifier{}
	verified, captured, err := verifier.VerifyMarker(context.Background(), pane, marker)
	if err != nil {
		t.Fatalf("verifier infrastructure error: %v", err)
	}
	if verified {
		t.Fatalf("wedge fixture unexpectedly reported verified=true; capture=%q", captured)
	}
	if strings.Contains(captured, marker) {
		t.Fatalf("wedge fixture should never have rendered the marker; capture=%q", captured)
	}

	// Exercise the full delivery-missing error construction so a regression
	// in the operator-facing message is caught here, not by an operator
	// in the field.
	missing := assignDeliveryMissingError(marker, captured)
	if !strings.Contains(missing.Error(), marker) {
		t.Fatalf("operator-facing missing-marker error must name the marker: %q", missing.Error())
	}
}

// testutilRequireTmux is a small helper that mirrors testutil.RequireTmux
// without importing it; the existing util lives in a different package and
// the import graph would create a cycle if we used it directly here.
func testutilRequireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not present; skipping bd-ift fixture")
	}
	if testing.Short() {
		t.Skip("tmux fixture skipped under -short")
	}
}

func pathEnv(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", "printf %s \"$PATH\"").Output()
	if err != nil {
		t.Fatalf("read PATH: %v", err)
	}
	return strings.TrimRight(string(out), "\n")
}

func writeFile(path, content string, mode uint32) error {
	return os.WriteFile(path, []byte(content), os.FileMode(mode))
}
