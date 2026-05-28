package cli

// `mus eln` — eLabJournal / eLabNext integration.
//
// `mus eln login` walks a new user from "no API key" to "verified, stored":
// prompts for the token, parses the `host;key` form the ELN web UI emits,
// hits GET /users/getCurrentUserInfo to confirm the token works, then stores
// `eln_url` + `eln_apikey` via the existing secret store.
//
// `mus eln tag-folder -x ID` calls the ELN API to fetch project/study/
// experiment names for the experiment ID and writes all of those into the
// local .env so subsequent `mus tag` / `mus irods upload` calls stamp each
// sidecar with the full ELN context.
//
// `mus eln update` re-fetches the same metadata when a project/study/
// experiment has been renamed on the server.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"codeberg.org/atrxia/mus/internal/config"
	"codeberg.org/atrxia/mus/internal/eln"
	"codeberg.org/atrxia/mus/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newELNCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eln",
		Short: "eLabJournal / eLabNext integration",
	}
	cmd.AddCommand(
		newELNLoginCmd(),
		newELNTagCmd(),
		newELNTagFolderCmd(), // legacy spelling — emits a deprecation notice
		newELNUpdateCmd(),
		newELNWhoamiCmd(),
	)
	return cmd
}

// openELN reads the stored URL + API key and returns a ready-to-use client.
// Returns a helpful error if either is missing — pointing the user at
// `mus eln login`.
func openELN() (*eln.Client, error) {
	store, err := secret.Open()
	if err != nil {
		return nil, err
	}
	baseURL, err := store.Get("eln_url")
	if err != nil {
		return nil, fmt.Errorf("eln_url not set — run `mus eln login` first")
	}
	key, err := store.Get("eln_apikey")
	if err != nil {
		return nil, fmt.Errorf("eln_apikey not set — run `mus eln login` first")
	}
	return eln.New(baseURL, key), nil
}

// --- login wizard -----------------------------------------------------------

func newELNLoginCmd() *cobra.Command {
	var nonInteractive bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "interactive: obtain + verify + store an ELN API token",
		Long: "Walks you through generating an API token in the ELN web UI, then\n" +
			"verifies the token by calling GET /users/getCurrentUserInfo before\n" +
			"storing it in your OS keyring (or age-encrypted file on hosts with\n" +
			"no keyring).\n\n" +
			"Accepts the full host;key token the web UI emits — splits on `;`\n" +
			"automatically and infers the base URL from the host part.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNLogin(cmd.InOrStdin(), cmd.OutOrStdout(), nonInteractive)
		},
	}
	cmd.Flags().BoolVar(&nonInteractive, "no-prompt", false,
		"do not show the wizard instructions; just read the token from stdin")
	return cmd
}

const elnLoginInstructions = `mus ELN login

Step 1 — Generate an API token in the ELN web UI
  1. Browse to https://vib.elabjournal.com (or your tenant).
  2. Open the account dropdown (Persian-green header) → Apps & Connections.
  3. Click "Manage authentication" under 'eLab Mobile App' or 'eLabSync'.
  4. Set "Used for" = eLabSync, give it a description ("mus on $HOSTNAME"),
     then click Generate.
  5. The token looks like  vib.elabjournal.com;n12a3b...n9n  —
     paste it whole; mus will split it.

Works with SAML/SSO and 2FA accounts (no password handoff to mus).

Step 2 — Paste the token below.`

func runELNLogin(stdin io.Reader, stdout io.Writer, noPrompt bool) error {
	if !noPrompt {
		fmt.Fprintln(stdout, elnLoginInstructions)
	}

	raw, err := readSecretLine(stdin, stdout, "Token: ")
	if err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("empty token")
	}

	host, key, err := parseELNToken(raw)
	if err != nil {
		return err
	}

	baseURL := fmt.Sprintf("https://%s/api/v1", host)
	fmt.Fprintf(stdout, "\nVerifying against %s …\n", baseURL)

	client := eln.New(baseURL, key)
	user, err := client.CurrentUser()
	if err != nil {
		return fmt.Errorf("token did not verify: %w\n  Re-check the token; the key is the part AFTER the ';'", err)
	}

	store, err := secret.Open()
	if err != nil {
		return fmt.Errorf("open secret store: %w", err)
	}
	if err := store.Set("eln_url", baseURL); err != nil {
		return fmt.Errorf("store eln_url: %w", err)
	}
	if err := store.Set("eln_apikey", key); err != nil {
		return fmt.Errorf("store eln_apikey: %w", err)
	}

	fmt.Fprintf(stdout, "✓ authenticated as %s (user ID %d)\n", user.FullName(), user.UserID)
	fmt.Fprintf(stdout, "  eln_url     stored in %s\n", store.Backend())
	fmt.Fprintf(stdout, "  eln_apikey  stored in %s\n", store.Backend())
	fmt.Fprintln(stdout, "\nNext: mus eln tag-folder -x <experiment_id>  (in a project directory)")
	return nil
}

// parseELNToken accepts either the `host;key` form that the ELN web UI emits
// or a bare key. Returns (host, key). For a bare key the host defaults to
// vib.elabjournal.com (the current institutional tenant) — users on other
// tenants should paste the full host;key form so the URL is correct.
func parseELNToken(raw string) (host string, key string, err error) {
	raw = strings.TrimSpace(raw)
	// Strip an accidental https://… prefix users might paste from the
	// instructions block.
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")

	// A leading ';' means an explicit (empty) host part — treat as malformed
	// rather than falling through to the bare-key default, so users learn
	// that the syntax is host;key.
	if idx := strings.IndexByte(raw, ';'); idx >= 0 {
		host = strings.TrimSpace(raw[:idx])
		key = strings.TrimSpace(raw[idx+1:])
	} else {
		host = "vib.elabjournal.com"
		key = raw
	}
	if host == "" {
		return "", "", errors.New("token host part is empty")
	}
	if key == "" {
		return "", "", errors.New("token key part is empty (use the part AFTER the ';')")
	}
	// Guard against the user pasting a full URL as the host.
	if u, perr := url.Parse("https://" + host); perr != nil || u.Host == "" {
		return "", "", fmt.Errorf("bad host %q", host)
	}
	return host, key, nil
}

// readSecretLine reads a single line of input, hiding it from the terminal
// when stdin is a TTY. Falls back to a plain ReadString when stdin is piped
// (scripts, CI).
func readSecretLine(stdin io.Reader, stdout io.Writer, prompt string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(stdout, prompt)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stdout)
		return string(raw), err
	}
	// No TTY — read a line straight off stdin without echoing prompt as
	// the caller probably piped a token in.
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// --- tag + tag-folder (deprecated) ------------------------------------------

// newELNTagCmd is the current command: `mus eln tag <expid>`. Positional arg,
// no -x flag — shorter to type, no flag-base-10 footgun.
func newELNTagCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "tag EXPERIMENT_ID",
		Short: "link the current folder to an ELN experiment (writes .env)",
		Long: "Fetches project / study / experiment names from the ELN API and writes\n" +
			"eln_experiment_id / eln_experiment_name / eln_study_id / eln_study_name /\n" +
			"eln_project_id / eln_project_name into the current folder's .env.\n\n" +
			"Refuses to overwrite an existing linkage; use `mus eln update` to refresh\n" +
			"after a server-side rename, or --force to relink to a different experiment.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNTag(cmd, args[0], force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing linkage in this folder")
	return cmd
}

// newELNTagFolderCmd is the LEGACY spelling: `mus eln tag-folder -x EXPID`.
// Still works; prints a one-line deprecation reminder pointing at `mus eln
// tag EXPID`. Remove once nobody is using it.
func newELNTagFolderCmd() *cobra.Command {
	// The -x flag accepts a string (not Int64) because cobra's Int64Var
	// parses with strconv.ParseInt(s, 0, 64) — base-0 means a leading "0"
	// is interpreted as octal, which breaks on eLabJournal long-form IDs
	// that copy-paste with leading zeros. parseELNExperimentID handles it.
	var expIDStr string
	var force bool
	cmd := &cobra.Command{
		Use:        "tag-folder",
		Short:      "(deprecated) use `mus eln tag EXPERIMENT_ID` instead",
		Deprecated: "use `mus eln tag EXPERIMENT_ID` (positional arg, no -x flag).",
		Hidden:     false, // still listed so users see the deprecation note in --help
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"note: `mus eln tag-folder -x ID` is deprecated; use `mus eln tag ID` instead")
			return runELNTag(cmd, expIDStr, force)
		},
	}
	cmd.Flags().StringVarP(&expIDStr, "experiment-id", "x", "",
		"ELN experiment ID (digits; leading zeros tolerated)")
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing linkage in this folder")
	_ = cmd.MarkFlagRequired("experiment-id")
	return cmd
}

// runELNTag is the shared body of `eln tag` and `eln tag-folder`.
func runELNTag(cmd *cobra.Command, expIDRaw string, force bool) error {
	expID, err := parseELNExperimentID(expIDRaw)
	if err != nil {
		return err
	}
	dir, err := workingDir(cmd)
	if err != nil {
		return err
	}
	// Idempotency guard: refuse if a different experiment is already linked.
	localEnv, _ := config.LoadLocal(dir)
	if existing := localEnv.String("eln_experiment_id"); existing != "" && !force {
		if existing == strconv.FormatInt(expID, 10) {
			fmt.Fprintf(cmd.OutOrStdout(),
				"folder is already linked to experiment %s — running `mus eln update` to refresh\n", existing)
			return runELNUpdate(cmd.OutOrStdout())
		}
		return fmt.Errorf("folder already linked to experiment %s.\n"+
			"  Use `mus eln update` to refresh the existing link, or\n"+
			"  `mus eln tag %d --force` to relink",
			existing, expID)
	}

	client, err := openELN()
	if err != nil {
		return err
	}
	info, err := client.ExpInfo(expID)
	if err != nil {
		return fmt.Errorf("fetch experiment %d: %w", expID, err)
	}
	kv := experimentToKV(info)
	for k, v := range kv {
		fmt.Fprintf(cmd.OutOrStdout(), "%-25s : %s\n", k, v)
	}
	return config.Save(dir, kv)
}

func newELNUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "refresh ELN metadata in local .env from the server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runELNUpdate(cmd.OutOrStdout())
		},
	}
}

func runELNUpdate(stdout io.Writer) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	env, err := config.Load(dir)
	if err != nil {
		return err
	}
	idStr := env.String("eln_experiment_id")
	if idStr == "" {
		return errors.New("no eln_experiment_id in .env cascade — run `mus eln tag-folder -x ID` first")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid eln_experiment_id %q: %w", idStr, err)
	}
	client, err := openELN()
	if err != nil {
		return err
	}
	info, err := client.ExpInfo(id)
	if err != nil {
		return fmt.Errorf("fetch experiment %d: %w", id, err)
	}
	kv := experimentToKV(info)
	for k, v := range kv {
		fmt.Fprintf(stdout, "%-25s : %s\n", k, v)
	}
	return config.Save(dir, kv)
}

// experimentToKV converts an ExperimentInfo to the flat key/value map mus
// writes into .env. Collaborators are dropped on purpose — they belong in a
// list-keyed config, not as scalars.
func experimentToKV(info *eln.ExperimentInfo) map[string]string {
	return map[string]string{
		"eln_experiment_id":   strconv.FormatInt(info.ExperimentID, 10),
		"eln_experiment_name": info.ExperimentName,
		"eln_study_id":        strconv.FormatInt(info.StudyID, 10),
		"eln_study_name":      info.StudyName,
		"eln_project_id":      strconv.FormatInt(info.ProjectID, 10),
		"eln_project_name":    info.ProjectName,
	}
}

// --- whoami -----------------------------------------------------------------

func newELNWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "print the user the stored token authenticates as",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := openELN()
			if err != nil {
				return err
			}
			user, err := client.CurrentUser()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "user_id   : %d\nname      : %s\nemail     : %s\n",
				user.UserID, user.FullName(), user.Email)
			return nil
		},
	}
}

// --- shared helpers ---------------------------------------------------------

// parseELNExperimentID accepts a digit string and returns an int64. Leading
// zeros are stripped — eLabJournal's web UI occasionally copy-pastes IDs with
// extra leading zeros. Returns an error for anything that isn't a non-empty
// run of decimal digits.
func parseELNExperimentID(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("-x/--experiment-id is required")
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("experiment ID %q contains non-digit %q", raw, string(r))
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("experiment ID %q: %w", raw, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("experiment ID must be positive, got %d", id)
	}
	return id, nil
}
