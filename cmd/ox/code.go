package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/codedb/index"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Search code in this repo",
	Long:  "Search git history and current code of this repo using Sourcegraph-style queries.",
}

var codeIndexCmd = &cobra.Command{
	Use:   "index [url]",
	Short: "Index a git repository (defaults to current repo)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository (specify a URL or run from a git repo)")
		}

		dataDir := paths.CodeDBDataDir(root)
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return fmt.Errorf("create codedb dir: %w", err)
		}

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		opts := index.IndexOptions{
			Progress: func(msg string) {
				fmt.Fprintf(os.Stderr, "  %s\n", msg)
			},
		}

		if len(args) > 0 {
			// Remote URL: clone/fetch and index
			url := args[0]
			fmt.Fprintf(os.Stderr, "Indexing %s...\n", url)
			if err := db.IndexRepo(context.Background(), url, opts); err != nil {
				return fmt.Errorf("index: %w", err)
			}
		} else {
			// No args: index current git repo in-place (including dirty files)
			fmt.Fprintf(os.Stderr, "Indexing local repo %s...\n", root)
			if err := db.IndexLocalRepo(context.Background(), root, opts); err != nil {
				return fmt.Errorf("index local: %w", err)
			}
		}

		fmt.Fprintf(os.Stderr, "Parsing symbols...\n")
		stats, err := db.ParseSymbols(context.Background(), func(msg string) {
			fmt.Fprintf(os.Stderr, "  %s\n", msg)
		})
		if err != nil {
			return fmt.Errorf("parse symbols: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Done. Parsed %d blobs, %d symbols\n",
			stats.BlobsParsed, stats.SymbolsExtracted)
		return nil
	},
}

var codeSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search indexed code using Sourcegraph-style queries",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		query := strings.Join(args, " ")
		dataDir := paths.CodeDBDataDir(root)

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		results, err := db.Search(context.Background(), query)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	},
}

var codeSQLCmd = &cobra.Command{
	Use:    "sql <query>",
	Short:  "Execute raw SQL against the CodeDB database",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		dataDir := paths.CodeDBDataDir(root)

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		cols, rows, err := db.RawSQL(args[0])
		if err != nil {
			return err
		}

		// Print as TSV
		fmt.Println(strings.Join(cols, "\t"))
		for _, row := range rows {
			fmt.Println(strings.Join(row, "\t"))
		}
		return nil
	},
}

func init() {
	codeCmd.AddCommand(codeIndexCmd)
	codeCmd.AddCommand(codeSearchCmd)
	codeCmd.AddCommand(codeSQLCmd)
	codeCmd.GroupID = "dev"
	rootCmd.AddCommand(codeCmd)
}
