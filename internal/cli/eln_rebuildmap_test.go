package cli

import (
	"os"
	"strings"
	"testing"
)

// TestRebuildMap_IsReadOnlyAgainstIRODS is a source-level guard: the
// rebuild command must NEVER invoke any mutating iron operation. If a
// future edit introduces an Upload / Mv / Chmod / MetaSet / etc. call
// into this file, this test fails loudly. The rebuild is contractually
// read-only against iRODS (query only) and the ELN (GET only); its sole
// write is the local mapping-store JSON under --apply.
func TestRebuildMap_IsReadOnlyAgainstIRODS(t *testing.T) {
	src, err := os.ReadFile("eln_rebuildmap.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	// Mutating iron.Client methods that must not appear.
	forbidden := []string{
		".Upload(", ".Download(", ".Mv(", ".Chmod(", ".Mkdir(",
		".MetaSet(", ".MetaAdd(", ".MetaUnset(", ".Stream(",
	}
	for _, f := range forbidden {
		if strings.Contains(body, f) {
			t.Errorf("rebuild-map must be read-only against iRODS, but calls %s", f)
		}
	}

	// The only iron entrypoint allowed is the read-only query.
	if !strings.Contains(body, ".QueryJSON(") {
		t.Error("expected rebuild-map to use QueryJSON (the read-only catalog query)")
	}

	// Guard the SQL shape: catalog reads only. A write verb slipping into
	// the query string would be a bug (genquery has no write form, but be
	// explicit).
	for _, verb := range []string{"delete ", "update ", "insert ", "drop "} {
		if strings.Contains(strings.ToLower(body), "\""+verb) {
			t.Errorf("rebuild-map SQL contains a write verb %q", verb)
		}
	}
}
