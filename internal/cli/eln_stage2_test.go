package cli

import (
	"testing"

	"codeberg.org/mfiers/mus/internal/defaults"
)

func TestStageTwoRemotePath_UsesProjectsPlural(t *testing.T) {
	cases := []struct {
		name      string
		irodsHome string
		want      string
	}{
		{
			name:      "explicit irods_home",
			irodsHome: "/gbiomed/home/BADS",
			want:      "/gbiomed/home/BADS/projects/eln_mappings.json",
		},
		{
			name:      "trailing slash is trimmed",
			irodsHome: "/gbiomed/home/BADS/",
			want:      "/gbiomed/home/BADS/projects/eln_mappings.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stageTwoRemotePath(envFromMap(t, map[string]string{"irods_home": tc.irodsHome}))
			if got != tc.want {
				t.Fatalf("stageTwoRemotePath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStageTwoRemotePath_FallsBackToDefault(t *testing.T) {
	if defaults.IRODSHome == "" {
		t.Skip("no baked-in default IRODSHome to test the fallback")
	}
	want := defaults.IRODSHome + "/projects/eln_mappings.json"
	got := stageTwoRemotePath(envFromMap(t, nil))
	if got != want {
		t.Fatalf("stageTwoRemotePath (default) = %q, want %q", got, want)
	}
}

func TestStageTwoLegacyRemotePath_UsesProjectSingular(t *testing.T) {
	got := stageTwoLegacyRemotePath(envFromMap(t, map[string]string{"irods_home": "/gbiomed/home/BADS"}))
	want := "/gbiomed/home/BADS/project/eln_mappings.json"
	if got != want {
		t.Fatalf("stageTwoLegacyRemotePath = %q, want %q", got, want)
	}
}

// The new and legacy paths must differ — the whole point of the transition
// is reading one location while writing the other.
func TestStageTwo_NewAndLegacyDiffer(t *testing.T) {
	env := envFromMap(t, map[string]string{"irods_home": "/gbiomed/home/BADS"})
	if stageTwoRemotePath(env) == stageTwoLegacyRemotePath(env) {
		t.Fatal("new and legacy stage-2 paths must differ")
	}
}

// When irods_home is unavailable from both cascade and defaults, both helpers
// return "" so callers skip stage-2 entirely.
func TestStageTwo_EmptyWhenNoHome(t *testing.T) {
	if defaults.IRODSHome != "" {
		t.Skip("defaults.IRODSHome is set; cannot exercise the empty-home path")
	}
	if got := stageTwoRemotePath(envFromMap(t, nil)); got != "" {
		t.Fatalf("stageTwoRemotePath with no home = %q, want empty", got)
	}
	if got := stageTwoLegacyRemotePath(envFromMap(t, nil)); got != "" {
		t.Fatalf("stageTwoLegacyRemotePath with no home = %q, want empty", got)
	}
}
