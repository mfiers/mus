package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/mfiers/mus/internal/config"
)

// TestResolveIRODSCollection_NewLayoutWithYear — uploads now land
// at <home>/projects/<YEAR>/<data_project>/<exp> (mus 0.2.33+).
// The YEAR comes from the data_project's mandatory 4-digit suffix.
func TestResolveIRODSCollection_NewLayoutWithYear(t *testing.T) {
	tmp := t.TempDir()
	if err := writeMusCfgFile(tmp, ""+
		"irods_home=/gbiomed/home/BADS\n"+
		"data_project=Fiers2025\n"+
		"eln_experiment_name=Pilot Microglia\n"); err != nil {
		t.Fatal(err)
	}
	env, err := config.Load(tmp)
	if err != nil {
		t.Fatal(err)
	}
	got, err := resolveIRODSCollection(env, "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "/gbiomed/home/BADS/projects/2025/Fiers2025/Pilot_Microglia"
	if got != want {
		t.Errorf("resolveIRODSCollection:\n  got  %q\n  want %q", got, want)
	}
}

// TestResolveIRODSCollection_YearMatchesDataProjectSuffix — every
// data_project's regex guarantees a 4-digit year suffix; the year
// segment in the iRODS path must match exactly.
func TestResolveIRODSCollection_YearMatchesDataProjectSuffix(t *testing.T) {
	for _, tc := range []struct {
		dp, wantYear string
	}{
		{"Fiers2025", "2025"},
		{"Fiers_Mancuso_2024", "2024"},
		{"MusLab_Pilot2019", "2019"},
		{"Sun2026", "2026"},
	} {
		tmp := t.TempDir()
		_ = writeMusCfgFile(tmp, "irods_home=/zone/home/lab\n"+
			"data_project="+tc.dp+"\n"+
			"eln_experiment_name=Exp\n")
		env, _ := config.Load(tmp)
		got, err := resolveIRODSCollection(env, "", "")
		if err != nil {
			t.Errorf("%s: %v", tc.dp, err)
			continue
		}
		// Path must contain /projects/<wantYear>/<dp>/ exactly.
		wantFragment := "/projects/" + tc.wantYear + "/" + tc.dp + "/"
		if !strings.Contains(got, wantFragment) {
			t.Errorf("%s: path %q does not contain %q", tc.dp, got, wantFragment)
		}
	}
}

// TestResolveIRODSCollection_RejectsInvalidDataProject — the
// resolver inherits dataproject.ValidateName via the regex check;
// the year extraction would silently return "" without it, so the
// guard at the resolver layer is also load-bearing.
func TestResolveIRODSCollection_RejectsInvalidDataProject(t *testing.T) {
	tmp := t.TempDir()
	_ = writeMusCfgFile(tmp, "irods_home=/zone\ndata_project=fiers2025\n"+
		"eln_experiment_name=Exp\n")
	env, _ := config.Load(tmp)
	_, err := resolveIRODSCollection(env, "", "")
	if err == nil {
		t.Fatal("expected ValidateName to reject lowercase first letter")
	}
}

// writeMusCfgFile drops a mus.cfg in dir with the given body.
// Test-local helper (returns the error so callers can decide
// whether to t.Fatal or t.Errorf).
func writeMusCfgFile(dir, body string) error {
	return os.WriteFile(filepath.Join(dir, "mus.cfg"), []byte(body), 0o644)
}
