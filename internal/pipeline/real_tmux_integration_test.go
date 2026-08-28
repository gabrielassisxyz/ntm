//go:build integration

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
	"github.com/Dicklesworthstone/ntm/tests/testutil/tmuxenv"
)

type realTmuxPipelineFixture struct {
	projectDir string
	session    string
	pane       tmux.Pane
}

func TestRealTmuxPipelineE2E_RunPersistAndLoad(t *testing.T) {
	fixture := newRealTmuxPipelineFixture(t)
	tracePath := filepath.Join(fixture.projectDir, "execution-order.log")
	templatePath := filepath.Join(fixture.projectDir, "dispatch.tpl")
	realTmuxWriteFixture(t, templatePath,
		`printf 'dispatch:<TOKEN>:${vars.suffix}\n' >> "${defaults.trace_file}"; printf 'PANE_%s:%s:%s\n' 'CAPTURE' '<TOKEN>' '${vars.suffix}'`)

	workflowPath := filepath.Join(fixture.projectDir, "run-persist.yaml")
	workflowYAML := fmt.Sprintf(`
schema_version: "2.0"
name: real-tmux-run-persist
description: Exercises the complete YAML-to-tmux-to-state path.
vars:
  input:
    type: string
    required: true
  suffix:
    type: string
    default: omega
defaults:
  trace_file: %s
settings:
  timeout: 12s
  log_dispatch: false
steps:
  - id: prepare
    command: |
      printf 'prepare:%%s\n' "$INPUT" >> "${defaults.trace_file}"
      printf '%%s' "$INPUT"
    args:
      INPUT: "${vars.input}"
    output_var: prepared

  - id: dispatch
    template: dispatch.tpl
    pane: %d
    params:
      TOKEN: "${steps.prepare.output}"
    after: prepare
    wait: time
    timeout: 1s
    output_var: pane_capture

  - id: verify_order
    command: |
      printf 'verify:%%s:%%s\n' "$PREPARED" "$SUFFIX" >> "${defaults.trace_file}"
      printf 'verified:%%s:%%s' "$PREPARED" "$SUFFIX"
    args:
      PREPARED: "${steps.prepare.output}"
      SUFFIX: "${vars.suffix}"
    after: dispatch
    output_var: verified
`, strconv.Quote(filepath.Base(tracePath)), fixture.pane.Index)
	realTmuxWriteFixture(t, workflowPath, workflowYAML)

	workflow, validation, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error = %v", err)
	}
	if !validation.Valid || len(validation.Errors) != 0 {
		t.Fatalf("LoadAndValidate() result = %+v, want valid workflow", validation)
	}
	if got := workflow.Steps[1].DependsOn; !reflect.DeepEqual(got, []string{"prepare"}) {
		t.Fatalf("dispatch dependencies = %v, want normalized [prepare]", got)
	}
	if got := workflow.Steps[2].DependsOn; !reflect.DeepEqual(got, []string{"dispatch"}) {
		t.Fatalf("verify_order dependencies = %v, want normalized [dispatch]", got)
	}

	runID := "real-tmux-run-persist"
	executor := newRealTmuxPipelineExecutor(fixture, workflowPath, runID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := executor.Run(ctx, workflow, map[string]interface{}{"input": "alpha"}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v; state = %+v", err, state)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("Run() status = %q, want %q", state.Status, StatusCompleted)
	}
	if state.RunID != runID || state.WorkflowFile != workflowPath || state.Session != fixture.session {
		t.Fatalf("Run() identity = run %q workflow %q session %q", state.RunID, state.WorkflowFile, state.Session)
	}
	if state.FinishedAt.IsZero() || state.LastCheckpointAt.IsZero() {
		t.Fatalf("Run() omitted terminal/checkpoint timestamps: %+v", state)
	}

	realTmuxAssertStep(t, state, "prepare", StatusCompleted)
	dispatch := realTmuxAssertStep(t, state, "dispatch", StatusCompleted)
	verify := realTmuxAssertStep(t, state, "verify_order", StatusCompleted)
	if dispatch.PaneUsed != fixture.pane.ID {
		t.Fatalf("dispatch pane = %q, want %q", dispatch.PaneUsed, fixture.pane.ID)
	}
	if !strings.Contains(dispatch.Output, "PANE_CAPTURE:alpha:omega") {
		t.Fatalf("dispatch output did not capture rendered pane response:\n%s", dispatch.Output)
	}
	if strings.Contains(dispatch.Output, "<TOKEN>") || strings.Contains(dispatch.Output, "${vars.suffix}") {
		t.Fatalf("dispatch output retained unresolved template text:\n%s", dispatch.Output)
	}
	if verify.Output != "verified:alpha:omega" {
		t.Fatalf("verify_order output = %q, want verified:alpha:omega", verify.Output)
	}
	if got := state.Variables["prepared"]; got != "alpha" {
		t.Fatalf("prepared output variable = %#v, want alpha", got)
	}
	if got, ok := state.Variables["pane_capture"].(string); !ok || !strings.Contains(got, "PANE_CAPTURE:alpha:omega") {
		t.Fatalf("pane_capture output variable = %#v, want captured pane marker", state.Variables["pane_capture"])
	}
	if got := state.Variables["verified"]; got != "verified:alpha:omega" {
		t.Fatalf("verified output variable = %#v, want verified:alpha:omega", got)
	}

	realTmuxAssertTrace(t, tracePath, []string{
		"prepare:alpha",
		"dispatch:alpha:omega",
		"verify:alpha:omega",
	})
	realTmuxWaitForOutput(t, fixture.pane.ID, "PANE_CAPTURE:alpha:omega", 3*time.Second)

	loaded, err := LoadState(fixture.projectDir, runID)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	if loaded.Status != StatusCompleted || loaded.RunID != runID || loaded.WorkflowID != workflow.Name {
		t.Fatalf("loaded state identity/status = run %q workflow %q status %q", loaded.RunID, loaded.WorkflowID, loaded.Status)
	}
	loadedDispatch := realTmuxAssertStep(t, loaded, "dispatch", StatusCompleted)
	if loadedDispatch.PaneUsed != fixture.pane.ID || !strings.Contains(loadedDispatch.Output, "PANE_CAPTURE:alpha:omega") {
		t.Fatalf("loaded dispatch result lost pane/output: %+v", loadedDispatch)
	}
	if got := loaded.Variables["verified"]; got != "verified:alpha:omega" {
		t.Fatalf("loaded verified variable = %#v, want verified:alpha:omega", got)
	}
	realTmuxAssertPersistedSchema(t, fixture.projectDir, runID)
}

func TestRealTmuxPipelineE2E_ResumeFailedTemplateDispatch(t *testing.T) {
	fixture := newRealTmuxPipelineFixture(t)
	tracePath := filepath.Join(fixture.projectDir, "resume-order.log")
	workflowPath := filepath.Join(fixture.projectDir, "resume.yaml")
	workflowYAML := fmt.Sprintf(`
schema_version: "2.0"
name: real-tmux-resume
defaults:
  trace_file: %s
settings:
  timeout: 12s
  log_dispatch: false
steps:
  - id: prepare_once
    command: |
      printf 'prepare_once\n' >> "${defaults.trace_file}"
      printf 'resume-seed'
    output_var: seed

  - id: recover_dispatch
    template: resume-dispatch.tpl
    pane: %d
    params:
      TOKEN: "${steps.prepare_once.output}"
    depends_on: [prepare_once]
    wait: time
    timeout: 1s
    output_var: resumed_capture
`, strconv.Quote(filepath.Base(tracePath)), fixture.pane.Index)
	realTmuxWriteFixture(t, workflowPath, workflowYAML)

	workflow, validation, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error = %v", err)
	}
	if !validation.Valid {
		t.Fatalf("LoadAndValidate() result = %+v, want valid workflow", validation)
	}

	runID := "real-tmux-resume"
	firstExecutor := newRealTmuxPipelineExecutor(fixture, workflowPath, runID)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 5*time.Second)
	failed, runErr := firstExecutor.Run(firstCtx, workflow, nil, nil)
	firstCancel()
	if runErr == nil {
		t.Fatal("Run() error = nil with missing template, want failure")
	}
	if failed.Status != StatusFailed {
		t.Fatalf("failed Run() status = %q, want %q", failed.Status, StatusFailed)
	}
	prepareBeforeResume := realTmuxAssertStep(t, failed, "prepare_once", StatusCompleted)
	if prepareBeforeResume.Output != "resume-seed" {
		t.Fatalf("prepare_once output = %q, want resume-seed", prepareBeforeResume.Output)
	}
	failedDispatch := realTmuxAssertStep(t, failed, "recover_dispatch", StatusFailed)
	if failedDispatch.Error == nil || failedDispatch.Error.Type != "template" ||
		!strings.Contains(failedDispatch.Error.Message, "template file not found") {
		t.Fatalf("recover_dispatch error = %+v, want missing-template error", failedDispatch.Error)
	}
	realTmuxAssertTrace(t, tracePath, []string{"prepare_once"})

	prior, err := LoadState(fixture.projectDir, runID)
	if err != nil {
		t.Fatalf("LoadState(failed run) error = %v", err)
	}
	if prior.Status != StatusFailed {
		t.Fatalf("persisted failed status = %q, want %q", prior.Status, StatusFailed)
	}
	realTmuxAssertPersistedSchema(t, fixture.projectDir, runID)

	templatePath := filepath.Join(fixture.projectDir, "resume-dispatch.tpl")
	realTmuxWriteFixture(t, templatePath,
		`printf 'resume_dispatch:<TOKEN>\n' >> "${defaults.trace_file}"; printf 'RESUME_%s:%s\n' 'CAPTURE' '<TOKEN>'`)

	resumeExecutor := newRealTmuxPipelineExecutor(fixture, workflowPath, "")
	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer resumeCancel()
	resumed, err := resumeExecutor.ResumeWithOptions(resumeCtx, workflow, prior, ResumeOptions{
		Mode:           ResumeModeRestartFailed,
		OnRosterChange: ResumeRosterAbort,
	}, nil)
	if err != nil {
		t.Fatalf("ResumeWithOptions() error = %v; state = %+v", err, resumed)
	}
	if resumed.Status != StatusCompleted || resumed.RunID != runID {
		t.Fatalf("resumed state = run %q status %q, want run %q completed", resumed.RunID, resumed.Status, runID)
	}
	prepareAfterResume := realTmuxAssertStep(t, resumed, "prepare_once", StatusCompleted)
	if !prepareAfterResume.StartedAt.Equal(prepareBeforeResume.StartedAt) {
		t.Fatalf("prepare_once reran during resume: before %s after %s", prepareBeforeResume.StartedAt, prepareAfterResume.StartedAt)
	}
	resumedDispatch := realTmuxAssertStep(t, resumed, "recover_dispatch", StatusCompleted)
	if resumedDispatch.PaneUsed != fixture.pane.ID || !strings.Contains(resumedDispatch.Output, "RESUME_CAPTURE:resume-seed") {
		t.Fatalf("resumed dispatch did not execute/capture on real pane: %+v", resumedDispatch)
	}
	if got := resumed.Variables["seed"]; got != "resume-seed" {
		t.Fatalf("resume did not rebuild retained output variable: %#v", got)
	}
	if got, ok := resumed.Variables["resumed_capture"].(string); !ok || !strings.Contains(got, "RESUME_CAPTURE:resume-seed") {
		t.Fatalf("resumed_capture = %#v, want captured resume marker", resumed.Variables["resumed_capture"])
	}
	realTmuxAssertTrace(t, tracePath, []string{
		"prepare_once",
		"resume_dispatch:resume-seed",
	})

	loaded, err := LoadState(fixture.projectDir, runID)
	if err != nil {
		t.Fatalf("LoadState(resumed run) error = %v", err)
	}
	loadedDispatch := realTmuxAssertStep(t, loaded, "recover_dispatch", StatusCompleted)
	if loaded.Status != StatusCompleted || !strings.Contains(loadedDispatch.Output, "RESUME_CAPTURE:resume-seed") {
		t.Fatalf("persisted resumed state lost terminal result: %+v", loaded)
	}
}

func TestRealTmuxPipelineE2E_NonZeroExitPersistsDiagnostics(t *testing.T) {
	fixture := newRealTmuxPipelineFixture(t)
	templatePath := filepath.Join(fixture.projectDir, "before-exit.tpl")
	realTmuxWriteFixture(t, templatePath, `printf 'BEFORE_%s_CAPTURE\n' 'EXIT'`)

	workflowPath := filepath.Join(fixture.projectDir, "exit-failure.yaml")
	workflowYAML := fmt.Sprintf(`
schema_version: "2.0"
name: real-tmux-exit-failure
settings:
  timeout: 12s
  log_dispatch: false
steps:
  - id: dispatch_before_failure
    template: before-exit.tpl
    pane: %d
    wait: time
    timeout: 1s

  - id: exit_23
    command: |
      printf 'stdout-before-exit\n'
      printf 'stderr-before-exit\n' >&2
      exit 23
    depends_on: [dispatch_before_failure]
`, fixture.pane.Index)
	realTmuxWriteFixture(t, workflowPath, workflowYAML)

	workflow, validation, err := LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error = %v", err)
	}
	if !validation.Valid {
		t.Fatalf("LoadAndValidate() result = %+v, want valid workflow", validation)
	}

	runID := "real-tmux-exit-failure"
	executor := newRealTmuxPipelineExecutor(fixture, workflowPath, runID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, runErr := executor.Run(ctx, workflow, nil, nil)
	if runErr == nil {
		t.Fatal("Run() error = nil for exit 23, want failure")
	}
	if !strings.Contains(runErr.Error(), "step exit_23 failed") || !strings.Contains(runErr.Error(), "exit_code=23") {
		t.Fatalf("Run() error = %q, want step and exit code", runErr)
	}
	if state.Status != StatusFailed {
		t.Fatalf("Run() status = %q, want %q", state.Status, StatusFailed)
	}
	dispatch := realTmuxAssertStep(t, state, "dispatch_before_failure", StatusCompleted)
	if dispatch.PaneUsed != fixture.pane.ID || !strings.Contains(dispatch.Output, "BEFORE_EXIT_CAPTURE") {
		t.Fatalf("pre-failure dispatch did not complete on real pane: %+v", dispatch)
	}
	failedStep := realTmuxAssertStep(t, state, "exit_23", StatusFailed)
	if failedStep.Error == nil {
		t.Fatal("exit_23 omitted StepError")
	}
	if failedStep.Error.Type != "exit" || !strings.Contains(failedStep.Error.Message, "exit_code=23") ||
		!strings.Contains(failedStep.Error.Details, "details=exit_code=23") {
		t.Fatalf("exit_23 error = %+v, want typed exit_code=23 diagnostics", failedStep.Error)
	}
	if !strings.Contains(failedStep.Output, "stdout-before-exit") || !strings.Contains(failedStep.Output, "stderr-before-exit") {
		t.Fatalf("exit_23 output lost stdout/stderr:\n%s", failedStep.Output)
	}

	loaded, err := LoadState(fixture.projectDir, runID)
	if err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}
	loadedFailure := realTmuxAssertStep(t, loaded, "exit_23", StatusFailed)
	if loaded.Status != StatusFailed || loadedFailure.Error == nil || loadedFailure.Error.Type != "exit" ||
		!strings.Contains(loadedFailure.Error.Details, "exit_code=23") {
		t.Fatalf("persisted failure diagnostics = %+v", loadedFailure)
	}
	if !strings.Contains(loadedFailure.Output, "stdout-before-exit") || !strings.Contains(loadedFailure.Output, "stderr-before-exit") {
		t.Fatalf("persisted failure lost command output: %q", loadedFailure.Output)
	}
	realTmuxAssertPersistedSchema(t, fixture.projectDir, runID)
}

func newRealTmuxPipelineFixture(t *testing.T) realTmuxPipelineFixture {
	t.Helper()
	testutil.RequireTmuxThrottled(t)
	longTempRoot := filepath.Join(t.TempDir(), strings.Repeat("long-tmux-socket-root-", 6))
	if err := os.MkdirAll(longTempRoot, 0o755); err != nil {
		t.Fatalf("create long ambient temp root: %v", err)
	}
	t.Setenv("TMPDIR", longTempRoot)
	t.Setenv("GOTMPDIR", longTempRoot)
	tmuxRoot := testutil.ShortTmuxTempDir(t)
	if strings.HasPrefix(tmuxRoot, longTempRoot+string(filepath.Separator)) {
		t.Fatalf("short tmux socket root %q remained beneath long ambient root %q", tmuxRoot, longTempRoot)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX_TMPDIR", tmuxRoot)

	projectDir := t.TempDir()
	session := fmt.Sprintf("ntm_pipe_e2e_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancelCleanup()
		// Explicit -S: this cleanup must not depend on TMUX_TMPDIR alone to
		// find the server it is allowed to kill.
		client := tmux.NewClientWithSocket(tmuxenv.DefaultSocketPath(tmuxRoot))
		if _, err := client.RunContext(cleanupCtx, "kill-server"); err != nil &&
			tmux.ClassifyCommandError(err).Kind != tmux.CommandErrorNoServer {
			t.Errorf("stop isolated tmux server: %v", err)
		}
	})
	createCtx, cancelCreate := context.WithTimeout(t.Context(), 8*time.Second)
	err := tmux.CreateSessionContext(createCtx, session, projectDir)
	cancelCreate()
	if err != nil {
		t.Fatalf("tmux.CreateSession(%q) error = %v", session, err)
	}

	windowCtx, cancelWindow := context.WithTimeout(t.Context(), 8*time.Second)
	firstWindow, err := tmux.GetFirstWindowContext(windowCtx, session)
	cancelWindow()
	if err != nil {
		t.Fatalf("tmux.GetFirstWindow(%q) error = %v", session, err)
	}
	target := fmt.Sprintf("%s:%d", session, firstWindow)
	paneID, err := tmux.DefaultClient.Run("split-window", "-t", target, "-c", projectDir,
		"-P", "-F", "#{pane_id}", "/bin/sh -i")
	if err != nil {
		t.Fatalf("tmux split-window for %q error = %v", session, err)
	}
	paneID = strings.TrimSpace(paneID)
	pane := realTmuxWaitForPane(t, session, paneID, 5*time.Second)
	if pane.Index <= 0 {
		t.Fatalf("split pane index = %d, pipeline explicit pane selectors require a positive index", pane.Index)
	}
	realTmuxWaitForShellPrompt(t, pane.ID, 5*time.Second)

	readyPath := filepath.Join(projectDir, ".pane-ready")
	sendCtx, cancelSend := context.WithTimeout(t.Context(), 8*time.Second)
	err = tmux.PasteKeysWithDelayContext(sendCtx, pane.ID, "printf 'ready' > .pane-ready", true, 500*time.Millisecond)
	cancelSend()
	if err != nil {
		t.Fatalf("tmux.PasteKeys(%q, readiness marker) error = %v", pane.ID, err)
	}
	realTmuxWaitForFileContent(t, readyPath, "ready", 5*time.Second)

	return realTmuxPipelineFixture{
		projectDir: projectDir,
		session:    session,
		pane:       pane,
	}
}

func newRealTmuxPipelineExecutor(fixture realTmuxPipelineFixture, workflowPath, runID string) *Executor {
	config := DefaultExecutorConfig(fixture.session)
	config.ProjectDir = fixture.projectDir
	config.WorkflowFile = workflowPath
	config.RunID = runID
	config.DefaultTimeout = 2 * time.Second
	config.GlobalTimeout = 12 * time.Second
	config.ProgressInterval = MinProgressInterval
	return NewExecutor(config)
}

func realTmuxWaitForPane(t *testing.T, session, paneID string, timeout time.Duration) tmux.Pane {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		panes, err := tmux.GetPanes(session)
		if err != nil {
			lastErr = err
		} else {
			for _, pane := range panes {
				if pane.ID == paneID {
					return pane
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane %q did not appear in session %q within %s (last error: %v)", paneID, session, timeout, lastErr)
	return tmux.Pane{}
}

func realTmuxWaitForShellPrompt(t *testing.T, paneID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastOutput string
	var lastErr error
	for time.Now().Before(deadline) {
		output, err := tmux.CapturePaneOutput(paneID, 20)
		if err != nil {
			lastErr = err
		} else {
			lastOutput = output
			if strings.TrimSpace(output) != "" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane %q did not render a shell prompt within %s (last error: %v)\nlast output:\n%s", paneID, timeout, lastErr, lastOutput)
}

func realTmuxWaitForOutput(t *testing.T, paneID, marker string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastOutput string
	var lastErr error
	for time.Now().Before(deadline) {
		output, err := tmux.CapturePaneOutput(paneID, 200)
		if err != nil {
			lastErr = err
		} else {
			lastOutput = output
			if strings.Contains(output, marker) {
				return output
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane %q did not contain %q within %s (last error: %v)\nlast output:\n%s", paneID, marker, timeout, lastErr, lastOutput)
	return ""
}

func realTmuxWaitForFileContent(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastContent string
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
		} else {
			lastContent = string(data)
			if lastContent == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %q did not equal %q within %s (last error: %v, content: %q)", path, want, timeout, lastErr, lastContent)
}

func realTmuxWriteFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%q) error = %v", path, err)
	}
}

func realTmuxAssertStep(t *testing.T, state *ExecutionState, stepID string, want ExecutionStatus) StepResult {
	t.Helper()
	if state == nil {
		t.Fatalf("state is nil while checking step %q", stepID)
	}
	result, ok := state.Steps[stepID]
	if !ok {
		t.Fatalf("state omitted step %q: %+v", stepID, state.Steps)
	}
	if result.Status != want {
		t.Fatalf("step %q status = %q, want %q; error = %+v", stepID, result.Status, want, result.Error)
	}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatalf("step %q omitted lifecycle timestamps: %+v", stepID, result)
	}
	return result
}

func realTmuxAssertTrace(t *testing.T, path string, want []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("trace = %v, want %v\nraw trace:\n%s", got, want, data)
	}
}

func realTmuxAssertPersistedSchema(t *testing.T, projectDir, runID string) {
	t.Helper()
	data, err := os.ReadFile(pipelineStatePath(projectDir, runID))
	if err != nil {
		t.Fatalf("read persisted state for %q: %v", runID, err)
	}
	var header struct {
		StateSchemaVersion int `json:"state_schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatalf("decode persisted state header for %q: %v", runID, err)
	}
	if header.StateSchemaVersion != PipelineStateSchemaVersion {
		t.Fatalf("state_schema_version = %d, want %d", header.StateSchemaVersion, PipelineStateSchemaVersion)
	}
}
