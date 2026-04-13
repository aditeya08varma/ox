package main

import "github.com/spf13/cobra"

// distillHistoryCmd is the `ox distill history` command group: read-only
// access to distilled daily/weekly/monthly summary entries. With no
// subcommand, it behaves like `ox distill history list` for discoverability.
var distillHistoryCmd = &cobra.Command{
	Use:           "history",
	Short:         "Inspect distilled daily/weekly/monthly summary entries",
	Long:          "Read-only commands for listing, showing, and dumping distilled summary entries in memory/daily, memory/weekly, and memory/monthly under a team-context root. With no subcommand, behaves like `ox distill history list`.",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runDistillHistoryList,
}

var distillHistoryListCmd = &cobra.Command{
	Use:           "list",
	Short:         "List distilled summary entries in a window",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var distillHistoryShowCmd = &cobra.Command{
	Use:           "show <id>...",
	Short:         "Show one or more distilled summary entries by id",
	SilenceUsage:  true,
	SilenceErrors: true,
}

var distillHistorySinceCmd = &cobra.Command{
	Use:           "since <duration>",
	Short:         "Dump distilled summary entries from the last `<duration>`",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	distillHistoryCmd.AddCommand(distillHistoryListCmd)
	distillHistoryCmd.AddCommand(distillHistoryShowCmd)
	distillHistoryCmd.AddCommand(distillHistorySinceCmd)
	distillCmd.AddCommand(distillHistoryCmd)
}
