package cli

// assign_delivery_verify.go — bd-ift: per-pane post-send delivery verification.
//
// Assign is two operations: claim the bead (a tracker record) and deliver the
// brief (typed text into a tmux pane). The claim can land while the brief
// never reaches the agent — a modal-wedged pane swallows pasted input while
// send-keys returns success, and the dispatch surface reports ReceiptDelivered.
// The result is "owned and inert": the bead shows in-flight, no agent is
// working it, and re-dispatch silently re-claims nothing because the claim
// already exists. bd-ift fixes this by adding a per-assignment delivered
// verdict derived from a post-send capture that must contain a delivery
// marker the assign layer injected into the prompt.
//
// Scope: the verification happens inside the cliAtomicPaneDispatchPort, so
// every entry point that uses it (--pane direct, batched executeAssignments,
// watch mode reassignment) gets the verdict. The dispatch service itself is
// untouched: ReceiptDelivered still means "send-keys returned no error", and
// the marker check is a CLI-side read-back against the post-dispatch pane
// capture. We do not push the marker into the dispatch package because the
// SEND surface is shared with send/replay/profile_switch/bugs_watch, none of
// which need this signal.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// assignDeliveryMarkerPrefix is the well-known prefix for the per-assignment
// delivery marker. The marker is a short, stable token appended to the prompt
// at dispatch time so the post-send pane capture can confirm our specific
// message reached the pane — distinct from any other pane's output and from
// other agents' responses.
const assignDeliveryMarkerPrefix = "[ntm-assign:"

// assignDeliveryMarkerSuffix closes the marker so a substring match cannot
// accidentally hit an agent's reply to a previous task.
const assignDeliveryMarkerSuffix = "]"

// assignDeliveryMarkerLength controls how many hex chars of the idempotency
// key hash ride along. 8 hex chars (32 bits) is enough to keep collision risk
// below the prompt-collision floor for any single session; the full key is
// stored separately on the receipt for forensics.
const assignDeliveryMarkerLength = 8

// newAssignDeliveryMarker derives a per-attempt marker from the idempotency
// key. The marker is short enough to fit on one composer line and unique
// enough that a substring match is unambiguous.
func newAssignDeliveryMarker(idempotencyKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	return fmt.Sprintf("%s%s%s", assignDeliveryMarkerPrefix, hex.EncodeToString(sum[:])[:assignDeliveryMarkerLength], assignDeliveryMarkerSuffix)
}

// injectAssignDeliveryMarker appends the delivery marker on its own trailing
// line if and only if the prompt does not already carry one. The marker
// survives dispatch redaction because the redact port is content-aware only
// for credential findings; a bracketed hex string is not flagged. The
// trailing newline-before-marker keeps it on its own composer row, so any
// visual wrap in the agent's composer does not split the token.
func injectAssignDeliveryMarker(prompt, marker string) string {
	if strings.TrimSpace(marker) == "" || strings.Contains(prompt, marker) {
		return prompt
	}
	if prompt == "" {
		return marker
	}
	return prompt + "\n" + marker
}

// deliveryVerifier is the read-back half of assign's per-pane delivery check.
// It captures the pane right after dispatch and reports whether the marker we
// injected is in the visible text. The interface exists so unit tests can
// substitute a recording capture without driving a real tmux session.
type deliveryVerifier interface {
	VerifyMarker(ctx context.Context, pane tmux.Pane, marker string) (verified bool, capturedText string, err error)
}

// tmuxCaptureRunner abstracts tmux.CapturePaneOutput so tests can inject a
// recorder without depending on a live tmux session.
type tmuxCaptureRunner interface {
	CapturePaneOutput(target string, lines int) (string, error)
}

// tmuxDefaultCaptureRunner is the production tmux capture call.
type tmuxDefaultCaptureRunner struct{}

func (tmuxDefaultCaptureRunner) CapturePaneOutput(target string, lines int) (string, error) {
	return tmux.CapturePaneOutput(target, lines)
}

// defaultDeliveryLines bounds how many trailing rows we scan. A typical
// composer is well under 100 visible rows; the marker sits on the last row of
// the typed message, but the agent may have already rendered one or two
// follow-on lines (the "Working…" prompt, etc.) before we get to capture.
const defaultDeliveryLines = 200

// tmuxDeliveryVerifier captures the pane through the project's tmux client.
type tmuxDeliveryVerifier struct {
	lines  int
	runner tmuxCaptureRunner
}

// VerifyMarker captures the pane and reports whether the marker substring is
// in the visible text. A capture failure is non-fatal: the verifier returns
// the error so the caller can decide between treating it as "delivered
// unconfirmed" and propagating it as an infrastructure failure.
func (v tmuxDeliveryVerifier) VerifyMarker(ctx context.Context, pane tmux.Pane, marker string) (bool, string, error) {
	if strings.TrimSpace(marker) == "" {
		return false, "", errors.New("delivery verification requires a non-empty marker")
	}
	lines := v.lines
	if lines <= 0 {
		lines = defaultDeliveryLines
	}
	runner := v.runner
	if runner == nil {
		runner = tmuxDefaultCaptureRunner{}
	}
	target := pane.ID
	if strings.TrimSpace(target) == "" {
		return false, "", fmt.Errorf("pane has no tmux identity for delivery verification (pane=%d)", pane.Index)
	}
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	captured, err := runner.CapturePaneOutput(target, lines)
	if err != nil {
		return false, "", fmt.Errorf("capture pane %s for delivery verification: %w", target, err)
	}
	verified := strings.Contains(captured, marker)
	return verified, captured, nil
}

// verifyAssignDelivery is the read-back used by cliAtomicPaneDispatchPort. It
// returns (verified, capturedText, err). The contract:
//   - verified=true: the capture contained the marker. Callers must treat
//     delivery as confirmed and surface Delivered=true on the receipt.
//   - verified=false with non-nil err: capture failed. Callers should surface
//     the err so an operator sees an infrastructure failure distinct from a
//     silent "marker not in pane".
//   - verified=false with nil err: capture succeeded, marker absent. Callers
//     surface Delivered=false with the marker named in the error so the
//     distinction between "lost keystrokes" and "system error" is preserved.
func verifyAssignDelivery(ctx context.Context, verifier deliveryVerifier, pane tmux.Pane, marker string) (bool, string, error) {
	if verifier == nil {
		verifier = tmuxDeliveryVerifier{}
	}
	return verifier.VerifyMarker(ctx, pane, marker)
}

// assignDeliveryMissingError is the canonical error returned when the
// post-send capture did not contain the marker. The call site maps this to
// robot.ErrCodeClaimOkDeliveryFailed, distinct from any dispatch transport
// error code.
func assignDeliveryMissingError(marker, captured string) error {
	if captured != "" {
		return fmt.Errorf("assign delivery unconfirmed: marker %s not found in post-send pane capture (length=%d)", marker, len(captured))
	}
	return fmt.Errorf("assign delivery unconfirmed: marker %s not found in post-send pane capture", marker)
}

// assignSkipDeliveryVerify is the package-level flag controlling whether the
// post-send capture check runs. The flag lives in this file because it is
// only meaningful in conjunction with the verifier; tests reset it.
var assignSkipDeliveryVerify bool

// assignDeliveryVerificationEnabled reports whether the bd-ift delivery
// verification should run for this command. Default on; tests and the
// --skip-delivery-verify escape hatch can disable it.
//
// We deliberately bind the read-back to a single package-level boolean rather
// than threading the option through every caller (--pane direct,
// executeAssignmentsEnhanced, watch loop, reassignment, retry). The check is
// a strict superset of "send-keys returned no error" — turning it off is only
// ever a debugging or recovery move, and a single toggle makes that easy.
func assignDeliveryVerificationEnabled() bool {
	return !assignSkipDeliveryVerify
}
