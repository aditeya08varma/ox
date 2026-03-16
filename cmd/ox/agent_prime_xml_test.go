package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestOutputAgentPrimeXML_UserNotices(t *testing.T) {
	tests := []struct {
		name                   string
		userNotices            []UserNotice
		wantUserNoticesBlock   bool
		wantNoticeTypes        []string
		wantNoticeMessages     []string
		wantNotInActions       []string // strings that should NOT appear in <immediate-actions>
		wantInActions          []string // strings that should appear in <immediate-actions>
	}{
		{
			name:                 "no notices omits user-notices block",
			userNotices:          nil,
			wantUserNoticesBlock: false,
		},
		{
			name: "upgrade notice in user-notices",
			userNotices: []UserNotice{
				{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox"},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"upgrade"},
			wantNoticeMessages:   []string{"v0.5.0 -> v0.5.1"},
		},
		{
			name: "restart notice in user-notices",
			userNotices: []UserNotice{
				{Type: "restart", Message: "SageOx hooks were just installed. Exit this session and start a new one so the hooks take effect."},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"restart"},
			wantNoticeMessages:   []string{"hooks were just installed"},
		},
		{
			name: "multiple notices",
			userNotices: []UserNotice{
				{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available"},
				{Type: "restart", Message: "Restart required"},
				{Type: "support", Message: "Agent not supported"},
			},
			wantUserNoticesBlock: true,
			wantNoticeTypes:      []string{"upgrade", "restart", "support"},
			wantNoticeMessages:   []string{"v0.5.0", "Restart", "not supported"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&buf)

			output := agentPrimeOutput{
				AgentID:     "test-agent",
				Status:      "fresh",
				UserNotices: tt.userNotices,
			}

			if err := outputAgentPrimeXML(cmd, output); err != nil {
				t.Fatalf("outputAgentPrimeXML() error = %v", err)
			}

			xml := buf.String()

			hasBlock := strings.Contains(xml, "<user-notices")
			if hasBlock != tt.wantUserNoticesBlock {
				t.Errorf("<user-notices> present = %v, want %v", hasBlock, tt.wantUserNoticesBlock)
			}

			if tt.wantUserNoticesBlock {
				if !strings.Contains(xml, `hint="Show each notice to the user"`) {
					t.Error("missing hint attribute on <user-notices>")
				}
			}

			for _, typ := range tt.wantNoticeTypes {
				wantAttr := `type="` + typ + `"`
				if !strings.Contains(xml, wantAttr) {
					t.Errorf("missing notice type=%q in output", typ)
				}
			}

			for _, msg := range tt.wantNoticeMessages {
				if !strings.Contains(xml, msg) {
					t.Errorf("missing notice message containing %q", msg)
				}
			}

			for _, s := range tt.wantNotInActions {
				// extract immediate-actions block
				start := strings.Index(xml, "<immediate-actions>")
				end := strings.Index(xml, "</immediate-actions>")
				if start >= 0 && end >= 0 {
					actionsBlock := xml[start:end]
					if strings.Contains(actionsBlock, s) {
						t.Errorf("%q should not be in <immediate-actions>, but found it", s)
					}
				}
			}

			for _, s := range tt.wantInActions {
				start := strings.Index(xml, "<immediate-actions>")
				end := strings.Index(xml, "</immediate-actions>")
				if start < 0 || end < 0 {
					t.Errorf("expected <immediate-actions> block for %q check", s)
				} else {
					actionsBlock := xml[start:end]
					if !strings.Contains(actionsBlock, s) {
						t.Errorf("%q should be in <immediate-actions>, but not found", s)
					}
				}
			}
		})
	}
}

func TestOutputAgentPrimeXML_DoctorStaysInActions(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:          "test-agent",
		Status:           "fresh",
		NeedsDoctorAgent: true,
		DoctorHint:       "Run 'ox agent doctor' to finalize incomplete sessions",
	}

	if err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// doctor hint must be in immediate-actions, not user-notices
	start := strings.Index(xml, "<immediate-actions>")
	end := strings.Index(xml, "</immediate-actions>")
	if start < 0 || end < 0 {
		t.Fatal("expected <immediate-actions> block")
	}
	actionsBlock := xml[start:end]
	if !strings.Contains(actionsBlock, "ox agent doctor") {
		t.Error("doctor hint not found in <immediate-actions>")
	}

	// should NOT have user-notices
	if strings.Contains(xml, "<user-notices") {
		t.Error("doctor-only output should not have <user-notices>")
	}
}

func TestOutputAgentPrimeXML_UpgradeNotInActions(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)

	output := agentPrimeOutput{
		AgentID:         "test-agent",
		Status:          "fresh",
		UpdateAvailable: true,
		UpdateHint:      "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox",
		UserNotices: []UserNotice{
			{Type: "upgrade", Message: "v0.5.0 -> v0.5.1 available. Run: brew upgrade sageox"},
		},
	}

	if err := outputAgentPrimeXML(cmd, output); err != nil {
		t.Fatalf("outputAgentPrimeXML() error = %v", err)
	}

	xml := buf.String()

	// upgrade must be in user-notices
	if !strings.Contains(xml, `<notice type="upgrade">`) {
		t.Error("upgrade notice not in <user-notices>")
	}

	// upgrade must NOT be in immediate-actions
	start := strings.Index(xml, "<immediate-actions>")
	end := strings.Index(xml, "</immediate-actions>")
	if start >= 0 && end >= 0 {
		actionsBlock := xml[start:end]
		if strings.Contains(actionsBlock, "brew upgrade") {
			t.Error("upgrade hint should not be in <immediate-actions>")
		}
	}
}

