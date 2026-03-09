package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/sageox/ox/internal/codedb"
	"github.com/sageox/ox/internal/daemon"
	"github.com/sageox/ox/internal/paths"
	"github.com/sageox/ox/internal/repotools"
	"github.com/spf13/cobra"
)

var codeCmd = &cobra.Command{
	Use:   "code",
	Short: "Search code in this repo",
	Long:  "Search git history and current code of this repo using queries.",
}

var codeIndexCmd = &cobra.Command{
	Use:   "index [url]",
	Short: "Index a git repository (defaults to current repo)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// ensure daemon is running — indexing happens in the daemon
		if err := daemon.EnsureDaemon(); err != nil {
			return fmt.Errorf("daemon required for indexing: %w", err)
		}

		payload := daemon.CodeIndexPayload{}
		if len(args) > 0 {
			payload.URL = args[0]
			fmt.Fprintf(os.Stderr, "Indexing %s...\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Indexing local repo...\n")
		}

		client := daemon.NewClientWithTimeout(5 * time.Minute)
		result, err := client.CodeIndex(payload, func(stage string, percent *int, message string) {
			if message != "" {
				fmt.Fprintf(os.Stderr, "  %s\n", message)
			}
		})
		if err != nil {
			return fmt.Errorf("index: %w", err)
		}

		fmt.Fprintf(os.Stderr, "Done. Parsed %d blobs, %d symbols\n",
			result.BlobsParsed, result.SymbolsExtracted)
		return nil
	},
}

var codeSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search indexed code using queries",
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

		raw, _ := cmd.Flags().GetBool("raw")

		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")

		if raw {
			if err := enc.Encode(results); err != nil {
				return fmt.Errorf("encode: %w", err)
			}
		} else {
			resp := &combinedQueryResponse{CodeResults: results}
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("encode: %w", err)
			}
		}

		outputBytes := buf.Len()
		if _, err := buf.WriteTo(os.Stdout); err != nil {
			return err
		}

		agentID, _ := detectAgentContext()
		if agentID != "" {
			slog.Debug("code search context cost", "agent_id", agentID, "bytes", outputBytes)
			trackContextBytes(int64(outputBytes))
		}
		return nil
	},
}

// codeQueryCmd is a hidden alias for codeSearchCmd — agents try "query" as a search verb
var codeQueryCmd = &cobra.Command{
	Use:    "query <query>",
	Short:  codeSearchCmd.Short,
	Hidden: true,
	Args:   cobra.MinimumNArgs(1),
	RunE:   codeSearchCmd.RunE,
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

var codeStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show code index statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		root, err := repotools.FindRepoRoot(repotools.VCSGit)
		if err != nil {
			return fmt.Errorf("not in a git repository")
		}

		dataDir := paths.CodeDBDataDir(root)
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			fmt.Println("No code index found. Run 'ox code index' to create one.")
			return nil
		}

		db, err := codedb.Open(dataDir)
		if err != nil {
			return fmt.Errorf("open codedb: %w", err)
		}
		defer db.Close()

		var totalCommits, totalBlobs, totalSymbols int
		_ = db.Store().QueryRow("SELECT COUNT(*) FROM commits").Scan(&totalCommits)
		_ = db.Store().QueryRow("SELECT COUNT(*) FROM blobs").Scan(&totalBlobs)
		_ = db.Store().QueryRow("SELECT COUNT(*) FROM symbols").Scan(&totalSymbols)

		// per-repo breakdown
		type repoRow struct {
			name    string
			path    string
			commits int
			blobs   int
		}
		var repos []repoRow
		rows, err := db.Store().Query(`
			SELECT r.name, r.path, COUNT(DISTINCT c.id), COUNT(DISTINCT fr.blob_id)
			FROM repos r
			LEFT JOIN commits c ON c.repo_id = r.id
			LEFT JOIN file_revs fr ON fr.commit_id = c.id
			GROUP BY r.id ORDER BY r.name`)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var r repoRow
				if rows.Scan(&r.name, &r.path, &r.commits, &r.blobs) == nil {
					repos = append(repos, r)
				}
			}
		}

		raw, _ := cmd.Flags().GetBool("json")
		if raw {
			type jsonStats struct {
				Commits int `json:"commits"`
				Blobs   int `json:"blobs"`
				Symbols int `json:"symbols"`
				Repos   []struct {
					Name    string `json:"name"`
					Path    string `json:"path"`
					Commits int    `json:"commits"`
					Blobs   int    `json:"blobs"`
				} `json:"repos"`
				DataDir string `json:"data_dir"`
			}
			out := jsonStats{
				Commits: totalCommits,
				Blobs:   totalBlobs,
				Symbols: totalSymbols,
				DataDir: dataDir,
			}
			for _, r := range repos {
				out.Repos = append(out.Repos, struct {
					Name    string `json:"name"`
					Path    string `json:"path"`
					Commits int    `json:"commits"`
					Blobs   int    `json:"blobs"`
				}{Name: r.name, Path: r.path, Commits: r.commits, Blobs: r.blobs})
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		// human-readable output
		fmt.Printf("Code Index: %s\n\n", dataDir)

		if len(repos) > 1 {
			for _, r := range repos {
				fmt.Printf("  %s\n", r.name)
				fmt.Printf("    Path:    %s\n", r.path)
				fmt.Printf("    Commits: %d\n", r.commits)
				fmt.Printf("    Blobs:   %d\n", r.blobs)
				fmt.Println()
			}
			fmt.Println("  Totals")
		} else if len(repos) == 1 {
			fmt.Printf("  Repo: %s\n", repos[0].name)
			fmt.Printf("  Path: %s\n\n", repos[0].path)
		}

		fmt.Printf("  Commits: %d\n", totalCommits)
		fmt.Printf("  Blobs:   %d\n", totalBlobs)
		fmt.Printf("  Symbols: %d\n", totalSymbols)

		return nil
	},
}

func init() {
	codeSearchCmd.Flags().Bool("raw", false, "output raw results array instead of combined response")
	_ = codeSearchCmd.Flags().MarkHidden("raw")

	codeQueryCmd.Flags().Bool("raw", false, "output raw results array instead of combined response")
	_ = codeQueryCmd.Flags().MarkHidden("raw")

	codeStatsCmd.Flags().Bool("json", false, "output as JSON")

	codeCmd.AddCommand(codeIndexCmd)
	codeCmd.AddCommand(codeSearchCmd)
	codeCmd.AddCommand(codeQueryCmd)
	codeCmd.AddCommand(codeSQLCmd)
	codeCmd.AddCommand(codeStatsCmd)
	codeCmd.GroupID = "dev"
	rootCmd.AddCommand(codeCmd)
}
