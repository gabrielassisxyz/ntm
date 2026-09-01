package swarm

// delivery_readback.go — bd-ljd: swarm spawn confirms per pane that the
// marching orders arrived.
//
// Spawn delivery had no read-back: send-keys returns success once the bytes
// enter the pane's pty, but a pane whose input is blocked (a modal-wedged
// agent, a pane still booting) swallows the marching orders while the injector
// reports success. The o62l shakedown lost a pane this way — it sat for an
// hour showing only the pi banner at 0.0% context, and nothing noticed. This
// file adds the delivery half of the fix (bd-q2a owns the readiness half):
// after the batch delivers, each pane is read back and the capture is checked
// for evidence the prompt landed. Two independent signals count as evidence:
//
//   - orders_text  — a tail fragment of the marching orders is visible in the
//     pane (composer echo or transcript echo of the typed input)
//   - context_meter — pi's bottom status line shows a context percentage above
//     zero; a pane that never consumed a prompt reads 0% of its window
//
// A pane with no evidence is re-sent exactly once and then reported loudly,
// naming the pane. A pane whose capture shows the orders landed is never
// re-sent: a duplicate prompt to a working agent is its own damage, so
// evidence short-circuits before any retry. The delivery mechanism itself
// (sendToPane) and the marching-orders content are unchanged; the read-back is
// layered on top, and only on the swarm spawn path (InjectSwarmWithContext).

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/status"
)

// Evidence-signal names recorded on InjectionResult.DeliverySignal so a
// confirmed delivery says which signal confirmed it.
const (
	swarmDeliverySignalOrdersText   = "orders_text"
	swarmDeliverySignalContextMeter = "context_meter"
)

// swarmDeliveryEvidenceWindowWords is how many consecutive prompt words each
// candidate fragment spans. TUI wrapping inserts line breaks into the echoed
// text; a fragment short enough to sit inside one rendered row survives
// whitespace-normalized comparison whenever the break lands on a space. A
// handful of overlapping tail windows keeps one unlucky mid-word wrap from
// hiding evidence that is actually there.
const swarmDeliveryEvidenceWindowWords = 6

// swarmDeliveryEvidenceMaxWindows bounds how many tail windows are tried. The
// tail is where the echo lives: the composer holds the most recently typed
// words before Enter, and the transcript renders the message with its end
// closest to the bottom of the pane.
const swarmDeliveryEvidenceMaxWindows = 3

// swarmDeliveryCaptureLines is the scrollback budget for a read-back capture.
// The echo of the marching orders may already sit above the live spinner once
// the agent starts streaming a response, so the check reads deeper than the
// 20-line status budget; 200 mirrors the bd-ift verifier's read depth.
const swarmDeliveryCaptureLines = 200

// piContextMeterRe extracts the context-usage percentage from pi's bottom
// status line, e.g. "↑100k ↓958 13.0%/262k (auto)". The number left of the
// slash is the share of the context window in use. A pane that never received
// anything reads 0.0% — the exact state the o62l incident pane sat in for an
// hour. The window size carries pi's own unit (k or M), which the regex
// deliberately does not pin down.
var piContextMeterRe = regexp.MustCompile(`(\d+(?:\.\d+)?)%/\d`)

// normalizeSwarmCaptureText folds a capture and a prompt into comparable
// form: ANSI stripped, every whitespace run collapsed to one space. Line
// wraps in the echo become single spaces, matching the prompt's own spaces.
func normalizeSwarmCaptureText(s string) string {
	return strings.Join(strings.Fields(status.StripANSI(s)), " ")
}

// ordersTextVisibleInCapture reports whether a tail fragment of the prompt is
// visible in the capture. The whole normalized prompt is tried first; a
// prompt longer than one rendered row falls back to overlapping tail windows
// of swarmDeliveryEvidenceWindowWords words.
func ordersTextVisibleInCapture(capture, prompt string) bool {
	normalizedCapture := normalizeSwarmCaptureText(capture)
	if normalizedCapture == "" {
		return false
	}
	normalizedPrompt := normalizeSwarmCaptureText(prompt)
	if normalizedPrompt == "" {
		return false
	}
	if strings.Contains(normalizedCapture, normalizedPrompt) {
		return true
	}
	words := strings.Fields(normalizedPrompt)
	if len(words) == 0 {
		return false
	}
	window := swarmDeliveryEvidenceWindowWords
	if window > len(words) {
		window = len(words)
	}
	for tried := 0; tried < swarmDeliveryEvidenceMaxWindows; tried++ {
		start := len(words) - window*(tried+1)
		if start < 0 {
			break
		}
		fragment := strings.Join(words[start:start+window], " ")
		if strings.Contains(normalizedCapture, fragment) {
			return true
		}
		if start == 0 {
			break
		}
	}
	return false
}

// piContextMeterMoved reports whether the capture shows pi's context meter
// above zero. A capture with no parseable meter (a non-pi pane, or a pi pane
// still booting) is not evidence either way and returns false.
func piContextMeterMoved(capture string) bool {
	for _, match := range piContextMeterRe.FindAllStringSubmatch(status.StripANSI(capture), -1) {
		pct, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		if pct > 0 {
			return true
		}
	}
	return false
}

// swarmDeliveryEvidence checks one capture for evidence the marching orders
// landed in the pane. Orders text is checked first because it is direct
// evidence for every agent type; the context-meter signal is pi-specific
// because pi's meter format is the one whose pristine state is known here.
func swarmDeliveryEvidence(capture, prompt, agentType string) (bool, string) {
	if normalizeSwarmCaptureText(prompt) == "" {
		// No orders, nothing to land: even a moved meter is evidence of
		// somebody else's work, not of this prompt arriving.
		return false, ""
	}
	if ordersTextVisibleInCapture(capture, prompt) {
		return true, swarmDeliverySignalOrdersText
	}
	if agent.AgentType(agentType).Canonical() == agent.AgentTypePi && piContextMeterMoved(capture) {
		return true, swarmDeliverySignalContextMeter
	}
	return false, ""
}

// deliverySettleDelay is how long the read-back waits after a send before its
// first capture, letting the agent render the typed prompt.
func (p *PromptInjector) deliverySettleDelay() time.Duration {
	if p.DeliverySettleDelay > 0 {
		return p.DeliverySettleDelay
	}
	return 500 * time.Millisecond
}

// deliveryPollInterval is the spacing between read-back captures while
// hunting for evidence inside one confirm window.
func (p *PromptInjector) deliveryPollInterval() time.Duration {
	if p.DeliveryPollInterval > 0 {
		return p.DeliveryPollInterval
	}
	return 250 * time.Millisecond
}

// deliveryConfirmWindow is how long one confirm pass polls for evidence
// before the pane is declared unconfirmed for that pass.
func (p *PromptInjector) deliveryConfirmWindow() time.Duration {
	if p.DeliveryConfirmWindow > 0 {
		return p.DeliveryConfirmWindow
	}
	return 3 * time.Second
}

// confirmSwarmDeliveries runs the bd-ljd read-back over a finished batch:
// every pane whose send reported success is confirmed by capture, re-sent
// once when unconfirmable, and reported loudly when still unconfirmable.
// Results with a failed send are left alone — the send error is already the
// report for those.
func (p *PromptInjector) confirmSwarmDeliveries(ctx context.Context, prompt string, result *BatchInjectionResult) {
	if result == nil || strings.TrimSpace(prompt) == "" {
		return
	}
	if ctx.Err() != nil {
		for i := range result.Results {
			res := &result.Results[i]
			if !res.Success {
				continue
			}
			res.DeliveryError = fmt.Sprintf("delivery read-back skipped: %v", ctx.Err())
			result.Unconfirmed++
		}
		return
	}
	for i := range result.Results {
		res := &result.Results[i]
		if !res.Success {
			continue
		}
		p.confirmSwarmDelivery(ctx, prompt, res)
		if res.DeliveryError != "" {
			result.Unconfirmed++
		}
	}
}

// confirmSwarmDelivery confirms one pane. Evidence on the first pass ends the
// check with no re-send — a pane that received its orders is never re-sent
// them (bd-ljd acceptance criterion: a duplicate prompt to a working agent is
// its own damage). No evidence means exactly one retry, then a loud report.
func (p *PromptInjector) confirmSwarmDelivery(ctx context.Context, prompt string, res *InjectionResult) {
	if found, signal := p.awaitDeliveryEvidence(ctx, res.SessionPane, res.AgentType, prompt); found {
		res.DeliveryConfirmed = true
		res.DeliverySignal = signal
		return
	}

	res.DeliveryRetried = true
	p.logger().Warn("[PromptInjector] delivery_unconfirmed_retrying",
		"session_pane", res.SessionPane,
		"agent_type", res.AgentType)
	if err := p.InjectPrompt(res.SessionPane, res.AgentType, prompt); err != nil {
		res.DeliveryError = fmt.Sprintf("marching orders unconfirmed and retry send failed for pane %s: %v", res.SessionPane, err)
		p.logger().Error("[PromptInjector] delivery_retry_send_failed",
			"session_pane", res.SessionPane,
			"agent_type", res.AgentType,
			"error", err)
		return
	}

	if found, signal := p.awaitDeliveryEvidence(ctx, res.SessionPane, res.AgentType, prompt); found {
		res.DeliveryConfirmed = true
		res.DeliverySignal = signal
		return
	}

	res.DeliveryError = fmt.Sprintf("marching orders unconfirmed for pane %s after delivery and one retry: no orders text and no context-meter movement in capture", res.SessionPane)
	p.logger().Error("[PromptInjector] delivery_unconfirmed",
		"session_pane", res.SessionPane,
		"agent_type", res.AgentType,
		"detail", res.DeliveryError)
}

// awaitDeliveryEvidence polls the pane's capture for evidence that the prompt
// landed, until the confirm window closes or the context is cancelled. A
// capture error is not evidence either way; the pass simply keeps polling and
// the caller decides on the retry.
func (p *PromptInjector) awaitDeliveryEvidence(ctx context.Context, sessionPane, agentType, prompt string) (bool, string) {
	settle := p.deliverySettleDelay()
	if settle > 0 {
		select {
		case <-ctx.Done():
			return false, ""
		case <-time.After(settle):
		}
	}
	interval := p.deliveryPollInterval()
	deadline := time.Now().Add(p.deliveryConfirmWindow())
	for {
		capture, err := p.captureForReadback(ctx, sessionPane)
		if err == nil {
			if found, signal := swarmDeliveryEvidence(capture, prompt, agentType); found {
				return true, signal
			}
		} else {
			p.logger().Debug("[PromptInjector] readback_capture_failed",
				"session_pane", sessionPane,
				"error", err)
		}
		if !time.Now().Add(interval).Before(deadline) {
			return false, ""
		}
		select {
		case <-ctx.Done():
			return false, ""
		case <-time.After(interval):
		}
	}
}

// captureForReadback captures the pane's trailing text through the configured
// tmux client.
func (p *PromptInjector) captureForReadback(ctx context.Context, target string) (string, error) {
	return p.tmuxClient().CapturePaneOutputContext(ctx, target, swarmDeliveryCaptureLines)
}

// UnconfirmedPanes names the panes whose marching orders could not be
// confirmed, for operator-facing reports.
func (b *BatchInjectionResult) UnconfirmedPanes() []string {
	if b == nil {
		return nil
	}
	panes := make([]string, 0, b.Unconfirmed)
	for _, res := range b.Results {
		if res.DeliveryError != "" {
			panes = append(panes, res.SessionPane)
		}
	}
	return panes
}
