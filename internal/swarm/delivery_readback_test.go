package swarm

// delivery_readback_test.go — bd-ljd unit coverage for the post-delivery
// read-back. The evidence check runs against captures recorded from real
// panes: the pi fixtures mirror the ones read off a live pi 0.84.2 pane for
// internal/cli/spawn_state_test.go (bd-q2a), and the echo fixtures follow the
// shell-fixture transcript shape proven by bd-ift's friendly agent fixture.
// The read-back loop itself is exercised against a scripted tmux client, so
// the retry budget and the never-re-send guarantee hold without any tmux
// server; the real-tmux integration coverage lives in
// delivery_readback_integration_test.go.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Recorded pi captures (pi 0.84.2), mirroring the fixtures in
// internal/cli/spawn_state_test.go. The pristine capture is the state the
// o62l incident pane sat in for an hour: banner, no prompt, 0.0% context.
const bdLjdPiPristineCapture = ` pi v0.84.2
 escape interrupt · ctrl+c/ctrl+d clear/exit · / commands · ! bash · ctrl+o more
 Press ctrl+o to show full startup help and loaded resources.

────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────
/home/gabriel/repositories/daytrace
0.0%/262k (auto)                              (litellm) kimi-k2.7`

// The same pane mid-turn: spinner up and the context meter off zero.
const bdLjdPiMeterMovedCapture = `with its own shell process, working directory, and scrollback history. You can
split a window horizontally or vertically to create panes.

 ⠴ Working...
────────────────────────────────────────────────────────────────

────────────────────────────────────────────────────────────────
/home/gabriel/repositories/daytrace
↑100k ↓958 13.0%/262k (auto)                  (litellm) kimi-k2.7`

// TestOrdersTextVisibleInCaptureFromFixtures is the evidence half of the
// read-back against recorded pane shapes: a pristine pi banner never counts
// as evidence, an echo of the orders always does, and the context meter
// moving off zero does even when the orders text has scrolled.
func TestSwarmDeliveryEvidenceFromFixtures(t *testing.T) {
	orders := DefaultMarchingOrders

	cases := []struct {
		name       string
		capture    string
		prompt     string
		agentType  string
		wantFound  bool
		wantSignal string
	}{
		{
			name:      "pristine pi banner at 0.0% is not evidence",
			capture:   bdLjdPiPristineCapture,
			prompt:    orders,
			agentType: "pi",
		},
		{
			name:       "pi context meter off zero is evidence",
			capture:    bdLjdPiMeterMovedCapture,
			prompt:     orders,
			agentType:  "pi",
			wantFound:  true,
			wantSignal: swarmDeliverySignalContextMeter,
		},
		{
			name: "orders echo in a transcript is evidence for any agent",
			capture: strings.Join([]string{
				"Agent ready",
				"❯ " + strings.SplitN(orders, "\n", 2)[0],
				strings.SplitN(orders, "\n", 2)[1],
				"",
			}, "\n"),
			prompt:     orders,
			agentType:  "cc",
			wantFound:  true,
			wantSignal: swarmDeliverySignalOrdersText,
		},
		{
			name:      "unrelated pane output is not evidence",
			capture:   "some other agent said something unrelated entirely",
			prompt:    orders,
			agentType: "cc",
		},
		{
			name:      "empty prompt is never evidence",
			capture:   bdLjdPiMeterMovedCapture,
			prompt:    "   \n  ",
			agentType: "pi",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found, signal := swarmDeliveryEvidence(tc.capture, tc.prompt, tc.agentType)
			if found != tc.wantFound {
				t.Fatalf("swarmDeliveryEvidence found=%v, want %v", found, tc.wantFound)
			}
			if tc.wantFound && signal != tc.wantSignal {
				t.Fatalf("evidence signal = %q, want %q", signal, tc.wantSignal)
			}
		})
	}
}

// TestOrdersTextVisibleSurvivesWrappingAndANSI is the wrap/ANSI half of the
// evidence check: an agent TUI wraps the echoed prompt across rows and may
// decorate it, so the check must survive both. The fixture hard-wraps the
// normalized orders at a fixed width (mid-word breaks included — the worst
// case) and sprinkles ANSI color codes over it.
func TestOrdersTextVisibleSurvivesWrappingAndANSI(t *testing.T) {
	orders := DefaultMarchingOrders
	normalized := strings.Join(strings.Fields(orders), " ")

	// Hard-wrap at 40 columns, breaking mid-word when the column boundary
	// says so — deliberately harsher than any real TUI's word wrapping.
	var wrapped strings.Builder
	for i, r := range normalized {
		if i > 0 && i%40 == 0 {
			wrapped.WriteString("\n")
		}
		wrapped.WriteRune(r)
	}
	decorated := "\x1b[38;5;245m" + wrapped.String() + "\x1b[0m"

	if !ordersTextVisibleInCapture(decorated, orders) {
		t.Fatalf("orders echo wrapped at 40 columns with ANSI must still be found as evidence")
	}

	// The meter-moved fixture alone must not satisfy the orders-text signal:
	// the two signals are independent, and a transcript that never showed the
	// orders must not be claimed as orders_text.
	if ordersTextVisibleInCapture(bdLjdPiMeterMovedCapture, orders) {
		t.Fatalf("meter-moved capture must not satisfy the orders_text signal")
	}
}

// TestPiContextMeterMovedReadsPristineAsUnmoved pins the meter parse against
// the two window-unit shapes pi draws (k and M windows) and against captures
// with no meter at all.
func TestPiContextMeterMovedReadsPristineAsUnmoved(t *testing.T) {
	if piContextMeterMoved(bdLjdPiPristineCapture) {
		t.Fatalf("0.0%% meter must not count as movement")
	}
	if !piContextMeterMoved("↑100k ↓958 13.0%/262k (auto)") {
		t.Fatalf("13.0%% meter must count as movement")
	}
	if !piContextMeterMoved("0.4%/1.0M (auto)                              (litellm) deepseek-v4-pro-high-k3") {
		t.Fatalf("a million-token window reading above zero must count as movement")
	}
	if piContextMeterMoved("OpenAI Codex CLI v1.2.3\n47% context left · ? for shortcuts") {
		t.Fatalf("a codex context-remaining display is not pi's used-percentage meter")
	}
	if piContextMeterMoved("") {
		t.Fatalf("a capture with no meter must not count as movement")
	}
}

// scriptedReadbackClient is a fake promptInjectionTmuxClient for read-back
// tests. WaitForReady reads idleCapture (shaped so pi resolves to idle, which
// keeps the send path fast); the read-back reads successive captures.
type scriptedReadbackClient struct {
	mu sync.Mutex

	idleCaptures string
	readbacks    []string // consumed in order; the last repeats forever
	readbackErr  error

	sendForAgentCount int
	sendKeysCount     int
	sendErrOnCall     map[int]error
}

func (c *scriptedReadbackClient) CaptureForStatusDetectionContext(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.idleCaptures, nil
}

func (c *scriptedReadbackClient) CapturePaneOutputContext(context.Context, string, int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readbackErr != nil {
		return "", c.readbackErr
	}
	if len(c.readbacks) == 0 {
		return "", nil
	}
	if len(c.readbacks) == 1 {
		return c.readbacks[0], nil
	}
	// Pop one per call; the last one sticks once the list drains.
	next := c.readbacks[0]
	if len(c.readbacks) > 1 {
		c.readbacks = c.readbacks[1:]
	}
	return next, nil
}

func (c *scriptedReadbackClient) GetPanes(string) ([]tmux.Pane, error) {
	return nil, nil
}

func (c *scriptedReadbackClient) SendKeys(string, string, bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendKeysCount++
	return nil
}

func (c *scriptedReadbackClient) SendKeysForAgent(_ string, _ string, _ bool, _ tmux.AgentType) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sendForAgentCount++
	if err, ok := c.sendErrOnCall[c.sendForAgentCount]; ok {
		return err
	}
	return nil
}

func newReadbackTestInjector(client *scriptedReadbackClient) *PromptInjector {
	injector := NewPromptInjector()
	injector.tmuxClientOverride = client
	injector.DeliverySettleDelay = 5 * time.Millisecond
	injector.DeliveryPollInterval = 5 * time.Millisecond
	injector.DeliveryConfirmWindow = 60 * time.Millisecond
	injector.EnterDelay = time.Millisecond
	return injector
}

// bdLjdProbePrompt carries a token unique to each run so callers can count
// deliveries in fixture logs.
func bdLjdProbePrompt(token string) string {
	return "Work on bd-ljd probe " + token + " then report status"
}

func bdLjdReadbackPlan() *SwarmPlan {
	return &SwarmPlan{
		Sessions: []SessionSpec{{
			Name:      "bd-ljd-unit",
			AgentType: "pi",
			PaneCount: 1,
			Panes:     []PaneSpec{{Index: 1, AgentType: "pi"}},
		}},
	}
}

// TestConfirmSwarmDeliveryRetriesOnceThenReports drives the read-back against
// a pane whose captures never show evidence: exactly one re-send is issued
// (the batch's initial delivery is simulated by the pre-built result; the
// fake's send counter sees only the read-back's re-send) and the pane is
// reported unconfirmed with its name in the error.
func TestConfirmSwarmDeliveryRetriesOnceThenReports(t *testing.T) {
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{bdLjdPiPristineCapture},
	}
	injector := newReadbackTestInjector(client)

	result := &BatchInjectionResult{
		TotalPanes: 1,
		Results: []InjectionResult{{
			SessionPane: "bd-ljd-unit:1.0",
			AgentType:   "pi",
			Success:     true,
		}},
	}

	injector.confirmSwarmDeliveries(context.Background(), bdLjdProbePrompt("retry-once"), result)

	if client.sendForAgentCount != 1 {
		t.Fatalf("expected exactly one re-send, got %d", client.sendForAgentCount)
	}
	if result.Unconfirmed != 1 {
		t.Fatalf("Unconfirmed = %d, want 1", result.Unconfirmed)
	}
	res := result.Results[0]
	if res.DeliveryConfirmed {
		t.Fatalf("pane with no evidence must not be reported confirmed: %+v", res)
	}
	if !res.DeliveryRetried {
		t.Fatalf("the retry must be recorded on the result: %+v", res)
	}
	if !strings.Contains(res.DeliveryError, res.SessionPane) {
		t.Fatalf("delivery error must name the pane: %q", res.DeliveryError)
	}
	if got := result.UnconfirmedPanes(); len(got) != 1 || got[0] != res.SessionPane {
		t.Fatalf("UnconfirmedPanes = %v, want [%s]", got, res.SessionPane)
	}
}

// TestConfirmSwarmDeliveryNeverResendsConfirmedPane is the planted negative:
// when the capture shows the orders landed, no re-send may happen.
func TestConfirmSwarmDeliveryNeverResendsConfirmedPane(t *testing.T) {
	echo := "Agent ready\nRECEIVED: " + bdLjdProbePrompt("no-resend") + "\n❯ "
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{echo},
	}
	injector := newReadbackTestInjector(client)

	result := &BatchInjectionResult{
		TotalPanes: 1,
		Results: []InjectionResult{{
			SessionPane: "bd-ljd-unit:1.0",
			AgentType:   "pi",
			Success:     true,
		}},
	}

	injector.confirmSwarmDeliveries(context.Background(), bdLjdProbePrompt("no-resend"), result)

	if client.sendForAgentCount != 0 {
		t.Fatalf("a pane whose capture shows the orders landed must never be re-sent; got %d re-sends", client.sendForAgentCount)
	}
	res := result.Results[0]
	if !res.DeliveryConfirmed {
		t.Fatalf("delivery must be confirmed by the orders echo: %+v", res)
	}
	if res.DeliverySignal != swarmDeliverySignalOrdersText {
		t.Fatalf("delivery signal = %q, want orders_text", res.DeliverySignal)
	}
	if res.DeliveryRetried {
		t.Fatalf("no retry may be recorded on a confirmed pane: %+v", res)
	}
	if result.Unconfirmed != 0 {
		t.Fatalf("unconfirmed count must stay zero: %d", result.Unconfirmed)
	}
}

// TestConfirmSwarmDeliveryAcceptsContextMeterEvidence proves the second
// evidence signal: a pi pane whose meter moved off zero is confirmed without
// a re-send even when the orders text is not in the capture.
func TestConfirmSwarmDeliveryAcceptsContextMeterEvidence(t *testing.T) {
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{bdLjdPiMeterMovedCapture},
	}
	injector := newReadbackTestInjector(client)

	result := &BatchInjectionResult{
		TotalPanes: 1,
		Results: []InjectionResult{{
			SessionPane: "bd-ljd-unit:1.0",
			AgentType:   "pi",
			Success:     true,
		}},
	}

	injector.confirmSwarmDeliveries(context.Background(), bdLjdProbePrompt("meter-only"), result)

	if client.sendForAgentCount != 0 {
		t.Fatalf("meter evidence must end the check without a re-send; got %d re-sends", client.sendForAgentCount)
	}
	res := result.Results[0]
	if !res.DeliveryConfirmed || res.DeliverySignal != swarmDeliverySignalContextMeter {
		t.Fatalf("expected confirmation via context_meter: %+v", res)
	}
}

// TestConfirmSwarmDeliveryReportsRetrySendFailure covers the path where the
// retry itself cannot be sent: the pane is reported unconfirmed with the send
// failure in the error, and no second retry is attempted.
func TestConfirmSwarmDeliveryReportsRetrySendFailure(t *testing.T) {
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{bdLjdPiPristineCapture},
		sendErrOnCall: map[int]error{
			1: errors.New("tmux send-keys: pane dead"),
		},
	}
	injector := newReadbackTestInjector(client)

	result := &BatchInjectionResult{
		TotalPanes: 1,
		Results: []InjectionResult{{
			SessionPane: "bd-ljd-unit:1.0",
			AgentType:   "pi",
			Success:     true,
		}},
	}

	injector.confirmSwarmDeliveries(context.Background(), bdLjdProbePrompt("retry-dead"), result)

	if client.sendForAgentCount != 1 {
		t.Fatalf("a failed retry send must not be retried again; got %d re-sends", client.sendForAgentCount)
	}
	res := result.Results[0]
	if res.DeliveryConfirmed {
		t.Fatalf("pane must not be reported confirmed: %+v", res)
	}
	if !strings.Contains(res.DeliveryError, "retry send failed") || !strings.Contains(res.DeliveryError, res.SessionPane) {
		t.Fatalf("delivery error must name the pane and the retry failure: %q", res.DeliveryError)
	}
	if result.Unconfirmed != 1 {
		t.Fatalf("Unconfirmed = %d, want 1", result.Unconfirmed)
	}
}

// TestConfirmSwarmDeliveriesSkipsFailedSendsAndEmptyPrompt pins the two
// short-circuits: a send that already failed is reported by its own send
// error (no read-back), and an empty prompt has nothing to look for.
func TestConfirmSwarmDeliveriesSkipsFailedSendsAndEmptyPrompt(t *testing.T) {
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{bdLjdPiPristineCapture},
	}
	injector := newReadbackTestInjector(client)

	result := &BatchInjectionResult{
		TotalPanes: 2,
		Results: []InjectionResult{
			{SessionPane: "bd-ljd-unit:1.0", AgentType: "pi", Success: false, Error: "send prompt text: boom"},
			{SessionPane: "bd-ljd-unit:1.1", AgentType: "pi", Success: true},
		},
	}

	injector.confirmSwarmDeliveries(context.Background(), "   ", result)

	if client.sendForAgentCount != 0 {
		t.Fatalf("empty prompt must skip the read-back entirely; got %d sends", client.sendForAgentCount)
	}
	if result.Unconfirmed != 0 {
		t.Fatalf("Unconfirmed = %d, want 0", result.Unconfirmed)
	}

	injector.confirmSwarmDeliveries(context.Background(), bdLjdProbePrompt("mixed"), result)
	if result.Results[0].DeliveryError != "" || result.Results[0].DeliveryRetried {
		t.Fatalf("a send that already failed must be left to its own report: %+v", result.Results[0])
	}
	if result.Unconfirmed != 1 {
		t.Fatalf("only the send-reported-success pane is unconfirmed; got %d", result.Unconfirmed)
	}
}

// TestConfirmSwarmDeliveriesReportsSkippedReadbackWhenContextCancelled: a
// spawn cancelled before the read-back must not leave its panes silently
// unchecked — each successful send is reported as unverified.
func TestConfirmSwarmDeliveriesReportsSkippedReadbackWhenContextCancelled(t *testing.T) {
	client := &scriptedReadbackClient{
		idleCaptures: bdLjdPiPristineCapture,
		readbacks:    []string{bdLjdPiPristineCapture},
	}
	injector := newReadbackTestInjector(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := &BatchInjectionResult{
		TotalPanes: 1,
		Results: []InjectionResult{{
			SessionPane: "bd-ljd-unit:1.0",
			AgentType:   "pi",
			Success:     true,
		}},
	}

	injector.confirmSwarmDeliveries(ctx, bdLjdProbePrompt("cancelled"), result)

	if client.sendForAgentCount != 0 {
		t.Fatalf("a cancelled context must not trigger a retry send; got %d sends", client.sendForAgentCount)
	}
	res := result.Results[0]
	if !strings.Contains(res.DeliveryError, "read-back skipped") {
		t.Fatalf("cancelled read-back must be reported, not silent: %+v", res)
	}
	if result.Unconfirmed != 1 {
		t.Fatalf("Unconfirmed = %d, want 1", result.Unconfirmed)
	}
}

// TestSwarmEvidenceIgnoresMeterForNonPiAgents: the meter signal is pi's; a
// codex-style context-remaining line must not be read as pi's used-percentage.
func TestSwarmEvidenceIgnoresMeterForNonPiAgents(t *testing.T) {
	found, signal := swarmDeliveryEvidence("47% context left · ? for shortcuts", bdLjdProbePrompt("x"), "cod")
	if found {
		t.Fatalf("cod capture must not gain evidence from a context-remaining display (signal=%q)", signal)
	}
}
