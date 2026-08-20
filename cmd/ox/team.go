package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sageox/ox/internal/api"
	"github.com/sageox/ox/internal/auth"
	"github.com/sageox/ox/internal/cli"
	"github.com/sageox/ox/internal/config"
	"github.com/sageox/ox/internal/endpoint"
	"github.com/spf13/cobra"
)

// memberLister is the seam `ox team members` fetches through, so the
// fetch-and-render core is unit-testable against a fake without a network.
// *api.RepoClient satisfies it.
type memberLister interface {
	ListTeamRoster(ctx context.Context, teamRef string) (*api.TeamRosterResponse, error)
}

// teamCmd is the single canonical home for everything about teams. `ox teams`
// (plural) is a back-compat alias; a bare `ox team`/`ox teams` lists the teams
// you belong to, and `ox team <verb>` inspects or acts on one team.
//
// The RunE is a strict dispatcher, not a catch-all: a bare invocation lists,
// but an unrecognized token errors instead of silently swallowing the argument.
// That closes the old trap where `ox teams members` printed the whole team list
// and ignored `members`.
var teamCmd = &cobra.Command{
	Use:     "team",
	Aliases: []string{"teams"},
	Short:   "Work with your teams and coworkers",
	Long: `Work with your teams and coworkers.

A bare 'ox team' lists the teams you belong to. Each team owns a team context —
its permanent conversation store of recordings, discussions, sessions, and
shared memory. That is not a knowledge bubble ('ox kb list' lists those; see ox
ADR-028 for the distinction).

Commands:
  list       List the teams you belong to
  members    List a team's coworkers (humans and AI coworkers)
  show       Show one team's details
  context    Output a team's context for AI planning
  open       Open a team's dashboard in the browser
  invite     Invite people to a team by email`,
	Args: cobra.ArbitraryArgs,
	RunE: runTeamDispatch,
}

// runTeamDispatch makes a bare `ox team` list, while an unknown subcommand token
// fails loudly rather than falling through to the lister — a valid subcommand is
// routed by cobra before RunE is ever reached.
func runTeamDispatch(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return runTeams(cmd, args)
	}
	return fmt.Errorf("unknown subcommand %q for %q\nRun 'ox team --help' to see available commands", args[0], cmd.CommandPath())
}

var teamListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the teams you belong to",
	Long: `List the teams you belong to and their team contexts.

A team context is the team's permanent conversation store — recordings,
discussions, sessions, and shared memory. It is not a knowledge bubble
(` + "`ox kb list`" + ` lists those; see ox ADR-028 for the distinction).`,
	RunE: runTeams,
}

var teamMembersCmd = &cobra.Command{
	Use:     "members",
	Aliases: []string{"coworkers"},
	Short:   "List the team's coworkers (humans and AI coworkers)",
	Long: `List the coworkers on your team — humans and AI coworkers alike.

Shows each coworker's display name, type, role, and the handles they're known
by (e.g. a GitHub login). The roster answers "who is this?" and renders the
identity fields the server exposes.

The roster is feature-flag gated on the server. When the flag is off (or the
server is older), the command reports that the capability is unavailable rather
than failing.

Example:
  ox team members
  ox team members --team acme
  ox team members --json`,
	RunE: runTeamMembers,
}

var teamShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show one team's details",
	Long: `Show a single team: its identity, where its context lives on disk, how
recently it synced, and how many coworkers it has.

With no --team, shows this repo's team.

Example:
  ox team show
  ox team show --team acme
  ox team show --json`,
	RunE: runTeamShow,
}

var teamContextCmd = &cobra.Command{
	Use:     "context [slug]",
	Aliases: []string{"ctx"},
	Short:   "Output a team's context for AI planning",
	Long: `Output a team's discussions and distilled context for AI planning.

Without arguments: outputs this repo's team context. With a team slug: outputs
that team's context. This is the same context surfaced to AI coworkers by
'ox agent team-ctx'.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runAgentTeamCtx,
}

var teamOpenCmd = &cobra.Command{
	Use:   "open",
	Short: "Open a team's dashboard in the browser",
	Long: `Open the SageOx web dashboard for a team.

With no --team, opens this repo's team.`,
	RunE: runTeamOpen,
}

func init() {
	teamListCmd.Flags().Bool("json", false, "Output as JSON")

	teamMembersCmd.Flags().String("team", "", "Team ID or slug to use (defaults to this repo's team)")
	teamMembersCmd.Flags().Bool("json", false, "Output as JSON")

	teamShowCmd.Flags().String("team", "", "Team ID or slug to show (defaults to this repo's team)")
	teamShowCmd.Flags().Bool("json", false, "Output as JSON")

	teamOpenCmd.Flags().String("team", "", "Team ID or slug to open (defaults to this repo's team)")
	teamOpenCmd.Flags().String("endpoint", "", "SageOx endpoint URL (for multi-endpoint repos)")

	// A bare `ox team`/`ox teams` lists, so the parent needs the list flag too.
	teamCmd.Flags().Bool("json", false, "Output as JSON")

	teamCmd.AddCommand(teamListCmd)
	teamCmd.AddCommand(teamMembersCmd)
	teamCmd.AddCommand(teamShowCmd)
	teamCmd.AddCommand(teamContextCmd)
	teamCmd.AddCommand(teamOpenCmd)
	teamCmd.AddCommand(inviteCmd) // re-homed: canonical `ox team invite` (invite.go)

	teamCmd.GroupID = "teams"
	rootCmd.AddCommand(teamCmd)
}

// resolveTeamRef returns the team ref to query and a human display label.
// --team wins (resolved locally, else passed through for the server to resolve
// as a slug); otherwise the repo's configured team is used.
func resolveTeamRef(cmd *cobra.Command, projectRoot string) (ref, label string, err error) {
	teamFlag, _ := cmd.Flags().GetString("team")
	if teamFlag != "" {
		if t := resolveTeamByQuery(projectRoot, teamFlag); t != nil {
			tc := t.toConfigTeamContext()
			return tc.TeamID, teamLabelFor(tc), nil
		}
		// not found locally — let the server resolve the slug/id
		return teamFlag, teamFlag, nil
	}
	tc := config.FindRepoTeamContext(projectRoot)
	if tc == nil {
		return "", "", fmt.Errorf("no team configured; run 'ox init' or pass --team")
	}
	return tc.TeamID, teamLabelFor(tc), nil
}

func teamLabelFor(tc *config.TeamContext) string {
	switch {
	case tc.TeamName != "":
		return tc.TeamName
	case tc.Slug != "":
		return tc.Slug
	default:
		return tc.TeamID
	}
}

func runTeamMembers(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}
	ep := endpoint.GetForProject(projectRoot)

	teamRef, label, err := resolveTeamRef(cmd, projectRoot)
	if err != nil {
		return err
	}

	// The roster is a team-scoped read: require a valid token up front and fail
	// fast with a friendly hint, matching `ox query` / `ox invite`.
	// EnsureValidTokenForEndpoint also proactively refreshes a near-expired token
	// (GetTokenForEndpoint would have sent it stale and eaten a 401).
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return fmt.Errorf("not authenticated — run 'ox login' first")
	}
	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(token.AccessToken)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return fetchAndRenderRoster(ctx, cmd.OutOrStdout(), client, teamRef, label, jsonMode)
}

// fetchAndRenderRoster is the testable core: it fetches the roster and renders
// it, degrading gracefully (exit 0) when the server reports the feature is
// unavailable. Auth / forbidden / version errors are surfaced as real errors.
func fetchAndRenderRoster(ctx context.Context, w io.Writer, lister memberLister, teamRef, label string, jsonMode bool) error {
	resp, err := lister.ListTeamRoster(ctx, teamRef)
	if err != nil {
		// Degrade gracefully (exit 0) when the roster can't be served: either the
		// feature/route doesn't exist (404) or the server is down/unreachable
		// (transport failure or 5xx). The roster is informational — a missing
		// feature or a reachability blip shouldn't be a hard failure. Auth,
		// forbidden, and version errors still surface: those need user action.
		if errors.Is(err, api.ErrTeamRosterUnsupported) || errors.Is(err, api.ErrTeamRosterUnavailable) {
			if jsonMode {
				return writeRosterJSON(w, nil, false)
			}
			msg := "Team roster is unavailable (feature not enabled on this server, or team not found)."
			if errors.Is(err, api.ErrTeamRosterUnavailable) {
				msg = "Team roster is unavailable right now — couldn't reach the server."
			}
			fmt.Fprintf(w, "%s %s\n", cli.Styles.Info.Render("ℹ"), msg)
			return nil
		}
		// ErrUnauthorized / ErrForbidden / ErrVersionUnsupported / other → real error
		return err
	}

	if jsonMode {
		return writeRosterJSON(w, resp, true)
	}

	renderRosterTable(w, resp, label)
	return nil
}

// writeRosterJSON emits ONE stable envelope for both success and graceful
// degrade so a JSON consumer (often an AI coworker) can rely on the shape:
// `members` is always an array (never null) and `available` is always present.
func writeRosterJSON(w io.Writer, resp *api.TeamRosterResponse, available bool) error {
	env := struct {
		TeamID    string           `json:"team_id"`
		Members   []api.TeamMember `json:"members"`
		Total     int              `json:"total"`
		Available bool             `json:"available"`
	}{Members: []api.TeamMember{}, Available: available}
	if resp != nil {
		env.TeamID = resp.TeamID
		env.Total = resp.Total
		if resp.Members != nil {
			env.Members = resp.Members
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func renderRosterTable(w io.Writer, resp *api.TeamRosterResponse, label string) {
	label = cli.SanitizeTerminalText(label)
	if resp == nil || len(resp.Members) == 0 {
		fmt.Fprintf(w, "%s No coworkers found in %s.\n", cli.Styles.Info.Render("ℹ"), label)
		return
	}

	header := fmt.Sprintf("Team Coworkers (%s)", label)
	fmt.Fprintln(w, cli.StyleGroupHeader.Render(header))
	fmt.Fprintln(w, cli.StyleDim.Render(strings.Repeat("─", len(header))))

	// aligned columns: name, type, role. Aliases (variable length) trail the row.
	rows := make([][]string, 0, len(resp.Members))
	aliases := make([]string, 0, len(resp.Members))
	for _, m := range resp.Members {
		// Every field is untrusted server text: sanitize each before render so
		// no column (m.Type was the gap) can carry ANSI/control bytes to the TTY.
		name := cli.SanitizeTerminalText(m.Name)
		if name == "" {
			name = cli.SanitizeTerminalText(m.PrincipalID)
		}
		typ := cli.SanitizeTerminalText(m.Type)
		if typ == "" {
			typ = "human"
		}
		rows = append(rows, []string{name, typ, cli.SanitizeTerminalText(m.Role)})
		aliases = append(aliases, cli.SanitizeTerminalText(strings.Join(m.Aliases, ", ")))
	}
	widths := cli.ColumnWidths(rows, []int{16, 5, 6}, []int{32, 6, 10})

	for i, row := range rows {
		name := fmt.Sprintf("%-*s", widths[0], row[0])
		typ := fmt.Sprintf("%-*s", widths[1], row[1])
		role := fmt.Sprintf("%-*s", widths[2], row[2])
		line := fmt.Sprintf("  %s  %s  %s",
			cli.StyleCalloutBold.Render(name),
			cli.StyleDim.Render(typ),
			cli.StyleDim.Render(role))
		if aliases[i] != "" {
			line += "  " + cli.StyleDim.Render(aliases[i])
		}
		fmt.Fprintln(w, line)
	}
}

// teamCard is the resolved set of fields `ox team show` renders for one team.
type teamCard struct {
	teamID   string
	name     string
	slug     string
	primary  bool
	path     string
	lastSync string
}

// resolveTeamCard picks the team `ox team show` describes: --team when given
// (resolved locally, else passed through as a bare ref), otherwise this repo's
// primary team.
func resolveTeamCard(projectRoot, teamFlag string) (teamCard, error) {
	if teamFlag != "" {
		if t := resolveTeamByQuery(projectRoot, teamFlag); t != nil {
			return teamCardFromEnriched(*t), nil
		}
		// Not synced locally — show what the flag gives us; the server owns the
		// rest and `ox team members`/`open` still resolve it as a slug/id.
		return teamCard{teamID: teamFlag, name: teamFlag, slug: teamFlag}, nil
	}
	for _, t := range discoverAllTeams(projectRoot) {
		if t.Primary {
			return teamCardFromEnriched(t), nil
		}
	}
	if tc := config.FindRepoTeamContext(projectRoot); tc != nil {
		return teamCard{teamID: tc.TeamID, name: tc.TeamName, slug: tc.Slug, path: tc.Path, primary: true}, nil
	}
	return teamCard{}, fmt.Errorf("no team configured; run 'ox init' or pass --team")
}

func teamCardFromEnriched(t enrichedTeam) teamCard {
	sync := "never"
	if !t.LastSync.IsZero() {
		sync = formatAge(time.Since(t.LastSync))
	}
	return teamCard{
		teamID:   t.TeamID,
		name:     t.Name,
		slug:     t.Slug,
		primary:  t.Primary,
		path:     t.Path,
		lastSync: sync,
	}
}

func runTeamShow(cmd *cobra.Command, args []string) error {
	jsonMode, _ := cmd.Flags().GetBool("json")

	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("could not find project root: %w", err)
	}
	ep := endpoint.GetForProject(projectRoot)

	teamFlag, _ := cmd.Flags().GetString("team")
	card, err := resolveTeamCard(projectRoot, teamFlag)
	if err != nil {
		return err
	}

	// Coworker count is a best-effort roster read: a missing feature or a
	// reachability blip leaves the count unknown rather than failing the card.
	count, countKnown := teamCoworkerCount(projectRoot, ep, card.teamID)

	dashboard := ""
	if ep != "" && card.teamID != "" {
		dashboard = fmt.Sprintf("%s/team/%s", strings.TrimRight(ep, "/"), card.teamID)
	}

	if jsonMode {
		return writeTeamShowJSON(cmd.OutOrStdout(), card, count, countKnown, dashboard)
	}
	renderTeamShow(cmd.OutOrStdout(), card, count, countKnown, dashboard)
	return nil
}

// teamCoworkerCount fetches the roster size, degrading to (0, false) whenever
// the roster can't be served — no auth, feature off, server unreachable.
func teamCoworkerCount(projectRoot, ep, teamRef string) (int, bool) {
	if teamRef == "" {
		return 0, false
	}
	token, err := auth.EnsureValidTokenForEndpoint(ep, 300)
	if err != nil || token == nil || token.AccessToken == "" {
		return 0, false
	}
	client := api.NewRepoClientForProject(projectRoot).WithAuthToken(token.AccessToken)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.ListTeamRoster(ctx, teamRef)
	if err != nil || resp == nil {
		return 0, false
	}
	if resp.Total > 0 {
		return resp.Total, true
	}
	return len(resp.Members), true
}

func writeTeamShowJSON(w io.Writer, c teamCard, count int, countKnown bool, dashboard string) error {
	env := struct {
		TeamID             string `json:"team_id"`
		Name               string `json:"name"`
		Slug               string `json:"slug,omitempty"`
		Primary            bool   `json:"primary"`
		CoworkerCount      int    `json:"coworker_count"`
		CoworkersAvailable bool   `json:"coworkers_available"`
		ContextPath        string `json:"context_path,omitempty"`
		LastSync           string `json:"last_sync,omitempty"`
		DashboardURL       string `json:"dashboard_url,omitempty"`
	}{
		TeamID:             c.teamID,
		Name:               c.name,
		Slug:               c.slug,
		Primary:            c.primary,
		CoworkerCount:      count,
		CoworkersAvailable: countKnown,
		ContextPath:        c.path,
		LastSync:           c.lastSync,
		DashboardURL:       dashboard,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

func renderTeamShow(w io.Writer, c teamCard, count int, countKnown bool, dashboard string) {
	name := cli.SanitizeTerminalText(c.name)
	if name == "" {
		name = cli.SanitizeTerminalText(c.teamID)
	}

	fmt.Fprintln(w, teamsHeaderStyle.Render(name))
	fmt.Fprintln(w, teamsHeaderStyle.Render(strings.Repeat("─", len(name))))

	kv := func(label, val string) {
		if val == "" {
			return
		}
		fmt.Fprintf(w, "  %s  %s\n", teamsLabelStyle.Render(fmt.Sprintf("%-10s", label)), val)
	}

	teamValue := teamsNameStyle.Render(name)
	if c.primary {
		teamValue += " " + teamsPrimaryBadge.Render("(this repo)")
	}
	kv("Team", teamValue)
	kv("ID", teamsValueStyle.Render(cli.SanitizeTerminalText(c.teamID)))
	kv("Slug", teamsValueStyle.Render(cli.SanitizeTerminalText(c.slug)))

	coworkers := "unavailable"
	if countKnown {
		coworkers = fmt.Sprintf("%d", count)
	}
	kv("Coworkers", teamsValueStyle.Render(coworkers))

	sync := c.lastSync
	if sync == "" {
		sync = "never"
	}
	kv("Sync", teamsValueStyle.Render(sync))
	kv("Path", teamsPathStyle.Render(c.path))
	kv("Dashboard", teamsPathStyle.Render(dashboard))

	fmt.Fprintf(w, "\n  %s %s\n", teamsHintStyle.Render("Coworkers:"), teamsCommandStyle.Render("ox team members"))
}

func runTeamOpen(cmd *cobra.Command, args []string) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("not in a SageOx project: %w", err)
	}
	cfg, err := config.LoadProjectConfig(projectRoot)
	if err != nil {
		return fmt.Errorf("failed to load project config: %w", err)
	}

	teamFlag, _ := cmd.Flags().GetString("team")
	teamID := cfg.TeamID
	if teamFlag != "" {
		if t := resolveTeamByQuery(projectRoot, teamFlag); t != nil {
			teamID = t.TeamID
		} else {
			teamID = teamFlag
		}
	}
	if teamID == "" {
		fmt.Fprintln(cmd.OutOrStdout(), "No team found. Run 'ox init' first to register this repository.")
		return nil
	}

	flagEndpoint, _ := cmd.Flags().GetString("endpoint")
	endpointURL, err := resolveEndpoint(projectRoot, cfg.GetEndpoint(), endpoint.NormalizeEndpoint(flagEndpoint))
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/team/%s", strings.TrimRight(endpointURL, "/"), teamID)
	fmt.Fprintf(cmd.OutOrStdout(), "Opening %s\n", url)
	if err := cli.OpenInBrowser(url); err != nil {
		if errors.Is(err, cli.ErrHeadless) {
			fmt.Fprintf(cmd.OutOrStdout(), "Visit: %s\n", url)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s Could not open browser. Visit: %s\n", cli.StyleWarning.Render("!"), url)
	}
	return nil
}
