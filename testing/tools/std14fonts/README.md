# std14fonts

Offline, maintainer-run tool that builds FluxDocs' standard-14 font data
(docs/design/type1-std14-fonts.md §3.3, §11). Its output is committed, so it
is not a build dependency:

- `core/internal/std14/faces/*.ttf`: renamed subsets of Liberation 2.1.5
  (12 Latin faces) and DejaVu Sans 2.37 (Symbol and ZapfDingbats);
- `core/internal/std14/afm_gen.go`: widths, `FontBBox` values and the Symbol
  and ZapfDingbats built-in encodings from the Adobe Core 14 AFM files;
- `core/internal/std14/agl_gen.go`: glyph name to Unicode entries from the
  Adobe Glyph List;
- `core/internal/std14/LICENSES/*.txt`: the upstream licenses, verbatim,
  each after a header naming what it covers.

The subsetter is a minimal TrueType (`glyf`) one written in Go for these
inputs (`ttf.go`). It keeps the glyphs of a Unicode set plus their composite
components and drops hinting and layout tables. It rebuilds `cmap` (format 4),
`post` (format 3) and the `name` table, keeping the copyright and license
strings verbatim and replacing every naming string. It refuses to keep a glyph
that reaches outside the redaction model's `substitutedBBox` (design §4.6),
and fails if a naming string contains a Reserved Font Name. It is not a
general-purpose subsetter.

## Inputs

Download only from these upstream sources, into a fresh directory, and
extract the archives. Do not run anything shipped inside them.

| Input | Source |
|---|---|
| `liberation-fonts-ttf-2.1.5.tar.gz` | <https://github.com/liberationfonts/liberation-fonts/files/7261482/liberation-fonts-ttf-2.1.5.tar.gz> (linked from the 2.1.5 release) |
| `dejavu-fonts-ttf-2.37.tar.bz2` | <https://github.com/dejavu-fonts/dejavu-fonts/releases/download/version_2_37/dejavu-fonts-ttf-2.37.tar.bz2>; check its SHA-256 against <https://dejavu-fonts.github.io/Download.html> |
| `Core14_AFMs.zip` | <https://download.macromedia.com/pub/developer/opentype/tech-notes/Core14_AFMs.zip> |
| `glyphlist.txt`, `zapfdingbats.txt`, `LICENSE.md` | `https://raw.githubusercontent.com/adobe-type-tools/agl-aglfn/4036a9ca80a62f64f9de4f7321a9a045ad0ecfd6/<file>` |

The archive checksums are in `THIRD_PARTY_NOTICES.md`. The checksums of
every extracted file the tool reads are pinned in `inputs.go`, and the tool
stops on any mismatch.

## Run

From the repository root:

```sh
go run ./testing/tools/std14fonts \
  -liberation DL/liberation-fonts-ttf-2.1.5 \
  -dejavu     DL/dejavu-fonts-ttf-2.37 \
  -afm        DL/Core14_AFMs \
  -agl        DL/agl \
  -out .
```

`go generate ./core/internal/std14` does the same with the four directories
taken from `STD14_LIBERATION`, `STD14_DEJAVU`, `STD14_AFM` and `STD14_AGL`.

The tool prints the size and SHA-256 of every file it writes. Output is
deterministic, so an unchanged input set reproduces the committed files byte
for byte. After a change:

1. update the "Committed files" table of `THIRD_PARTY_NOTICES.md` and copy
   the file and `core/internal/std14/LICENSES/*` to `packages/core/`;
2. run `go test ./core/internal/std14/... ./core/render/`. The notices
   checksum test, the Reserved Font Name test and the substitute-box gate
   must pass.
