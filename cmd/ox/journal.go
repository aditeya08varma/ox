package main

import "github.com/spf13/cobra"

var journalCmd = &cobra.Command{
	Use:   "journal",
	Short: "Inspect the team memory journal",
	Long:  "Read-only commands for listing and showing entries in memory/daily, memory/weekly, and memory/monthly under a team-context root.",
}

var journalListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List journal entries in a window",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var journalShowCmd = &cobra.Command{
	Use:           "show <id>...",
	Short:         "Show one or more journal entries by id",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var journalSinceCmd = &cobra.Command{
	Use:           "since <duration>",
	Short:         "Dump journal entries from the last <duration>",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	journalCmd.AddCommand(journalListCmd)
	journalCmd.AddCommand(journalShowCmd)
	journalCmd.AddCommand(journalSinceCmd)
	rootCmd.AddCommand(journalCmd)
}
