package std14

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// repoRoot is the repository root relative to this package's directory, the
// working directory of its tests.
const repoRoot = "../../.."

// noticeRow matches a row of THIRD_PARTY_NOTICES.md's "Committed files"
// table: | `path` | bytes | `sha256` |.
var noticeRow = regexp.MustCompile("^\\| `([^`]+)` \\| ([0-9]+) \\| `([0-9a-f]{64})` \\|$")

// TestThirdPartyNotices is the license CI check of design §11.1: every
// bundled face, generated table and license file is listed in
// THIRD_PARTY_NOTICES.md with its current size and SHA-256 (so changing one
// without updating the notices fails), every listed file exists, nothing
// under faces/ or LICENSES/ is unlisted, and the copies the npm package
// ships (packages/core) equal the originals.
func TestThirdPartyNotices(t *testing.T) {
	notices, err := os.ReadFile(filepath.Join(repoRoot, "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(notices))
	for sc.Scan() {
		m := noticeRow.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		path, size, sum := m[1], m[2], m[3]
		listed[path] = true
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if strconv.Itoa(len(data)) != size {
			t.Errorf("%s: %d bytes, THIRD_PARTY_NOTICES.md says %s", path, len(data), size)
		}
		h := sha256.Sum256(data)
		if got := hex.EncodeToString(h[:]); got != sum {
			t.Errorf("%s: SHA-256 %s, THIRD_PARTY_NOTICES.md says %s (regenerate with testing/tools/std14fonts and update the notices)", path, got, sum)
		}
	}
	if len(listed) == 0 {
		t.Fatal("no file rows found in THIRD_PARTY_NOTICES.md")
	}
	var want []string
	for _, pattern := range []string{"faces/*.ttf", "LICENSES/*", "*_gen.go"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, m...)
	}
	if len(want) < 13+4+2 {
		t.Errorf("only %d bundled files found: %v", len(want), want)
	}
	for _, p := range want {
		if path := "core/internal/std14/" + filepath.ToSlash(p); !listed[path] {
			t.Errorf("%s is not listed in THIRD_PARTY_NOTICES.md", path)
		}
	}

	copyOf := map[string]string{"packages/core/THIRD_PARTY_NOTICES.md": "THIRD_PARTY_NOTICES.md"}
	lic, _ := filepath.Glob("LICENSES/*")
	for _, p := range lic {
		copyOf["packages/core/LICENSES/"+filepath.Base(p)] = "core/internal/std14/LICENSES/" + filepath.Base(p)
	}
	for dst, src := range copyOf {
		a, err1 := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(src)))
		b, err2 := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(dst)))
		if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
			t.Errorf("%s is not a copy of %s (%v, %v)", dst, src, err1, err2)
		}
	}
}

// TestGeneratedHeaders: the generated tables carry their notices (APAFML:
// copyright notices kept and the modification stated; BSD: the copyright
// line) and point to their license file.
func TestGeneratedHeaders(t *testing.T) {
	for file, needles := range map[string][]string{
		"afm_gen.go": {"Adobe Systems Incorporated", "Modified:", "LICENSES/APAFML.txt", "Code generated"},
		"agl_gen.go": {"Copyright 2002-2019 Adobe", "LICENSES/BSD-3-Clause-AGL.txt", "Code generated"},
	} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		head := string(data[:min(len(data), 2048)])
		for _, n := range needles {
			if !strings.Contains(head, n) {
				t.Errorf("%s: header lacks %q", file, n)
			}
		}
	}
}
