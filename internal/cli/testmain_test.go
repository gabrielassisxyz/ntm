package cli

import (
	"fmt"
	"os"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
)

const (
	testAgentCatCommandTemplate    = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`
	testAgentBinCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}/bin/cat`

	// testAgentReadyCatCommandTemplate is testAgentCatCommandTemplate plus both
	// agents' composer markers, for tests that dispatch a real prompt through the
	// fixture and need ComposerReadyForDelivery to see the pane as ready rather
	// than "initializing" (bd-sr2sg). Printing both markers keeps one template
	// usable for either agent type. Do not swap this in for every consumer of
	// testAgentCatCommandTemplate: a pane that always looks ready can no longer be
	// made to look busy, which is what TestRunReassignment_TargetBusyWithoutForce
	// asserts against.
	testAgentReadyCatCommandTemplate = `{{if .Model}}: {{shellQuote .Model}} >/dev/null && {{end}}printf '❯\n›\n' && /bin/cat`
)

func newTmuxIntegrationTestConfig(projectsBase string) *config.Config {
	testCfg := config.Default()
	testCfg.ProjectsBase = projectsBase

	// Generic tmux integration tests should stay focused on pane/session behavior
	// instead of shelling out to optional memory and coordination helpers.
	testCfg.AgentMail.Enabled = false
	testCfg.CASS.Context.Enabled = false
	testCfg.SessionRecovery.Enabled = false
	testCfg.SessionRecovery.AutoInjectOnSpawn = false
	testCfg.SessionRecovery.IncludeAgentMail = false
	testCfg.SessionRecovery.IncludeBeadsContext = false
	testCfg.SessionRecovery.IncludeCMMemories = false
	testCfg.GeminiSetup.AutoSelectProModel = false

	return testCfg
}

func TestMain(m *testing.M) {
	cleanupTmux, err := testutil.IsolateTmuxTestProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI tmux tests: %v\n", err)
		os.Exit(1)
	}

	// CLI tests exercise git-touching paths (hooks install via `ntm init`,
	// worktrees, ...); never let them read or write the developer's real git
	// configuration (#225).
	cleanupGit, err := testutil.IsolateGitConfigProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI git config: %v\n", err)
		os.Exit(1)
	}

	// CLI tests also persist Agent Mail session agents and registries; never
	// let them write into the developer's real ~/.config/ntm/sessions/.
	cleanupConfig, err := testutil.IsolateUserConfigProcess()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate CLI config: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	if err := cleanupConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI config: %v\n", err)
		code = 1
	}
	if err := cleanupGit(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI git config: %v\n", err)
		code = 1
	}
	if err := cleanupTmux(); err != nil {
		fmt.Fprintf(os.Stderr, "clean up isolated CLI tmux: %v\n", err)
		code = 1
	}

	os.Exit(code)
}
