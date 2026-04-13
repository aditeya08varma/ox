package main

import (
	"errors"

	"github.com/spf13/cobra"
)

// errJournalNotImplemented is the Unit 1 placeholder response for every
// ox journal subcommand. Units 3–5 replace each RunE with a reader call.
var errJournalNotImplemented = errors.New("ox journal: not yet implemented")

var journalCmd = &cobra.Command{
	Use:    "journal",
	Short:  "Inspect the team memory journal",
	Long:   "Read-only commands for listing and showing entries in memory/daily, memory/weekly, and memory/monthly under a team-context root.",
	Hidden: true,
}

var journalListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List journal entries in a window",
	Hidden:        true,
	SilenceUsage:  true,
	SilenceErrors: true,
}

var journalShowCmd = &cobra.Command{
	Use:    "show <id>...",
	Short:  "Show one or more journal entries by id",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return errJournalNotImplemented
	},
}

var journalSinceCmd = &cobra.Command{
	Use:    "since <duration>",
	Short:  "Dump journal entries from the last <duration>",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return errJournalNotImplemented
	},
}

func init() {
	journalCmd.AddCommand(journalListCmd)
	journalCmd.AddCommand(journalShowCmd)
	journalCmd.AddCommand(journalSinceCmd)
	rootCmd.AddCommand(journalCmd)
}
