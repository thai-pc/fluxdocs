package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/thai-pc/fluxdocs/core"
	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file is the byte-identity gate of docs/design/xref-reconstruction.md
// §8.3: cross-reference reconstruction runs only when the strict open fails
// (§2.1), so every file that opened before it must open exactly as before.
// TestOpenManifest records, per file, the open outcome, the page count, the
// ExtractText hash, the 72-DPI page-0 render hash and the SaveTo hash (of its
// canonical form, saveHash), and
// compares them with a manifest recorded at the base commit. A row that
// opened at the base must match exactly; only an "open_failed" row may
// change.
//
// Inputs: every PDF under testing/corpus (manifest committed in
// testdata/open_manifest.tsv), plus, when FLUXDOCS_EXTRA_CORPUS names a file
// with one PDF path per line, those PDFs (manifest in the file named by
// FLUXDOCS_EXTRA_MANIFEST; without it they are only counted).
// FLUXDOCS_MANIFEST_WRITE=path writes the current testing/corpus rows there,
// FLUXDOCS_EXTRA_MANIFEST_WRITE=path the extra corpus rows. Rows hold
// outcomes and hashes only, never document text.

// openManifest is the committed manifest for testing/corpus.
const openManifest = "testdata/open_manifest.tsv"

// manifestRow returns the manifest line of one PDF:
// "key<TAB>outcome<TAB>pages<TAB>extract<TAB>png72<TAB>save". A PNG is hashed
// by its decoded pixels (as in fileHashes), so the row does not depend on the
// Go version's PNG encoder. A failed step records "error:" and its class.
func manifestRow(key string, data []byte) string {
	row := func(fields ...string) string { return key + "\t" + strings.Join(fields, "\t") }
	doc, err := core.OpenBytes(data)
	if err != nil {
		var ie *core.InternalError
		switch {
		case errors.As(err, &ie):
			return row("internal", "-", "-", "-", "-")
		case errors.Is(err, core.ErrEncryptedDocument):
			return row("encrypted", "-", "-", "-", "-")
		}
		return row("open_failed", "-", "-", "-", "-")
	}
	defer doc.Close()
	sum := func(b []byte, err error) string {
		if err != nil {
			return "error:" + errClass(err)
		}
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}
	text, err := doc.ExtractText(core.ExtractOptions{})
	extract := sum([]byte(text), err)
	render := "-"
	if doc.PageCount > 0 {
		render = pixelHash(doc.RenderPage(0, core.RenderOptions{DPI: 72, Format: "png"}))
	}
	var buf bytes.Buffer
	save := "error:other"
	if err := doc.SaveTo(&buf); err != nil {
		save = "error:" + errClass(err)
	} else {
		save = saveHash(buf.Bytes())
	}
	outcome := "ok"
	if rep, ok := doc.RepairReport(); ok {
		// A repaired open is its own outcome, so a file that opened strictly
		// at the base and needs repair now fails the comparison.
		outcome = "ok/" + string(rep.Method)
	}
	return row(outcome, strconv.Itoa(doc.PageCount), extract, render, save)
}

// saveHash hashes a SaveTo output up to its object numbering. The writer
// numbers objects in the order it meets them while walking Go maps
// (parse.Dict), so the bytes of one document's SaveTo differ from run to run
// at the base commit; the hash is taken over a canonical serialization
// instead: the trailer's /Root and /Info walked depth first with dictionary
// keys sorted, each object renumbered by the order it is first met, streams
// by their dictionary and raw bytes. (The saved size is not recorded: the
// digits of the object numbers change it.)
func saveHash(saved []byte) string {
	r, err := parse.NewResolver(saved)
	if err != nil {
		return "error:reopen"
	}
	h := sha256.New()
	ids := map[int]int{} // source object number -> canonical number
	var queue []int
	var write func(o parse.Object)
	write = func(o parse.Object) {
		switch v := o.(type) {
		case parse.Reference:
			id, ok := ids[v.Number]
			if !ok {
				id = len(ids) + 1
				ids[v.Number] = id
				queue = append(queue, v.Number)
			}
			fmt.Fprintf(h, "%d R ", id)
		case parse.Dict:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, string(k))
			}
			sort.Strings(keys)
			h.Write([]byte("<<"))
			for _, k := range keys {
				fmt.Fprintf(h, "/%s ", k)
				write(v[parse.Name(k)])
			}
			h.Write([]byte(">>"))
		case parse.Array:
			h.Write([]byte("["))
			for _, e := range v {
				write(e)
			}
			h.Write([]byte("]"))
		case *parse.Stream:
			write(v.Dict)
			fmt.Fprintf(h, "stream %d:", len(v.Raw))
			h.Write(v.Raw)
		case parse.String:
			fmt.Fprintf(h, "<%x> ", v.Value)
		default:
			fmt.Fprintf(h, "%v ", v)
		}
	}
	tr := r.Trailer()
	write(parse.Dict{"Root": tr["Root"], "Info": tr["Info"]})
	for i := 0; i < len(queue); i++ {
		o, err := r.Resolve(parse.Reference{Number: queue[i]})
		if err != nil {
			return "error:resolve"
		}
		fmt.Fprintf(h, "\nobj %d ", ids[queue[i]])
		write(o)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pixelHash hashes a PNG by its size and decoded 8-bit RGBA rows.
func pixelHash(b []byte, err error) string {
	if err != nil {
		return "error:" + errClass(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return "error:png"
	}
	bd := img.Bounds()
	h := sha256.New()
	fmt.Fprintf(h, "%dx%d\n", bd.Dx(), bd.Dy())
	row := make([]byte, 0, 4*bd.Dx())
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		row = row[:0]
		for x := bd.Min.X; x < bd.Max.X; x++ {
			c := color.RGBAModel.Convert(img.At(x, y)).(color.RGBA)
			row = append(row, c.R, c.G, c.B, c.A)
		}
		h.Write(row)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// manifestRows computes the rows of the given (key, path) inputs, sorted.
func manifestRows(keys, paths []string) []string {
	rows := make([]string, len(paths))
	parallel(len(paths), func(i int) {
		data, err := os.ReadFile(paths[i])
		if err != nil {
			rows[i] = keys[i] + "\tread_failed\t-\t-\t-\t-"
			return
		}
		rows[i] = manifestRow(keys[i], data)
	})
	sort.Strings(rows)
	return rows
}

// localCorpus returns the keys and paths of every PDF under testing/corpus.
func localCorpus() (keys, paths []string) {
	for _, dir := range []string{"clean", "malformed", "encrypted", "forms", "scanned"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
		for _, m := range matches {
			keys = append(keys, filepath.ToSlash(m))
			paths = append(paths, m)
		}
	}
	return keys, paths
}

// TestOpenManifest is the §8.3 gate.
func TestOpenManifest(t *testing.T) {
	keys, paths := localCorpus()
	local := manifestRows(keys, paths)
	var extra []string
	if list := os.Getenv("FLUXDOCS_EXTRA_CORPUS"); list != "" {
		ps, err := readLines(list)
		if err != nil {
			t.Fatalf("FLUXDOCS_EXTRA_CORPUS: %v", err)
		}
		extra = manifestRows(ps, ps)
	}
	for _, w := range []struct {
		env  string
		rows []string
	}{{"FLUXDOCS_MANIFEST_WRITE", local}, {"FLUXDOCS_EXTRA_MANIFEST_WRITE", extra}} {
		if path := os.Getenv(w.env); path != "" {
			if err := os.WriteFile(path, []byte(strings.Join(w.rows, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %d manifest rows to %s", len(w.rows), path)
		}
	}
	compareManifest(t, "testing/corpus", local, openManifest)
	if len(extra) > 0 {
		if base := os.Getenv("FLUXDOCS_EXTRA_MANIFEST"); base != "" {
			compareManifest(t, "extra corpus", extra, base)
		} else {
			t.Logf("extra corpus: %d rows (no FLUXDOCS_EXTRA_MANIFEST to compare)", len(extra))
		}
	}
}

// compareManifest fails on a row that opened in the manifest at basePath and
// differs now in any field, and on a manifest file that is gone. A row that
// was "open_failed" may change (it is logged), and a new file is logged.
func compareManifest(t *testing.T, label string, rows []string, basePath string) {
	t.Helper()
	lines, err := readLines(basePath)
	if err != nil {
		t.Fatalf("%s manifest: %v (record one with FLUXDOCS_MANIFEST_WRITE)", label, err)
	}
	split := func(l string) (string, string) {
		k, rest, _ := strings.Cut(l, "\t")
		return k, rest
	}
	base := make(map[string]string, len(lines))
	for _, l := range lines {
		k, rest := split(l)
		base[k] = rest
	}
	seen := make(map[string]bool, len(rows))
	changed, identical := 0, 0
	for _, r := range rows {
		k, now := split(r)
		seen[k] = true
		was, ok := base[k]
		switch {
		case !ok:
			t.Logf("%s: %s not in the manifest: %s", label, k, now)
		case was == now:
			identical++
		case strings.HasPrefix(was, "open_failed\t"):
			changed++
			t.Logf("%s: %s was open_failed, now %s", label, k, now)
		default:
			t.Errorf("%s: %s changed:\n was %s\n now %s", label, k, was, now)
		}
	}
	for k := range base {
		if !seen[k] {
			t.Errorf("%s: %s is in the manifest but was not checked", label, k)
		}
	}
	t.Logf("%s: %d rows identical, %d open_failed rows changed", label, identical, changed)
}
