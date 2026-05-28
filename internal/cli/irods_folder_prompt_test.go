package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPromptOverwriteArchive(t *testing.T) {
	cases := []struct {
		input   string
		want    bool
		wantErr bool
	}{
		{"y\n", true, false},
		{"Y\n", true, false},
		{"yes\n", true, false},
		{"n\n", false, false},
		{"no\n", false, false},
		{"\n", false, false},      // default = no (don't nuke on stray Enter)
		{"garbage\ny\n", true, false}, // recovers after invalid input
		{"garbage\nx\nq\n", false, true}, // 3 invalids = error
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.input, "\n", "\\n"), func(t *testing.T) {
			var out bytes.Buffer
			got, err := promptOverwriteArchive(strings.NewReader(tc.input), &out, "/tmp/x.tar.gz", 1024)
			if tc.wantErr {
				if err == nil {
					t.Errorf("want error, got nil; output:\n%s", out.String())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v want %v; output:\n%s", got, tc.want, out.String())
			}
		})
	}
}
