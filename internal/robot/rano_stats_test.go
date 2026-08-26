package robot

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/integrations/rano"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tools"
)

func TestNormalizeRanoWindowDefault(t *testing.T) {
	got, err := normalizeRanoWindow("")
	if err != nil {
		t.Fatalf("normalizeRanoWindow returned error: %v", err)
	}
	if got != "5m" {
		t.Fatalf("expected default window 5m, got %s", got)
	}
}

func TestNormalizeRanoWindowInvalid(t *testing.T) {
	if _, err := normalizeRanoWindow("5x"); err == nil {
		t.Fatal("expected error for invalid duration, got nil")
	}
}

func TestAggregateRanoStats(t *testing.T) {
	stats := []tools.RanoProcessStats{
		{PID: 100, RequestCount: 2, BytesIn: 10, BytesOut: 5, LastRequest: "2026-01-01T00:00:01Z"},
		{PID: 101, RequestCount: 3, BytesIn: 20, BytesOut: 8, LastRequest: "2026-01-01T00:00:02Z"},
		{PID: 200, RequestCount: 1, BytesIn: 5, BytesOut: 2, LastRequest: "2026-01-01T00:00:03Z"},
	}

	pidLookup := func(pid int) *rano.PaneIdentity {
		switch pid {
		case 100, 101:
			return &rano.PaneIdentity{
				Session:   "s1",
				PaneIndex: 1,
				PaneTitle: "s1__cc_1",
				NTMIndex:  1,
			}
		case 200:
			return &rano.PaneIdentity{
				Session:   "s1",
				PaneIndex: 2,
				PaneTitle: "s1__cc_2",
				NTMIndex:  2,
			}
		default:
			return nil
		}
	}

	allowPane := func(identity *rano.PaneIdentity) bool {
		return identity != nil && identity.PaneTitle == "s1__cc_1"
	}

	panes, total := aggregateRanoStats(stats, pidLookup, allowPane)

	pane, ok := panes["s1__cc_1"]
	if !ok {
		t.Fatalf("expected pane s1__cc_1 to be present")
	}
	if pane.RequestCount != 5 {
		t.Fatalf("expected request count 5, got %d", pane.RequestCount)
	}
	if pane.BytesIn != 30 {
		t.Fatalf("expected bytes_in 30, got %d", pane.BytesIn)
	}
	if pane.BytesOut != 13 {
		t.Fatalf("expected bytes_out 13, got %d", pane.BytesOut)
	}
	if pane.LastRequest != "2026-01-01T00:00:02Z" {
		t.Fatalf("expected last_request 2026-01-01T00:00:02Z, got %s", pane.LastRequest)
	}
	if len(pane.PIDs) != 2 {
		t.Fatalf("expected 2 pids, got %d", len(pane.PIDs))
	}

	if total.RequestCount != 5 {
		t.Fatalf("expected total request count 5, got %d", total.RequestCount)
	}
	if total.BytesIn != 30 {
		t.Fatalf("expected total bytes_in 30, got %d", total.BytesIn)
	}
	if total.BytesOut != 13 {
		t.Fatalf("expected total bytes_out 13, got %d", total.BytesOut)
	}
}

// ranoStatsTestInventory is a stubbed pane inventory with two sessions: s1
// holds panes 1-2 and s2 holds panes 1-3, so a requested index can exist in
// one session but not the other. Each pane has one process stat.
func ranoStatsTestInventory() ([]tmux.Session, map[string][]tmux.Pane, []tools.RanoProcessStats, func(int) *rano.PaneIdentity) {
	sessions := []tmux.Session{{Name: "s1"}, {Name: "s2"}}
	panesBySession := map[string][]tmux.Pane{
		"s1": {
			{ID: "%1", Index: 1, Title: "s1__cc_1"},
			{ID: "%2", Index: 2, Title: "s1__cc_2"},
		},
		"s2": {
			{ID: "%3", Index: 1, Title: "s2__cc_1"},
			{ID: "%4", Index: 2, Title: "s2__cc_2"},
			{ID: "%5", Index: 3, Title: "s2__cc_3"},
		},
	}
	stats := []tools.RanoProcessStats{
		{PID: 101, RequestCount: 2, BytesIn: 10, BytesOut: 5},
		{PID: 102, RequestCount: 3, BytesIn: 20, BytesOut: 8},
		{PID: 201, RequestCount: 1, BytesIn: 5, BytesOut: 2},
		{PID: 202, RequestCount: 4, BytesIn: 40, BytesOut: 16},
		{PID: 203, RequestCount: 5, BytesIn: 50, BytesOut: 20},
	}
	pidLookup := func(pid int) *rano.PaneIdentity {
		switch pid {
		case 101:
			return &rano.PaneIdentity{Session: "s1", PaneIndex: 1, PaneTitle: "s1__cc_1"}
		case 102:
			return &rano.PaneIdentity{Session: "s1", PaneIndex: 2, PaneTitle: "s1__cc_2"}
		case 201:
			return &rano.PaneIdentity{Session: "s2", PaneIndex: 1, PaneTitle: "s2__cc_1"}
		case 202:
			return &rano.PaneIdentity{Session: "s2", PaneIndex: 2, PaneTitle: "s2__cc_2"}
		case 203:
			return &rano.PaneIdentity{Session: "s2", PaneIndex: 3, PaneTitle: "s2__cc_3"}
		default:
			return nil
		}
	}
	return sessions, panesBySession, stats, pidLookup
}

// TestRanoStatsSessionScope is the bd-y5rmg acceptance suite: --session
// scopes the statistics to one session's panes, --panes and --session
// intersect with panes outside the session reported rather than dropped, and
// an unknown session yields SESSION_NOT_FOUND. The inventory is stubbed so
// no tmux server or rano install is needed.
func TestRanoStatsSessionScope(t *testing.T) {
	sessions, panesBySession, stats, pidLookup := ranoStatsTestInventory()

	tests := []struct {
		name        string
		opts        RanoStatsOptions
		wantPanes   []string
		wantOutside []int
		wantError   string
	}{
		{
			name:      "scoped to one of two sessions counts only its panes",
			opts:      RanoStatsOptions{Session: "s1"},
			wantPanes: []string{"s1__cc_1", "s1__cc_2"},
		},
		{
			name:      "unscoped counts every pane",
			opts:      RanoStatsOptions{},
			wantPanes: []string{"s1__cc_1", "s1__cc_2", "s2__cc_1", "s2__cc_2", "s2__cc_3"},
		},
		{
			name:      "panes and session intersect when all panes are inside",
			opts:      RanoStatsOptions{Session: "s1", Panes: []int{1, 2}},
			wantPanes: []string{"s1__cc_1", "s1__cc_2"},
		},
		{
			name:      "pane filter excludes a pane inside the session",
			opts:      RanoStatsOptions{Session: "s1", Panes: []int{1}},
			wantPanes: []string{"s1__cc_1"},
		},
		{
			name:        "pane outside the session is reported not dropped",
			opts:        RanoStatsOptions{Session: "s1", Panes: []int{2, 3}},
			wantPanes:   []string{"s1__cc_2"},
			wantOutside: []int{3},
		},
		{
			name:      "unknown session returns SESSION_NOT_FOUND",
			opts:      RanoStatsOptions{Session: "nope"},
			wantError: ErrCodeSessionNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := ranoStatsFromInventory(tt.opts, "5m", sessions, panesBySession, stats, pidLookup)

			if tt.wantError != "" {
				if output.Success {
					t.Errorf("expected success=false, got success=true")
				}
				if output.ErrorCode != tt.wantError {
					t.Errorf("expected error_code %q, got %q", tt.wantError, output.ErrorCode)
				}
				return
			}

			if !output.Success {
				t.Fatalf("expected success=true, got success=false (error_code=%q error=%q)", output.ErrorCode, output.Error)
			}
			if output.Query.Session != tt.opts.Session {
				t.Errorf("query.session = %q, want %q", output.Query.Session, tt.opts.Session)
			}
			if !reflect.DeepEqual(output.PanesOutsideSession, tt.wantOutside) {
				t.Errorf("panes_outside_session = %v, want %v", output.PanesOutsideSession, tt.wantOutside)
			}

			gotPanes := make([]string, 0, len(output.Panes))
			for key := range output.Panes {
				gotPanes = append(gotPanes, key)
			}
			sort.Strings(gotPanes)
			if !reflect.DeepEqual(gotPanes, tt.wantPanes) {
				t.Errorf("panes = %v, want %v", gotPanes, tt.wantPanes)
			}
		})
	}
}

// TestRanoStatsUnscopedOutputUnchanged is the bd-y5rmg regression guard: with
// no --session the response must be byte-identical to the pre-filter shape.
// The timestamp is the only moving part and is normalized; everything else is
// compared verbatim, so a new field leaking into the unscoped envelope (a
// session echo or an empty outside list) fails this test.
func TestRanoStatsUnscopedOutputUnchanged(t *testing.T) {
	sessions, panesBySession, stats, pidLookup := ranoStatsTestInventory()

	output := ranoStatsFromInventory(RanoStatsOptions{}, "5m", sessions, panesBySession, stats, pidLookup)
	output.Timestamp = "2026-01-01T00:00:00Z"

	got, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	want := `{"success":true,"timestamp":"2026-01-01T00:00:00Z","version":"1.0.0","output_format":"auto","window":"5m","query":{},"panes":{"s1__cc_1":{"session":"s1","pane_index":1,"pane_title":"s1__cc_1","pids":[101],"request_count":2,"bytes_in":10,"bytes_out":5},"s1__cc_2":{"session":"s1","pane_index":2,"pane_title":"s1__cc_2","pids":[102],"request_count":3,"bytes_in":20,"bytes_out":8},"s2__cc_1":{"session":"s2","pane_index":1,"pane_title":"s2__cc_1","pids":[201],"request_count":1,"bytes_in":5,"bytes_out":2},"s2__cc_2":{"session":"s2","pane_index":2,"pane_title":"s2__cc_2","pids":[202],"request_count":4,"bytes_in":40,"bytes_out":16},"s2__cc_3":{"session":"s2","pane_index":3,"pane_title":"s2__cc_3","pids":[203],"request_count":5,"bytes_in":50,"bytes_out":20}},"total":{"request_count":15,"bytes_in":125,"bytes_out":51}}`
	if string(got) != want {
		t.Errorf("unscoped output changed:\ngot  %s\nwant %s", got, want)
	}
}
