package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/reservationsim"
)

func jsonEncodeIndent(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func TestBuildLocksAdviceResult_AgentMailUnavailableKeepsProofMode(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	result, err := buildLocksAdviceResult(
		t.Context(),
		"proj",
		"",
		"/repo",
		nil,
		nil,
		[]string{"Agent Mail server unavailable"},
		now,
		true,
		"connection refused",
	)
	if err != nil {
		t.Fatalf("buildLocksAdviceResult() error = %v", err)
	}

	if !result.Success {
		t.Fatal("Success = false, want proof-mode success")
	}
	if result.AgentMailAvailable {
		t.Fatal("AgentMailAvailable = true, want false")
	}
	if result.Reservations.AgentMailAvailable {
		t.Fatal("reservation report AgentMailAvailable = true, want false")
	}
	if len(result.Reservations.Warnings) != 1 {
		t.Fatalf("reservation warnings = %d, want 1", len(result.Reservations.Warnings))
	}
	if result.RecommendationCount != 0 {
		t.Fatalf("RecommendationCount = %d, want 0", result.RecommendationCount)
	}
}

func TestBuildLocksAdviceResult_CombinesReservationAndWorktreeLogRows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	result, err := buildLocksAdviceResult(
		t.Context(),
		"proj",
		"BlueLake",
		"/repo",
		[]agentmail.FileReservation{
			{
				ID:          11,
				PathPattern: "**",
				AgentName:   "BlueLake",
				Exclusive:   true,
				CreatedTS:   agentmail.FlexTime{Time: now.Add(-3 * time.Hour)},
				ExpiresTS:   agentmail.FlexTime{Time: now.Add(5 * time.Minute)},
			},
		},
		nil,
		nil,
		now,
		false,
		"",
	)
	if err != nil {
		t.Fatalf("buildLocksAdviceResult() error = %v", err)
	}

	if !result.AgentMailAvailable {
		t.Fatal("AgentMailAvailable = false, want true")
	}
	if result.RecommendationCount != 1 {
		t.Fatalf("RecommendationCount = %d, want 1", result.RecommendationCount)
	}
	if len(result.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(result.LogRows))
	}
	row := result.LogRows[0]
	if !locksTextEqual(row.Source, "reservation") || row.ReservationID != 11 || !locksTextEqual(row.PathPattern, "**") || !locksTextEqual(row.Holder, "BlueLake") {
		t.Fatalf("unexpected row: %+v", row)
	}
	if !locksTextEqual(row.Action, reservationsim.ReservationActionNarrow) && !locksTextEqual(row.Action, reservationsim.ReservationActionRenew) {
		t.Fatalf("Action = %q, want narrow or renew", row.Action)
	}
}

func TestBuildLocksAdviceResultRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := buildLocksAdviceResult(ctx, "proj", "", t.TempDir(), nil, nil, nil, time.Now(), false, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("buildLocksAdviceResult() error = %v, want context.Canceled", err)
	}
}

func locksTextEqual(a, b string) bool {
	return strings.Compare(a, b) == 0
}

// Test the path-matching helper that decides whether a configured
// reservation pattern (which may include `/` directory prefixes or
// `*`/`**` glob meta) covers a queried path. The wrapper-facing
// contract from ntm#127 depends on this function being precise:
// false positives would tell wrappers a path is held when it isn't,
// false negatives would let wrappers proceed when they shouldn't.
func TestLocksCheckPathMatches_ExactAndPrefixAndGlobs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		path    string
		pattern string
		want    bool
	}{
		// Exact match
		{"exact", "src/auth.rs", "src/auth.rs", true},
		// Path inside directory pattern (prefix with /)
		{"prefix_subdir", "src/auth.rs", "src", true},
		{"prefix_trailing_slash", "src/auth.rs", "src/", true},
		// Path not inside pattern
		{"unrelated", "tests/auth.rs", "src", false},
		{"sibling", "src2/auth.rs", "src", false},
		// Recursive glob
		{"recursive_glob_match", "src/auth/handler.rs", "src/**", true},
		{"recursive_glob_root_match", "src/auth.rs", "src/**", true},
		{"recursive_glob_unrelated", "tests/auth.rs", "src/**", false},
		{"recursive_glob_directory_itself", "src", "src/**", true},
		// Bare ** is the broad catch-all used by reservation tooling.
		{"bare_recursive_glob", "internal/cli/locks.go", "**", true},
		{"bare_recursive_glob_absolute", "/data/projects/foo/internal/cli/locks.go", "**", true},
		// Project root reservations normalize to "." and cover every
		// project-relative path, but not unrelated absolute paths.
		{"project_root_dot_matches_relative_child", "internal/cli/locks.go", ".", true},
		{"project_root_dot_matches_root", ".", ".", true},
		{"project_root_dot_does_not_match_outside_absolute", "/tmp/ntm/internal/cli/locks.go", ".", false},
		{"project_root_dot_does_not_match_parent_escape", "../other/repo/file.go", ".", false},
		{"project_root_dot_does_not_match_cleaned_parent_escape", "internal/../../other/repo/file.go", ".", false},
		// Suffix recursive glob
		{"suffix_recursive", "src/auth/handler.rs", "**/handler.rs", true},
		{"suffix_recursive_middle", "internal/cli/locks.go", "internal/**/*.go", true},
		{"suffix_recursive_middle_unrelated_ext", "internal/cli/locks.txt", "internal/**/*.go", false},
		{"prefix_recursive_suffix", "internal/cli/locks.go", "**/*.go", true},
		// Single-char wildcards
		{"single_segment_glob", "auth.rs", "*.rs", true},
		{"single_segment_glob_no_slash_crossing", "src/auth.rs", "*.rs", false},
		{"single_segment_subdir_glob", "src/auth.rs", "src/*.rs", true},
		{"single_segment_subdir_glob_no_deep_crossing", "src/auth/handler.rs", "src/*.rs", false},
		// Empty pattern shouldn't match anything
		{"empty_pattern", "src/auth.rs", "", false},
		// Regression: empty pattern + absolute path must NOT match.
		// Without the explicit guard, HasPrefix("/abs/path", ""+"/")
		// would return true and incorrectly report `blocked` for
		// every absolute-path query whenever any reservation
		// somehow ended up with an empty path_pattern. This is the
		// shape that motivated the empty-pattern guard.
		{"empty_pattern_absolute_path", "/data/projects/foo", "", false},
		{"empty_pattern_root", "/", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := locksCheckPathMatches(tc.path, tc.pattern)
			if got != tc.want {
				t.Fatalf("locksCheckPathMatches(%q, %q) = %v, want %v",
					tc.path, tc.pattern, got, tc.want)
			}
		})
	}
}

func TestLocksComparableReservationPath_ProjectRelativeMatching(t *testing.T) {
	t.Parallel()
	projectKey := "/data/projects/ntm"
	cases := []struct {
		name        string
		path        string
		pattern     string
		wantMatch   bool
		wantPath    string
		wantPattern string
	}{
		{
			name:        "absolute_query_matches_relative_pattern",
			path:        "/data/projects/ntm/internal/cli/locks.go",
			pattern:     "internal/**",
			wantMatch:   true,
			wantPath:    "internal/cli/locks.go",
			wantPattern: "internal/**",
		},
		{
			name:        "relative_query_matches_absolute_pattern",
			path:        "internal/cli/locks.go",
			pattern:     "/data/projects/ntm/internal/**/*.go",
			wantMatch:   true,
			wantPath:    "internal/cli/locks.go",
			wantPattern: "internal/**/*.go",
		},
		{
			name:        "dotdot_query_matches_relative_pattern",
			path:        "/data/projects/ntm/internal/../go.mod",
			pattern:     "go.mod",
			wantMatch:   true,
			wantPath:    "go.mod",
			wantPattern: "go.mod",
		},
		{
			name:        "dot_segments_absolute_pattern_cleans_before_project_relative_compare",
			path:        "internal/cli/locks.go",
			pattern:     "/data/projects/ntm/./internal/cli/../cli/*.go",
			wantMatch:   true,
			wantPath:    "internal/cli/locks.go",
			wantPattern: "internal/cli/*.go",
		},
		{
			name:        "absolute_project_root_pattern_blocks_project_child",
			path:        "/data/projects/ntm/internal/cli/locks.go",
			pattern:     "/data/projects/ntm",
			wantMatch:   true,
			wantPath:    "internal/cli/locks.go",
			wantPattern: ".",
		},
		{
			name:        "absolute_project_root_pattern_blocks_project_root",
			path:        "/data/projects/ntm",
			pattern:     "/data/projects/ntm",
			wantMatch:   true,
			wantPath:    ".",
			wantPattern: ".",
		},
		{
			name:        "absolute_query_outside_project_stays_absolute",
			path:        "/tmp/ntm/internal/cli/locks.go",
			pattern:     "internal/**",
			wantMatch:   false,
			wantPath:    "/tmp/ntm/internal/cli/locks.go",
			wantPattern: "internal/**",
		},
		{
			name:        "project_root_pattern_does_not_block_outside_absolute_query",
			path:        "/tmp/ntm/internal/cli/locks.go",
			pattern:     "/data/projects/ntm",
			wantMatch:   false,
			wantPath:    "/tmp/ntm/internal/cli/locks.go",
			wantPattern: ".",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPath := locksComparableReservationPath(tc.path, projectKey)
			gotPattern := locksComparableReservationPath(tc.pattern, projectKey)
			if gotPath != tc.wantPath {
				t.Fatalf("comparable path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotPattern != tc.wantPattern {
				t.Fatalf("comparable pattern = %q, want %q", gotPattern, tc.wantPattern)
			}
			if got := locksCheckPathMatches(gotPath, gotPattern); got != tc.wantMatch {
				t.Fatalf("locksCheckPathMatches(%q, %q) = %v, want %v", gotPath, gotPattern, got, tc.wantMatch)
			}
		})
	}
}

func TestSelectLocksCheckHolder_IgnoresSharedReservations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	future := agentmail.FlexTime{Time: now.Add(time.Hour)}
	reservations := []agentmail.FileReservation{
		{
			ID:          1,
			PathPattern: "internal/**",
			AgentName:   "OtherAgent",
			Exclusive:   false,
			ExpiresTS:   future,
		},
		{
			ID:          2,
			PathPattern: "internal/cli/*.go",
			AgentName:   "BlueLake",
			Exclusive:   false,
			ExpiresTS:   future,
		},
	}

	holder := selectLocksCheckHolder(reservations, "BlueLake", "internal/cli/locks.go", "/data/projects/ntm", now)
	if holder != nil {
		t.Fatalf("shared reservations must not decide locks check state; got holder %+v", *holder)
	}
}

func TestSelectLocksCheckHolder_PrefersOwnExclusiveReservation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	future := agentmail.FlexTime{Time: now.Add(time.Hour)}
	reservations := []agentmail.FileReservation{
		{
			ID:          1,
			PathPattern: "internal/**",
			AgentName:   "OtherAgent",
			Exclusive:   true,
			ExpiresTS:   future,
		},
		{
			ID:          2,
			PathPattern: "/data/projects/ntm/internal/cli/locks.go",
			AgentName:   "BlueLake",
			Exclusive:   true,
			ExpiresTS:   future,
		},
	}

	holder := selectLocksCheckHolder(reservations, "BlueLake", "internal/cli/locks.go", "/data/projects/ntm", now)
	if holder == nil {
		t.Fatal("holder = nil, want caller's own exclusive reservation")
	}
	if holder.ID != 2 {
		t.Fatalf("holder.ID = %d, want caller's own exclusive reservation ID 2", holder.ID)
	}
}

func TestSelectLocksCheckHolder_IgnoresInactiveReservations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	future := agentmail.FlexTime{Time: now.Add(time.Hour)}
	past := agentmail.FlexTime{Time: now.Add(-time.Minute)}
	releasedAt := agentmail.FlexTime{Time: now.Add(-30 * time.Second)}
	reservations := []agentmail.FileReservation{
		{
			ID:          1,
			PathPattern: "internal/**",
			AgentName:   "MissingExpiry",
			Exclusive:   true,
		},
		{
			ID:          2,
			PathPattern: "internal/**",
			AgentName:   "Expired",
			Exclusive:   true,
			ExpiresTS:   past,
		},
		{
			ID:          3,
			PathPattern: "internal/**",
			AgentName:   "Released",
			Exclusive:   true,
			ExpiresTS:   future,
			ReleasedTS:  &releasedAt,
		},
	}

	holder := selectLocksCheckHolder(reservations, "BlueLake", "internal/cli/locks.go", "/data/projects/ntm", now)
	if holder != nil {
		t.Fatalf("inactive reservations must not decide locks check state; got holder %+v", *holder)
	}
}

// Pin the JSON envelope's contract: the four wrapper-facing fields
// (state, holder, audit_token, observed_at) are present in the
// stable shape, and `holder == null` cleanly distinguishes the
// `free` case from `held`/`blocked`. Wrappers depend on this for
// `jq '.holder == null'`-style filtering, so a future refactor
// that drops `omitempty` would break their integration silently.
func TestLocksCheckResult_FreeStateOmitsHolder(t *testing.T) {
	t.Parallel()
	observedAt := "2026-05-12T12:00:00Z"
	r := LocksCheckResult{
		Success:    true,
		Session:    "myproject",
		ProjectKey: "/data/projects/foo",
		Path:       "src/auth.rs",
		State:      "free",
		ObservedAt: observedAt,
		AuditToken: newLocksCheckAuditToken("foo", "src/auth.rs", observedAt),
	}
	bytes, err := jsonMarshalIndent(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(bytes)
	if strings.Contains(out, "\"holder\"") {
		t.Fatalf(
			"free state must omit holder field for jq filtering; got:\n%s",
			out,
		)
	}
	if !strings.Contains(out, "\"state\": \"free\"") {
		t.Fatalf("expected `\"state\": \"free\"` in output:\n%s", out)
	}
	if !strings.Contains(out, "\"audit_token\"") {
		t.Fatalf("expected audit_token field in output:\n%s", out)
	}
}

func TestLocksCheckResult_HeldStatePopulatesHolder(t *testing.T) {
	t.Parallel()
	observedAt := "2026-05-12T12:00:00Z"
	r := LocksCheckResult{
		Success:    true,
		Session:    "myproject",
		ProjectKey: "/data/projects/foo",
		Path:       "src/auth.rs",
		State:      "held",
		Holder: &LocksCheckHolder{
			Agent:         "agent-alpha",
			Reason:        "feature work",
			ExpiresAt:     "2026-05-12T13:00:00Z",
			Exclusive:     true,
			PathPattern:   "src/auth.rs",
			ReservationID: 42,
		},
		ObservedAt: observedAt,
		AuditToken: newLocksCheckAuditToken("foo", "src/auth.rs", observedAt),
	}
	bytes, err := jsonMarshalIndent(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(bytes)
	if !strings.Contains(out, "agent-alpha") {
		t.Fatalf("held state must serialize holder.agent; got:\n%s", out)
	}
	if !strings.Contains(out, "\"reservation_id\": 42") {
		t.Fatalf("held state must serialize reservation_id; got:\n%s", out)
	}
}

// jsonMarshalIndent serializes the LocksCheckResult envelope the
// same way the CLI does (two-space indent). Used by the
// envelope-shape tests above.
func jsonMarshalIndent(v interface{}) ([]byte, error) {
	return jsonEncodeIndent(v)
}

func TestLocksList_NoSessionArg_PrintsRequiredAndExample(t *testing.T) {
	t.Parallel()
	cmd := newLocksListCmd()
	err := cmd.Args(cmd, []string{})
	if err == nil {
		t.Fatal("expected error when no session is provided, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "session is required") {
		t.Fatalf("error missing 'session is required': got %q", msg)
	}
	if !strings.Contains(msg, "ntm locks list myproject") {
		t.Fatalf("error missing example 'ntm locks list myproject': got %q", msg)
	}
	if strings.Contains(msg, "accepts 1 arg(s), received 0") {
		t.Fatalf("error should not be bare arity error: got %q", msg)
	}
}

func TestLocksList_OutputFormatting_FourCases(t *testing.T) {
	now := time.Now().UTC()
	future := agentmail.FlexTime{Time: now.Add(45 * time.Minute)}
	resAmber := agentmail.FileReservation{
		ID:          1,
		PathPattern: "src/auth.go",
		AgentName:   "AmberCove",
		Exclusive:   true,
		Reason:      "auth refactor",
		CreatedTS:   agentmail.FlexTime{Time: now.Add(-15 * time.Minute)},
		ExpiresTS:   future,
	}
	resGray := agentmail.FileReservation{
		ID:          2,
		PathPattern: "src/domain/mod.rs",
		AgentName:   "GrayCompass",
		Exclusive:   true,
		Reason:      "domain models",
		CreatedTS:   agentmail.FlexTime{Time: now.Add(-10 * time.Minute)},
		ExpiresTS:   future,
	}

	t.Run("case1_nothing_held_anywhere", func(t *testing.T) {
		res := LocksResult{
			Success:    true,
			Session:    "myproject",
			Agent:      "AmberCove",
			ProjectKey: "/home/gabriel/repositories/repo",
			Count:      0,
		}

		// Single-agent scope
		outSingle := captureLocksStdout(t, func() error {
			return printLocksResult(res, false)
		})
		firstLineSingle := strings.TrimSpace(strings.Split(outSingle, "\n")[0])
		wantSingleHeader := "No active file reservations for agent AmberCove (single-agent scope)"
		if firstLineSingle != wantSingleHeader {
			t.Fatalf("single-agent count line = %q, want %q", firstLineSingle, wantSingleHeader)
		}
		if !strings.Contains(outSingle, "Agent: AmberCove") || !strings.Contains(outSingle, "Project: /home/gabriel/repositories/repo") {
			t.Fatalf("single-agent output missing context:\n%s", outSingle)
		}
		if strings.Contains(outSingle, "session scope") {
			t.Fatalf("single-agent output contains ambiguous 'session scope':\n%s", outSingle)
		}
		if strings.Contains(outSingle, "project scope") || strings.Contains(outSingle, "across all agents") {
			t.Fatalf("single-agent output contains all-agents wording:\n%s", outSingle)
		}

		// All-agents scope
		outAll := captureLocksStdout(t, func() error {
			return printLocksResult(res, true)
		})
		firstLineAll := strings.TrimSpace(strings.Split(outAll, "\n")[0])
		wantAllHeader := "No active file reservations across all agents (project scope, queried by AmberCove)"
		if firstLineAll != wantAllHeader {
			t.Fatalf("all-agents count line = %q, want %q", firstLineAll, wantAllHeader)
		}
		if !strings.Contains(outAll, "Agent: AmberCove") || !strings.Contains(outAll, "Project: /home/gabriel/repositories/repo") {
			t.Fatalf("all-agents output missing context:\n%s", outAll)
		}
		if strings.Contains(outAll, "single-agent scope") || strings.Contains(outAll, "session scope") {
			t.Fatalf("all-agents output contains single-agent/session wording:\n%s", outAll)
		}
	})

	t.Run("case2_querier_holds_reservation", func(t *testing.T) {
		// Single-agent scope
		resSingle := LocksResult{
			Success:      true,
			Session:      "myproject",
			Agent:        "AmberCove",
			ProjectKey:   "/home/gabriel/repositories/repo",
			Reservations: []agentmail.FileReservation{resAmber},
			Count:        1,
		}
		outSingle := captureLocksStdout(t, func() error {
			return printLocksResult(resSingle, false)
		})
		firstLineSingle := strings.TrimSpace(strings.Split(outSingle, "\n")[0])
		wantSingleHeader := "File Reservations: 1 active for agent AmberCove (single-agent scope)"
		if firstLineSingle != wantSingleHeader {
			t.Fatalf("single-agent count line = %q, want %q", firstLineSingle, wantSingleHeader)
		}
		if !strings.Contains(outSingle, "[#1] src/auth.go") || !strings.Contains(outSingle, "Agent: AmberCove") {
			t.Fatalf("single-agent output missing AmberCove reservation:\n%s", outSingle)
		}
		if strings.Contains(outSingle, "project scope") || strings.Contains(outSingle, "across all agents") {
			t.Fatalf("single-agent output contains all-agents wording:\n%s", outSingle)
		}

		// All-agents scope
		resAll := LocksResult{
			Success:      true,
			Session:      "myproject",
			Agent:        "AmberCove",
			ProjectKey:   "/home/gabriel/repositories/repo",
			Reservations: []agentmail.FileReservation{resAmber},
			Count:        1,
		}
		outAll := captureLocksStdout(t, func() error {
			return printLocksResult(resAll, true)
		})
		firstLineAll := strings.TrimSpace(strings.Split(outAll, "\n")[0])
		wantAllHeader := "File Reservations: 1 active across all agents (project scope, queried by AmberCove)"
		if firstLineAll != wantAllHeader {
			t.Fatalf("all-agents count line = %q, want %q", firstLineAll, wantAllHeader)
		}
		if !strings.Contains(outAll, "[#1] src/auth.go") || !strings.Contains(outAll, "Agent: AmberCove") {
			t.Fatalf("all-agents output missing AmberCove reservation:\n%s", outAll)
		}
		if strings.Contains(outAll, "single-agent scope") {
			t.Fatalf("all-agents output contains single-agent wording:\n%s", outAll)
		}
	})

	t.Run("case3_other_agent_holds_reservation", func(t *testing.T) {
		// Single-agent scope (querier AmberCove has 0)
		resSingle := LocksResult{
			Success:    true,
			Session:    "myproject",
			Agent:      "AmberCove",
			ProjectKey: "/home/gabriel/repositories/repo",
			Count:      0,
		}
		outSingle := captureLocksStdout(t, func() error {
			return printLocksResult(resSingle, false)
		})
		firstLineSingle := strings.TrimSpace(strings.Split(outSingle, "\n")[0])
		wantSingleHeader := "No active file reservations for agent AmberCove (single-agent scope)"
		if firstLineSingle != wantSingleHeader {
			t.Fatalf("single-agent count line = %q, want %q", firstLineSingle, wantSingleHeader)
		}
		if strings.Contains(outSingle, "GrayCompass") {
			t.Fatalf("single-agent output must not list other agent:\n%s", outSingle)
		}

		// All-agents scope (GrayCompass holds 1)
		resAll := LocksResult{
			Success:      true,
			Session:      "myproject",
			Agent:        "AmberCove",
			ProjectKey:   "/home/gabriel/repositories/repo",
			Reservations: []agentmail.FileReservation{resGray},
			Count:        1,
		}
		outAll := captureLocksStdout(t, func() error {
			return printLocksResult(resAll, true)
		})
		firstLineAll := strings.TrimSpace(strings.Split(outAll, "\n")[0])
		wantAllHeader := "File Reservations: 1 active across all agents (project scope, queried by AmberCove)"
		if firstLineAll != wantAllHeader {
			t.Fatalf("all-agents count line = %q, want %q", firstLineAll, wantAllHeader)
		}
		if !strings.Contains(outAll, "[#2] src/domain/mod.rs") || !strings.Contains(outAll, "Agent: GrayCompass") {
			t.Fatalf("all-agents output missing GrayCompass reservation:\n%s", outAll)
		}
	})

	t.Run("case4_both_agents_hold_reservations", func(t *testing.T) {
		// Single-agent scope (AmberCove queried, has 1)
		resSingle := LocksResult{
			Success:      true,
			Session:      "myproject",
			Agent:        "AmberCove",
			ProjectKey:   "/home/gabriel/repositories/repo",
			Reservations: []agentmail.FileReservation{resAmber},
			Count:        1,
		}
		outSingle := captureLocksStdout(t, func() error {
			return printLocksResult(resSingle, false)
		})
		firstLineSingle := strings.TrimSpace(strings.Split(outSingle, "\n")[0])
		wantSingleHeader := "File Reservations: 1 active for agent AmberCove (single-agent scope)"
		if firstLineSingle != wantSingleHeader {
			t.Fatalf("single-agent count line = %q, want %q", firstLineSingle, wantSingleHeader)
		}
		if !strings.Contains(outSingle, "[#1] src/auth.go") || !strings.Contains(outSingle, "Agent: AmberCove") {
			t.Fatalf("single-agent output missing AmberCove reservation:\n%s", outSingle)
		}
		if strings.Contains(outSingle, "GrayCompass") {
			t.Fatalf("single-agent output must not contain GrayCompass:\n%s", outSingle)
		}

		// All-agents scope (both AmberCove and GrayCompass queried, 2 total)
		resAll := LocksResult{
			Success:      true,
			Session:      "myproject",
			Agent:        "AmberCove",
			ProjectKey:   "/home/gabriel/repositories/repo",
			Reservations: []agentmail.FileReservation{resAmber, resGray},
			Count:        2,
		}
		outAll := captureLocksStdout(t, func() error {
			return printLocksResult(resAll, true)
		})
		firstLineAll := strings.TrimSpace(strings.Split(outAll, "\n")[0])
		wantAllHeader := "File Reservations: 2 active across all agents (project scope, queried by AmberCove)"
		if firstLineAll != wantAllHeader {
			t.Fatalf("all-agents count line = %q, want %q", firstLineAll, wantAllHeader)
		}
		if !strings.Contains(outAll, "[#1] src/auth.go") || !strings.Contains(outAll, "Agent: AmberCove") {
			t.Fatalf("all-agents output missing AmberCove reservation:\n%s", outAll)
		}
		if !strings.Contains(outAll, "[#2] src/domain/mod.rs") || !strings.Contains(outAll, "Agent: GrayCompass") {
			t.Fatalf("all-agents output missing GrayCompass reservation:\n%s", outAll)
		}
	})
}

func TestLocksList_Regression_ReproductionAmbiguousOutputNotProducible(t *testing.T) {
	res := LocksResult{
		Success:    true,
		Session:    "myproject",
		Agent:      "AmberCove",
		ProjectKey: "/home/gabriel/repositories/repo",
		Count:      0,
	}
	outSingle := captureLocksStdout(t, func() error {
		return printLocksResult(res, false)
	})
	if strings.Contains(outSingle, "No active file reservations (session scope)") {
		t.Fatalf("single-agent output reproduced ambiguous output:\n%s", outSingle)
	}

	outAll := captureLocksStdout(t, func() error {
		return printLocksResult(res, true)
	})
	if strings.Contains(outAll, "No active file reservations (session scope)") {
		t.Fatalf("all-agents output reproduced ambiguous output:\n%s", outAll)
	}
}

func TestLocksList_ContextPreserved(t *testing.T) {
	res := LocksResult{
		Success:    true,
		Session:    "test-session",
		Agent:      "GreenCastle",
		ProjectKey: "/path/to/stale/dir",
		Count:      0,
	}

	outSingle := captureLocksStdout(t, func() error {
		return printLocksResult(res, false)
	})
	if !strings.Contains(outSingle, "Agent: GreenCastle") {
		t.Fatalf("single-agent output must preserve Agent identity:\n%s", outSingle)
	}
	if !strings.Contains(outSingle, "Project: /path/to/stale/dir") {
		t.Fatalf("single-agent output must preserve Project path:\n%s", outSingle)
	}

	outAll := captureLocksStdout(t, func() error {
		return printLocksResult(res, true)
	})
	if !strings.Contains(outAll, "Agent: GreenCastle") {
		t.Fatalf("all-agents output must preserve Agent identity:\n%s", outAll)
	}
	if !strings.Contains(outAll, "Project: /path/to/stale/dir") {
		t.Fatalf("all-agents output must preserve Project path:\n%s", outAll)
	}
}
