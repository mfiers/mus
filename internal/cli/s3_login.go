package cli

// `mus s3 login` — interactive wizard for S3 credentials.
//
// Asks for the four pieces mus needs (endpoint, region, access key,
// secret key), VERIFIES them by listing buckets through the SDK,
// and only persists to the secret backend on a successful ping.
// That way a typo'd access key never silently overwrites known-good
// credentials.

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"codeberg.org/mfiers/mus/internal/s3client"
	"codeberg.org/mfiers/mus/internal/secret"
)

// Secret-store key names. The endpoint / region / access key aren't
// strictly secrets, but storing them alongside the secret keeps
// configuration in one place.
const (
	secretS3Endpoint  = "s3_endpoint"
	secretS3Region    = "s3_region"
	secretS3AccessKey = "s3_access_key"
	secretS3SecretKey = "s3_secret_key"
)

func newS3LoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "interactive: store S3 endpoint + region + access key + secret key",
		Long: "Interactive wizard. Prompts for the four connection parameters,\n" +
			"verifies them by listing buckets, and persists to the secret\n" +
			"backend only if the verification succeeds. Re-run to update\n" +
			"any field — old values are shown as the default so you can\n" +
			"keep them by pressing enter.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runS3Login(cmd)
		},
	}
}

func runS3Login(cmd *cobra.Command) error {
	store, err := openSecrets()
	if err != nil {
		return err
	}
	stdin := cmd.InOrStdin()
	stdout := cmd.OutOrStdout()

	fmt.Fprintln(stdout, "Configure S3 deposit (press Enter to keep an existing value)")
	fmt.Fprintln(stdout)

	endpoint, err := promptWithDefault(stdin, stdout,
		"Endpoint URL (e.g. https://s3.eu-central-1.amazonaws.com or https://minio.lab.example/): ",
		existing(store, secretS3Endpoint))
	if err != nil {
		return err
	}
	region, err := promptWithDefault(stdin, stdout,
		"Region (e.g. us-east-1, eu-central-1, or 'auto' for many MinIO setups): ",
		existing(store, secretS3Region))
	if err != nil {
		return err
	}
	accessKey, err := promptWithDefault(stdin, stdout,
		"Access key ID: ",
		existing(store, secretS3AccessKey))
	if err != nil {
		return err
	}
	// Secret key — never echo the existing value back; the masked
	// reader in readSecretLine handles the typing.
	fmt.Fprint(stdout, "Secret key")
	if existing(store, secretS3SecretKey) != "" {
		fmt.Fprint(stdout, " (leave empty to keep the stored value)")
	}
	fmt.Fprint(stdout, ": ")
	secretKey, err := readSecretLine(stdin, stdout, "")
	if err != nil {
		return err
	}
	if secretKey == "" {
		secretKey = existing(store, secretS3SecretKey)
	}

	cfg := s3client.Config{
		Endpoint:  strings.TrimSpace(endpoint),
		Region:    strings.TrimSpace(region),
		AccessKey: strings.TrimSpace(accessKey),
		SecretKey: secretKey,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	fmt.Fprintln(stdout, "→ verifying by listing buckets…")
	client, err := s3client.New(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	buckets, err := client.Ping(cmd.Context())
	if err != nil {
		return fmt.Errorf(
			"verification failed: %v\n"+
				"  Common causes: bad access/secret key, wrong region for the\n"+
				"  endpoint, endpoint URL typo, network reachability.\n"+
				"  Nothing was persisted — re-run `mus s3 login` to retry.", err)
	}
	fmt.Fprintf(stdout, "✓ authenticated, %d bucket(s) visible\n", len(buckets))
	if len(buckets) <= 8 && len(buckets) > 0 {
		for _, b := range buckets {
			fmt.Fprintf(stdout, "    %s\n", b)
		}
	} else if len(buckets) > 8 {
		fmt.Fprintf(stdout, "    %s\n    %s\n    ...(and %d more)\n",
			buckets[0], buckets[1], len(buckets)-2)
	}

	if err := persistS3Config(store, cfg); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "✓ saved to mus secret store")
	fmt.Fprintln(stdout, "  You can now: `mus s3 check`  /  `mus s3 upload -m \"...\" FILE`")
	return nil
}

// persistS3Config writes all four fields to the secret store. Atomic-
// per-key (the underlying backend doesn't support transactions); on
// partial failure the user sees an error and re-runs login.
func persistS3Config(store secret.Store, cfg s3client.Config) error {
	pairs := [][2]string{
		{secretS3Endpoint, cfg.Endpoint},
		{secretS3Region, cfg.Region},
		{secretS3AccessKey, cfg.AccessKey},
		{secretS3SecretKey, cfg.SecretKey},
	}
	for _, p := range pairs {
		if err := store.Set(p[0], p[1]); err != nil {
			return fmt.Errorf("save %s: %w", p[0], err)
		}
	}
	return nil
}

// loadS3Config reads the four fields from the secret store. Returns
// a friendly error pointing at `mus s3 login` when anything is
// missing.
func loadS3Config(store secret.Store) (s3client.Config, error) {
	cfg := s3client.Config{
		Endpoint:  existing(store, secretS3Endpoint),
		Region:    existing(store, secretS3Region),
		AccessKey: existing(store, secretS3AccessKey),
		SecretKey: existing(store, secretS3SecretKey),
	}
	return cfg, cfg.Validate()
}

// existing reads a secret, returning "" when the key is unset (or
// any other error — login UX treats missing as "no default").
func existing(store secret.Store, name string) string {
	v, err := store.Get(name)
	if err != nil {
		return ""
	}
	return v
}

// promptWithDefault writes a prompt to stdout and reads a single
// line from stdin. Empty input returns the supplied default. Used
// by the login wizard so re-runs can keep existing values via just
// pressing enter.
func promptWithDefault(stdin io.Reader, stdout io.Writer, prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(stdout, "%s[%s] ", prompt, def)
	} else {
		fmt.Fprint(stdout, prompt)
	}
	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return def, nil
	}
	return line, nil
}

