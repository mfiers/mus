package hashcache

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestSumAndCacheHit(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "hc.db")
	c, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	f := filepath.Join(tmp, "data.bin")
	payload := []byte("hello mus")
	if err := os.WriteFile(f, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	exp := sha256.Sum256(payload)
	expHex := hex.EncodeToString(exp[:])

	got, err := c.Sum(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != expHex {
		t.Fatalf("Sum = %q, want %q", got, expHex)
	}

	// second call should hit cache; verify via direct Get
	st, _ := os.Stat(f)
	cached, ok, err := c.Get(f, st)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || cached != expHex {
		t.Errorf("cache miss after Sum: ok=%v cached=%q", ok, cached)
	}
}

func TestCacheInvalidatesOnMtimeChange(t *testing.T) {
	tmp := t.TempDir()
	c, err := Open(filepath.Join(tmp, "hc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	f := filepath.Join(tmp, "data.bin")
	if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := c.Sum(f)
	if err != nil {
		t.Fatal(err)
	}

	// rewrite with new content + bump mtime
	if err := os.WriteFile(f, []byte("v2-different"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(f, future, future); err != nil {
		t.Fatal(err)
	}
	second, err := c.Sum(f)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Errorf("expected different sum after content change")
	}
}

func TestSumMany(t *testing.T) {
	tmp := t.TempDir()
	c, err := Open(filepath.Join(tmp, "hc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var paths []string
	for i := 0; i < 8; i++ {
		p := filepath.Join(tmp, "f"+string(rune('a'+i)))
		if err := os.WriteFile(p, []byte("content "+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	res := c.SumMany(paths)
	if len(res) != len(paths) {
		t.Fatalf("len(res) = %d, want %d", len(res), len(paths))
	}
	for _, p := range paths {
		r := res[p]
		if r.Err != nil || len(r.Sum) != 64 {
			t.Errorf("res[%q] = %+v", p, r)
		}
	}

	// sums must be unique (we wrote unique content)
	sums := make([]string, 0, len(res))
	for _, r := range res {
		sums = append(sums, r.Sum)
	}
	sort.Strings(sums)
	for i := 1; i < len(sums); i++ {
		if sums[i] == sums[i-1] {
			t.Errorf("duplicate sums: %q", sums[i])
		}
	}
}
