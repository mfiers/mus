package cli

// `mus irods scan` walks an iRODS collection (via `iron tree --json`) and
// caches every object's path, size, and server-side checksum in a local
// SQLite database. Subsequent `mus irods scan show` / `find` calls read from
// the cache — no network round-trip required.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"codeberg.org/mfiers/mus/internal/irodscache"
	"codeberg.org/mfiers/mus/internal/iron"
	"github.com/spf13/cobra"
)

func newIRODSScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "snapshot iRODS collections to a local cache for fast browsing",
	}
	cmd.AddCommand(
		newIRODSScanRunCmd(),
		newIRODSScanListCmd(),
		newIRODSScanShowCmd(),
		newIRODSScanFindCmd(),
		newIRODSScanRmCmd(),
	)
	return cmd
}

// `mus irods scan run PATH` (also the default `mus irods scan PATH`)
func newIRODSScanRunCmd() *cobra.Command {
	var maxAge time.Duration
	var refresh bool
	cmd := &cobra.Command{
		Use:   "run PATH",
		Short: "scan an iRODS collection and cache the result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIRODSScan(cmd.Context(), args[0], maxAge, refresh)
		},
	}
	cmd.Flags().DurationVar(&maxAge, "max-age", 24*time.Hour,
		"reuse cached scan if newer than this (0 = always rescan)")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "force re-scan regardless of cache age")
	return cmd
}

func runIRODSScan(ctx context.Context, rootPath string, maxAge time.Duration, refresh bool) error {
	cache, err := irodscache.Open("")
	if err != nil {
		return err
	}
	defer cache.Close()

	if !refresh {
		if existing, _ := cache.GetScan(rootPath); existing != nil {
			age := time.Since(existing.ScannedAt)
			if maxAge == 0 || age < maxAge {
				printScanSummary(existing, age)
				return nil
			}
			fmt.Fprintf(os.Stderr, "cache for %s is %s old; re-scanning\n",
				rootPath, age.Round(time.Second))
		}
	}

	client, err := iron.New()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "scanning %s (this can take a while for large trees)…\n", rootPath)
	start := time.Now()
	rawJSON, err := client.TreeJSON(ctx, rootPath, "name", "size", "checksum", "date", "creator")
	if err != nil {
		return fmt.Errorf("iron tree: %w", err)
	}
	objects, err := parseIronTreeJSON(strings.NewReader(rawJSON), rootPath)
	if err != nil {
		return fmt.Errorf("parse iron output: %w", err)
	}
	info, err := cache.Replace(rootPath, objects)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "scanned in %s\n", time.Since(start).Round(time.Millisecond))
	printScanSummary(&info, 0)
	return nil
}

func printScanSummary(info *irodscache.ScanInfo, age time.Duration) {
	ageStr := "just now"
	if age > time.Second {
		ageStr = fmt.Sprintf("%s ago", age.Round(time.Second))
	}
	fmt.Printf("root:       %s\n", info.RootPath)
	fmt.Printf("scanned:    %s (%s)\n", info.ScannedAt.Local().Format(time.RFC3339), ageStr)
	fmt.Printf("objects:    %d\n", info.ObjectCount)
	fmt.Printf("size_total: %s\n", humanBytes(info.BytesTotal))
}

// parseIronTreeJSON consumes one JSON object per line. IRON's tree output
// only includes a `checksum` field for data objects; we use that to set
// IsObject true/false.
//
// rootPath is provided for context but not used to filter — IRON's tree
// includes the root itself plus every descendant.
func parseIronTreeJSON(r io.Reader, _ string) ([]irodscache.Object, error) {
	scanner := bufio.NewScanner(r)
	// Allow long lines (paths or names can be long).
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []irodscache.Object
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw struct {
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			Checksum string `json:"checksum"`
			Modified string `json:"modified"`
			Creator  string `json:"creator"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNum, err)
		}
		if raw.Name == "" {
			continue
		}
		obj := irodscache.Object{
			Path:     raw.Name,
			Size:     raw.Size,
			Checksum: raw.Checksum,
			Creator:  raw.Creator,
			IsObject: raw.Checksum != "", // IRON only emits checksum for data objects
		}
		if raw.Modified != "" {
			if t, err := time.Parse(time.RFC3339, raw.Modified); err == nil {
				obj.Modified = t.UTC()
			}
		}
		out = append(out, obj)
	}
	return out, scanner.Err()
}

func newIRODSScanListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "list cached iRODS scans",
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, err := irodscache.Open("")
			if err != nil {
				return err
			}
			defer cache.Close()
			scans, err := cache.ListScans()
			if err != nil {
				return err
			}
			if len(scans) == 0 {
				fmt.Println("(no scans cached yet — run `mus irods scan run /path/on/irods`)")
				return nil
			}
			fmt.Printf("%-12s  %-8s  %s\n", "OBJECTS", "AGE", "PATH")
			for _, s := range scans {
				age := time.Since(s.ScannedAt).Round(time.Second)
				fmt.Printf("%-12d  %-8s  %s\n", s.ObjectCount, age, s.RootPath)
			}
			return nil
		},
	}
}

func newIRODSScanShowCmd() *cobra.Command {
	var prefix string
	var dataOnly, colsOnly bool
	var checksums bool
	cmd := &cobra.Command{
		Use:   "show PATH",
		Short: "print every cached entry under a scan root",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, err := irodscache.Open("")
			if err != nil {
				return err
			}
			defer cache.Close()
			if info, _ := cache.GetScan(args[0]); info == nil {
				return fmt.Errorf("no cached scan for %s — run `mus irods scan run %s` first",
					args[0], args[0])
			}
			objs, err := cache.ListObjects(args[0], irodscache.ListObjectsOpts{
				OnlyDataObjects: dataOnly,
				OnlyCollections: colsOnly,
				PathPrefix:      prefix,
			})
			if err != nil {
				return err
			}
			for _, o := range objs {
				kind := "d"
				if o.IsObject {
					kind = "f"
				}
				if checksums && o.IsObject {
					fmt.Printf("%s  %14d  %s  %s\n", kind, o.Size, shortChecksum(o.Checksum), o.Path)
				} else {
					fmt.Printf("%s  %14d  %s\n", kind, o.Size, o.Path)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&prefix, "prefix", "", "only entries whose path starts with this")
	cmd.Flags().BoolVar(&dataOnly, "data-only", false, "data objects only (skip collections)")
	cmd.Flags().BoolVar(&colsOnly, "collections-only", false, "collections only (skip data objects)")
	cmd.Flags().BoolVar(&checksums, "checksums", true, "include checksum column for data objects")
	return cmd
}

func newIRODSScanFindCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "find CHECKSUM",
		Short: "find iRODS objects across all cached scans with this checksum",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, err := irodscache.Open("")
			if err != nil {
				return err
			}
			defer cache.Close()
			hits, err := cache.FindByChecksum(args[0])
			if err != nil {
				return err
			}
			if len(hits) == 0 {
				return fmt.Errorf("no match for %s", args[0])
			}
			for _, h := range hits {
				fmt.Printf("%14d  %s\n", h.Size, h.Path)
			}
			return nil
		},
	}
}

func newIRODSScanRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm PATH",
		Short: "delete a cached scan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, err := irodscache.Open("")
			if err != nil {
				return err
			}
			defer cache.Close()
			return cache.Delete(args[0])
		},
	}
}

func shortChecksum(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
