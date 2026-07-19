package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/sageox/ox/internal/agentinstance"
	"github.com/sageox/ox/internal/agenttask"
)

// TestAgentTasksDoneCancel_AcceptResultReasonFlags is the regression test for
// the cobra flag-scoping bug: `ox agent <id> tasks done <task-id> --result
// "<note>"` and `ox agent <id> tasks cancel <task-id> --reason "<note>"` both
// failed with "unknown flag: --result" / "unknown flag: --reason" — even
// though parseTaskIDAndNote already knew how to read them out of a raw
// []string. The actual bug was that agentCmd never registered these flags,
// so cobra's own ParseFlags rejected the whole invocation before
// runAgentDispatcher ever ran.
//
// Failure prevented: agent_tasks_test.go's existing coverage calls
// runAgentTasks(...) directly with an already-split []string — that bypasses
// agentCmd's cobra flag parser entirely, so it passed even with the live bug
// in production and would not catch a regression either. This test instead
// calls agentCmd.ParseFlags directly on the production agentCmd singleton —
// the exact FlagSet agent.go's init() registers — so it fails with "unknown
// flag" if the registration in init() is ever reverted or removed.
//
// ParseFlags (not Execute) is used deliberately: cobra's ExecuteC() runs
// "regardless of what command Execute is called on, run on Root only"
// whenever the command has a parent, and agentCmd is parented under rootCmd
// by production init() — so agentCmd.Execute() would silently redirect to
// rootCmd.Execute() using rootCmd's own (unset) args, not the args this test
// sets. ParseFlags has no such redirect: it parses directly against
// agentCmd's own merged flag set, which is exactly where the bug lived.
func TestAgentTasksDoneCancel_AcceptResultReasonFlags(t *testing.T) {
	tests := []struct {
		name       string
		verb       string
		flag       string
		note       string
		wantStatus agenttask.Status
	}{
		{name: "done with --result", verb: "done", flag: "--result", note: "note-for-result", wantStatus: agenttask.StatusCompleted},
		{name: "cancel with --reason", verb: "cancel", flag: "--reason", note: "note-for-reason", wantStatus: agenttask.StatusCanceled},
		{name: "done with --note (alternate spelling)", verb: "done", flag: "--note", note: "note-for-note", wantStatus: agenttask.StatusCompleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := setupTaskProject(t)
			const agentID = "OxFlag"
			registerTestInstance(t, root, agentID)

			if _, err := agenttask.Enqueue(root, &agenttask.Task{Title: "chore"}); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			store, err := agenttask.NewStore(root)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer func() { _ = store.Close() }()
			claimed, err := store.Claim(agenttask.ClaimOptions{AgentID: agentID, AgentType: "claude", PID: 1})
			if err != nil || claimed == nil {
				t.Fatalf("claim: %v (claimed=%v)", err, claimed)
			}

			// agentCmd's parsed flag values are shared package-level state
			// (mirrors the established pattern in agent_session_abort_test.go's
			// setForceFlag). Start from a clean slate and restore it after,
			// so this subtest's values can't leak into a sibling subtest or
			// any other test sharing the process.
			resetAgentTaskNoteFlags(t)

			args := []string{agentID, "tasks", tt.verb, claimed.ID, tt.flag, tt.note}
			if err := agentCmd.ParseFlags(args); err != nil {
				t.Fatalf("agentCmd.ParseFlags(%q) failed — this IS the reported bug "+
					"(cobra rejecting %s as an unknown flag on agentCmd): %v", args, tt.flag, err)
			}
			positional := agentCmd.Flags().Args()

			var stdout bytes.Buffer
			agentCmd.SetOut(&stdout)
			t.Cleanup(func() { agentCmd.SetOut(nil) })

			if err := runAgentDispatcher(agentCmd, positional); err != nil {
				t.Fatalf("runAgentDispatcher(%q): %v", positional, err)
			}

			got, err := store.Get(claimed.ID)
			if err != nil {
				t.Fatalf("reload task: %v", err)
			}
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Result != tt.note {
				t.Fatalf("note = %q, want %q (got %+v)", got.Result, tt.note, got)
			}
		})
	}
}

// resetAgentTaskNoteFlags clears agentCmd's result/reason/note flags to their
// zero value and schedules the same reset for after the test. Guards against
// state leaking between subtests/tests that share the agentCmd singleton.
func resetAgentTaskNoteFlags(t *testing.T) {
	t.Helper()
	reset := func() {
		for _, name := range []string{"result", "reason", "note"} {
			_ = agentCmd.PersistentFlags().Set(name, "")
		}
	}
	reset()
	t.Cleanup(reset)
}

// registerTestInstance creates a real agentinstance.Instance in the on-disk
// store so resolveInstance(agentID) — called by runWithAgentID before any
// subcommand dispatch — succeeds during the test.
func registerTestInstance(t *testing.T, projectRoot, agentID string) {
	t.Helper()
	store, err := agentinstance.NewStoreForUser(projectRoot, getUserSlug())
	if err != nil {
		t.Fatalf("open instance store: %v", err)
	}
	inst := &agentinstance.Instance{
		AgentID:         agentID,
		ServerSessionID: "oxsid_test0000000000000000000000000",
		AgentType:       "claude",
		CreatedAt:       time.Now(),
		ExpiresAt:       time.Now().Add(24 * time.Hour),
	}
	if err := store.Add(inst); err != nil {
		t.Fatalf("add instance: %v", err)
	}
}
