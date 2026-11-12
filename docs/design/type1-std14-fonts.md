# Design: Type 1, CFF and standard-14 font rendering (Wave 1)

Status:

- **Phase 0 done** (census and baselines, §10).
- **Phase 1 done, landed 2026-11-12** (`4929f76`; the `/Widths` advance change
  in its own commit `a1fb7ca`): embedded Type 1.
- **Phase 2 done, landed 2026-11-12** (`7ef8e90`): bare CFF (Type1C and
  CIDFontType0C).
- **Phase 3 next** (in progress). The maintainer decided D1–D4, Q5 and Q9 on
  2026-11-11 (§11) and approved downloading the Phase 3 font sources on
  2026-11-12 (§11).
- Phase 4 and later: not started.

Sections marked "As shipped" record what Phases 1 and 2 actually implemented
where it refines or differs from the original design. Everything else in this
note is still the plan.
Tier: **Core (MIT)**. Packages: `core/render` (glyph pipeline), new
`core/internal/psfont` (Type 1 and CFF parsers), `core/internal/fontmodel`
(read-only accessors and encoding tables), new `core/internal/std14` (metrics,
names), plus the substitute faces (location set by D2). Pure Go, no CGO. Both
targets (native, `GOOS=js GOARCH=wasm`).

Basis (clean-room, public specifications only):

- ISO 32000-1:2008 §9.2.4 (glyph space), §9.4.4 (text space details),
  §9.6.2 (Type 1 fonts; §9.6.2.2 standard 14), §9.6.6 (character encoding;
  §9.6.6.2 Type 1, §9.6.6.4 TrueType), §9.7.4 (CIDFonts; §9.7.4.2 glyph
  selection), §9.8 (font descriptors; §9.8.2 flags), §9.9 (embedded font
  programs, Table 126 `/Length1` `/Length2` `/Length3`, Table 127 `/FontFile3`
  subtypes), Annex D (D.2 Latin set and the Standard, MacRoman, WinAnsi and
  PDFDoc encodings; D.5 Symbol; D.6 ZapfDingbats).
- Adobe *Type 1 Font Format* (version 1.1): Chapter 6 (charstring commands),
  Chapter 7 (encryption), Chapter 8 (OtherSubrs: flex, hint replacement).
- Adobe Technical Note #5015 (Type 1 Font Format Supplement), #5040 (PFB
  segment layout), #5176 (CFF), #5177 (Type 2 charstrings).
- Adobe Glyph List and AGLFN (BSD 3-Clause), the Adobe Core 14 AFM files (Adobe
  PostScript AFM License, SPDX `APAFML`), the Unicode Standard code charts.

**Never** read or paraphrase the source of MuPDF, pdf.js, Nutrient, Apryse, or
any other PDF renderer (SECURITY.md §6). Ghostscript is used only as an
external black-box test oracle, as the redaction oracle job already does.

---

## 1. Problem and scope

Today `render.loadOneFont` accepts only a program that
`golang.org/x/image/font/sfnt` parses: `/FontFile2` (TrueType) and
`/FontFile3 /OpenType`. Everything else yields a nil font, and `Tj`/`TJ` with
a nil font draws nothing (`core/render/glyphfont.go:15-18`,
`core/render/render.go:1-8`). That covers:

- **Type 1 programs** (`/FontFile`), typical of pdfTeX, dvips and older
  Distiller output. Every LaTeX paper set in Computer Modern renders blank.
- **Bare CFF programs** (`/FontFile3 /Type1C` and `/CIDFontType0C`), typical of
  Acrobat Distiller, InDesign, Quartz and many server-side generators.
  `sfnt.Parse` requires an sfnt wrapper (`cmap`, `head`, `hhea`, `hmtx`,
  `maxp`), so a bare CFF fails to parse and the text is dropped.
- **The 14 standard fonts when not embedded** (§9.6.2.2), and other
  non-embedded simple fonts (Word "Arial"/"Times New Roman" with embedding
  disabled, report generators). This includes the in-repo corpus: all four
  text-bearing files in `testing/corpus/clean/` use non-embedded `/Helvetica`,
  so their text renders blank today.
- **Type 3 fonts**: no `/FontDescriptor` program, so also blank (deferred, §1.2).

(This paragraph describes the state before Phase 1. Since Phase 2, Type 1
and bare CFF programs render; see the support matrix.)

Three related gaps in the existing sfnt path are recorded so the new pipeline
does not repeat them:

- The **advance** of every glyph comes from the program (`hmtx`), not from the
  PDF's `/Widths`. §9.4.4 defines the displacement from `/Widths`. **Fixed
  for simple fonts in Phase 1** (`a1fb7ca`, §2.1 step 3).
- Simple-font **codes are looked up as Latin-1 runes** in whatever cmap sfnt
  picked: `/Encoding`, `/Differences`, and the §9.6.6.4 symbolic-cmap rules are
  ignored. Still open (P4).
- **TrueType/OpenType glyphs were drawn upside down.** sfnt outlines are
  y-down and text space is y-up (§9.2.4), and the glyph-to-text matrix did not
  negate y. Found during Phase 1 and **fixed in `41ebb56`** (the y scale is
  now `−fontSize/1000`).

### 1.1 Support matrix

"Phase" refers to §8. "Deferred" items must keep today's behavior: no crash,
nothing drawn, the rest of the page renders.

| Font | Program | Today | Wave 1 target | Phase |
|---|---|---|---|---|
| Type 1, embedded | `/FontFile` (cleartext + eexec, binary or hex, PFB segments tolerated) | blank | own parser, Type 1 charstrings incl. `seac`, flex, hint replacement | **P1, done 2026-11-12** |
| Type 1 / TrueType, all simple fonts | — | advance from program | advance from `/Widths` (§9.4.4), then `/MissingWidth`, then program (§2.1 step 3); AFM step added by P3 | **P1, done 2026-11-12** (AFM: P3) |
| Type 1 via CFF | `/FontFile3 /Type1C` | blank | own CFF parser, Type 2 charstrings | **P2, done 2026-11-12** |
| Type 0 / CIDFontType0 via CFF | `/FontFile3 /CIDFontType0C` | blank | own CFF parser, CID-keyed (FDArray/FDSelect, charset CID→GID); non-CID-keyed CFF uses CID as GID (§9.7.4.2) | **P2, done 2026-11-12** |
| Composite font over CFF or Type 1, advances | `/FontFile3`, `/FontFile` | advance from program | `/W` and `/DW` (§9.7.4.3), exact for an Identity CMap; other CMaps fall back to the program | **P2, done 2026-11-12** |
| Composite TrueType (CIDFontType2), advances | `/FontFile2` | advance from `hmtx` | `/W` and `/DW` (§9.7.4.3) | proposed P4 follow-up |
| OpenType (CFF or TrueType outlines) | `/FontFile3 /OpenType` | renders via sfnt (upright since `41ebb56`) | unchanged | — |
| OpenType CFF glyph using `endchar`-`seac` | `/FontFile3 /OpenType` | that glyph is dropped (sfnt returns "unsupported Type 2 Charstring") | optional: retry the glyph through `psfont` on the extracted `CFF ` table | P2 (optional), **still open** (rare, §8) |
| Standard 14, not embedded | none | blank | bundled substitute faces + Adobe AFM widths when `/Widths` is absent | **P3** (needs D1–D3) |
| Other simple font, not embedded | none | blank | substitute chosen by name alias, then descriptor flags; `/Widths` advances | **P3** |
| TrueType, nonsymbolic with `/Encoding` or `/Differences` | `/FontFile2` | codes read as Latin-1 runes | §9.6.6.4: name → Unicode → (3,1) cmap; `post` names for `/Differences` | **P4** |
| TrueType, symbolic, (3,0) cmap | `/FontFile2` | blank unless the code is also a valid U+00xx in the chosen cmap | §9.6.6.4: try code, then `0xF000`, `0xF100`, `0xF200` + code; (1,0) fallback | **P4** |
| CIDFontType2 with a `/CIDToGIDMap` stream | `/FontFile2` | GID = CID (wrong glyphs) | honor the stream map | **P4** |
| TrueType without a `cmap` table | `/FontFile2` | `sfnt.Parse` fails → blank | deferred: needs a synthesized cmap or an own `glyf` reader; measure prevalence first (Phase 0) | deferred |
| Type 3 | `/CharProcs` | blank | separate design note (content-stream glyphs; reuses the Form XObject machinery and its recursion guards) | deferred, next |
| Non-embedded Type 0 (CJK) | none | blank | deferred (needs CJK faces; size) | deferred |
| `/MMType1` | embedded MM program | blank | deferred (needs blend support); non-embedded MM → P3 substitute by flags | deferred / P3 |
| CFF with `CharstringType 1`, CFF2, variable fonts | — | blank | rejected (glyph not drawn) | deferred |
| Vertical writing (WMode 1) | any | horizontal advance | deferred | deferred |
| Hinting (Type 1 hints, Type 2 hint masks, TrueType bytecode) | — | ignored | ignored by design (anti-aliased outline rendering) | — |

### 1.2 Out of scope

Type 3, vertical metrics, CJK substitution, MM blending, glyph hinting, a
document-level font cache (§9 Q7), and any change to text extraction or to the
redaction text model (§4.6).

---

## 2. Algorithms

### 2.1 Pipeline shared by every simple font

For a code `c` of a simple font (one byte, §9.4.3):

1. **Glyph name** (§9.6.6):
   - `/Encoding` `/Differences` entry for `c`, if any.
   - Otherwise the base encoding: `/Encoding` name (`WinAnsiEncoding`,
     `MacRomanEncoding`, `MacExpertEncoding`) or the dictionary's
     `/BaseEncoding`.
   - Without one: the program's **built-in encoding** (Type 1 `/Encoding` array
     or the CFF Encoding) when embedded. For a non-embedded font:
     StandardEncoding for nonsymbolic fonts, the font's own built-in encoding
     for Symbol and ZapfDingbats (Annex D.5, D.6).
   - Annex D notes for WinAnsiEncoding apply (code 240 octal reads as `space`,
     255 octal as `hyphen`; other unassigned codes above 40 octal as `bullet`).
     The implementer transcribes them from the notes to Table D.2, not from
     memory.
2. **Glyph**, by program kind:
   - Type 1: name → `/CharStrings` entry. A missing name → `.notdef`, which is
     drawn (usually empty).
   - CFF (non-CID): name → SID → GID through the charset. Standard strings 0–390
     come from TN #5176 Appendix A. A font whose Top DICT is CID-keyed but is
     used as a simple font: name lookup fails → nothing drawn.
   - Substitute or other Unicode-cmap face: name → Unicode (AGL data, `uniXXXX`,
     `uXXXX[XX]` via the existing `fontmodel.glyphRune` rules) → cmap.
   - TrueType (P4): §9.6.6.4.
3. **Advance** w0 in text space at size 1:
   - `/Widths[c − FirstChar] / 1000` when present. `/Widths` is in glyph-space
     units of 1/1000 text space for every non-Type 3 font (§9.6.2.1),
     **whatever the program's FontMatrix**.
   - Else, for a standard-14 font, the AFM width of the glyph name.
   - Else `/MissingWidth / 1000` when the descriptor has it and `c` is outside
     `/FirstChar`–`/LastChar`.
   - Else the program's own advance (`hsbw`/`sbw` wx, the CFF width, `hmtx`)
     mapped through its FontMatrix.
   - Else 0.5 em (today's fallback).

   **As shipped (Phase 1, `a1fb7ca`; `glyphFont.advance`).** The order is
   `/Widths` → `/MissingWidth` → program advance → 0.5 em. The AFM step does
   not exist yet; Phase 3 adds it as planned above.
   - `/Widths` misses for a code outside `/FirstChar`–`/LastChar` or a
     non-numeric entry; then `/MissingWidth` applies.
   - **No `/Widths` array at all:** the program advance is used directly
     (`/MissingWidth` is only consulted next to a `/Widths` array).
   - **`/Widths` present, no `/MissingWidth`:** we fall back to the program
     advance. Table 122 makes the default `/MissingWidth` 0, so this is a
     **deliberate leniency**: the program's advance is a better guess for a
     code `/Widths` does not cover than a zero displacement.
   - The program advance of a glyph that cannot be built is 0.5 em.
   - The base encodings available in step 1 are StandardEncoding,
     WinAnsiEncoding and MacRomanEncoding. `MacExpertEncoding` is not
     transcribed yet (P3); a font naming it falls back to the program's
     built-in encoding, then StandardEncoding.
4. **Outline**: program glyph space → FontMatrix → text space (font size, Tz,
   Ts) → Tm → CTM. Outlines are filled with the nonzero rule (Type 1 and Type 2
   outlines are designed for it), through the existing `fillPath`.

Composite fonts keep their current code split. CIDFontType0C adds CID → GID
through the CFF charset (or GID = CID for a non-CID-keyed CFF, §9.7.4.2).

**As shipped (Phase 2): composite advances.** A composite font over a CFF
program (CIDFontType0C) or a Type 1 program (a nonstandard `/FontFile` under
a CIDFont) advances by the CIDFont's `/W` and `/DW` (§9.7.4.3; `/DW` defaults
to 1000), read through `fontmodel`. The value is exact for an Identity CMap.
For another CMap `fontmodel` only gives bounds, and the program's advance is
used. **Composite TrueType (CIDFontType2) still advances by `hmtx`**;
switching it to `/W`/`/DW` is a proposed P4 follow-up (it changes TrueType
page hashes, so it needs its own commit).

### 2.2 Type 1 program (`/FontFile`)

**Segments.** ISO §9.9 Table 126 describes three parts: `/Length1` bytes of
cleartext, `/Length2` bytes of eexec-encrypted binary, and `/Length3` bytes of
trailing zeros and `cleartomark` (may be 0). Real files violate the lengths,
so parsing is lenient:

- If the data starts with `0x80 0x01`, strip PFB segment headers (TN #5040:
  `0x80`, a type byte 1/2/3, a 4-byte little-endian length), bounds-checked.
- Locate the end of the cleartext by the `eexec` token, not by trusting
  `/Length1`. If `/Length1` points at a byte right after `eexec` + whitespace,
  the two agree. If they disagree, the token wins.
- The encrypted part runs to the end of the data, or to a run of ≥ 64 ASCII
  `0` characters (the fixed-content part). `/Length2` and `/Length3` are hints
  only.

**eexec decryption** (Type 1 spec Ch. 7):
`plain = cipher XOR (r >> 8)`, `r = ((cipher + r) · 52845 + 22719) mod 65536`,
`r0 = 55665`. The first 4 plaintext bytes are discarded.

- Hex vs binary: skip whitespace after `eexec`. If the first 4 bytes are all
  hex digits, the part is hex; hex decoding skips whitespace and ignores a
  trailing odd nibble.
- The decrypted buffer is charged to the decoded-bytes counter D (§5.2).

**Cleartext and private dictionary scan.** This is a token scan, not a
PostScript interpreter:

- Cleartext: `/FontMatrix [a b c d e f]` (6 finite numbers, else the default
  0.001 matrix), `/FontBBox`, and `/Encoding`. The encoding is either
  `StandardEncoding` or an array filled by `dup <code> /<name> put` lines;
  codes 0–255 only, at most 256 entries.
- Private dictionary (decrypted): `/lenIV` (default 4; −1 = charstrings not
  encrypted), `/Subrs <n> array` followed by `dup <i> <len> RD <len bytes> NP`,
  and `/CharStrings <n> dict dup begin` followed by `/<name> <len> RD <len
  bytes> ND`.
- The `RD` token may be spelled `RD` or `-|`; `NP` as `NP`, `|` or `noaccess
  put`; `ND` as `ND`, `|-` or `noaccess def`. Exactly one space separates
  `RD` from the binary bytes. Lengths are bounds-checked against the remaining
  buffer.
- Charstring decryption uses the same cipher with `r0 = 4330`, then discards
  the first `lenIV` bytes. A charstring shorter than `lenIV` is an empty glyph.

**Type 1 charstring interpreter** (Type 1 spec Ch. 6):

- Numbers: byte v in 32–246 → v − 139; 247–250 → (v − 247)·256 + w + 108;
  251–254 → −(v − 251)·256 − w − 108; 255 → a 32-bit signed big-endian integer.
- Operators implemented:
  - `hsbw` / `sbw`: set the sidebearing point as the current point and record
    the width.
  - `rmoveto`, `hmoveto`, `vmoveto`, `rlineto`, `hlineto`, `vlineto`,
    `rrcurveto`, `vhcurveto`, `hvcurveto`, `closepath`, `endchar`.
  - `callsubr`, `return`, `div`, `callothersubr`, `pop`, `setcurrentpoint`.
  - `seac`: compose the base and accent glyphs. `bchar` and `achar` are
    StandardEncoding codes, looked up by StandardEncoding name in
    `/CharStrings`, whatever the font's own encoding. The accent is translated
    per the `seac` definition in Ch. 6, using `asb`, `adx` and `ady` relative to
    the base's sidebearing point. The implementer derives the translation from
    the spec text and pins it with a hand-computed test (§7.1). One level only:
    a component that itself calls `seac` aborts the glyph.
- Hint operators (`hstem`, `vstem`, `hstem3`, `vstem3`, `dotsection`) are
  consumed and ignored.
- OtherSubrs (Ch. 8), for rendering only:
  - **0, 1, 2 (flex):** after `1 callothersubr`, the next seven `rmoveto` are
    collected as the reference point plus six control points, not drawn.
    `0 callothersubr` (flex height, final x, y) emits two `curveto`s from the
    collected points. It pushes the final coordinates for the two `pop`s that
    precede `setcurrentpoint`. A flex sequence without exactly 7 points is
    discarded (the glyph keeps its other segments).
  - **3 (hint replacement):** returns its argument (the subr number), so the
    following `pop callsubr` calls a hint-only subr, which is harmless.
  - **Any other number** (counter control 12–13 per TN #5015, MM 14–18,
    unknown): pop its n arguments and push them back so that the following
    `pop`s return them in order. MM fonts are deferred, so their blend result is
    not computed. (As shipped, 12 and 13 differ; see below.)
- Arithmetic is float64. Every product that feeds a coordinate is wrapped in
  an explicit `float64(…)` conversion: the Go spec allows fused multiply-add,
  and arm64 builds fuse while wasm does not. Without the conversion,
  native/wasm render parity breaks. `div` by zero aborts the glyph.

**As shipped (Phase 1): confirmed semantics.** Confirmed during
implementation and review; see `core/internal/psfont/type1_test.go` (e.g.
`TestType1SeacRules`, `TestType1_OtherSubrResults`).

- **`closepath` keeps the current point** (Ch. 6), unlike PostScript
  `closepath`. The next drawing operator after a moveto starts a new subpath,
  and every subpath is closed for filling.
- **`seac` accent origin:** the accent is drawn with its origin at
  (`sbx + adx − asb`, `ady`), where `sbx` is the x sidebearing of the
  composite's own `hsbw`/`sbw`. Its sidebearing point then lands at
  (`sbx + adx`, `ady`). The base is drawn at the glyph origin. A component's
  own `hsbw` sets only its start point, not the width. `seac` ends the
  charstring.
- **Counter control, OtherSubrs 12 and 13,** return nothing: they push no
  results onto the PostScript stack.
- **Unpopped OtherSubr results are dropped** at the next `callothersubr`:
  every call starts with an empty PostScript stack (`psp` reset), so a glyph's
  unpopped results cannot accumulate up to `maxPSStack`.
- **OtherSubr 0 with an argument count other than 3** ends the flex (no
  curves drawn) and its arguments become its results, like an unknown
  OtherSubr.
- **Flex end vs `setcurrentpoint`:** OtherSubr 0 moves the current point to
  the last flex point and leaves (x, y) for the `pop pop` that follow. When
  the `setcurrentpoint` after them names a different point, **we follow
  `setcurrentpoint`**.

### 2.3 CFF program (`/FontFile3 /Type1C`, `/CIDFontType0C`)

Per TN #5176:

- **Structure:** Header (major version 1; `hdrSize` honored), Name INDEX, Top
  DICT INDEX (first entry used), String INDEX, Global Subr INDEX. From the Top
  DICT: `CharStrings`, `charset`, `Encoding`, `Private` (size, offset;
  `Subrs` is relative to the Private DICT), `FontMatrix` (default 0.001), and
  `CharstringType` (only 2 is accepted).
- **CID-keyed fonts:** `ROS` marks the font as CID-keyed. Read `FDArray` (each
  Font DICT has its own Private DICT and local Subrs) and `FDSelect` formats 0
  and 3. The effective FontMatrix of a CID-keyed font is resolved per TN #5176
  from the Top DICT and the selected FDArray dictionary; the product is
  validated (§5.1).
- **Charset:** formats 0, 1, 2 (GID → SID, or GID → CID for CID-keyed fonts),
  inverted lazily into name → GID or CID → GID maps.
- **Encoding:** predefined 0 (Standard) and 1 (Expert), custom formats 0 and 1,
  and supplements (high bit of the format byte).
- **INDEX:** `count` (Card16), `offSize` 1–4, count+1 offsets, 1-based relative
  to the byte before the data. All arithmetic in int64.

**Type 2 charstrings** (TN #5177):

- **Numbers:** the 32–254 ranges as in Type 1, `28` shortint, `255` 16.16
  fixed.
- **Path operators:** `rmoveto`, `hmoveto`, `vmoveto`, `rlineto`, `hlineto`,
  `vlineto`, `rrcurveto`, `hhcurveto`, `vvcurveto`, `hvcurveto`, `vhcurveto`,
  `rcurveline`, `rlinecurve`, `flex`, `hflex`, `hflex1`, `flex1` (flex always
  drawn as two curves), `endchar`.
- **Hints:** `hstem`, `vstem`, `hstemhm`, `vstemhm`, `hintmask`, `cntrmask`.
  They are consumed for the stem count (implied `vstem` before the first
  `hintmask`; the mask is ceil(stems / 8) bytes) and otherwise ignored.
- **Subroutines:** `callsubr` and `callgsubr` with the bias rule (107 for count
  < 1240, 1131 for < 33900, else 32768), and `return`.
- **Arithmetic and storage:** `and or not abs add sub div neg eq drop put get
  ifelse random mul sqrt dup exch index roll`. `random` returns a fixed
  constant, for deterministic output on both targets. `sqrt` of a negative
  number aborts the glyph.
- **Width:** an odd argument count on the first stack-clearing operator
  carries the width (`nominalWidthX` + w; `defaultWidthX` otherwise).
- **`endchar` with 4 arguments** is the deprecated `seac` (Appendix C): adx ady
  bchar achar, components looked up by StandardEncoding name through the
  charset. Same one-level rule as Type 1.

**As shipped (Phase 2).**

- **`endchar`-seac accent origin is (`adx`, `ady`).** Type 2 has no `asb`
  operand and no sidebearing point (TN #5177 Appendix C), so the accent's
  origin is simply offset by (`adx`, `ady`) from the base's.
- **`endchar` operand counts:** 1 = width; 4 = seac; **5 = width + seac**
  (the width is the bottom operand, as for any first stack-clearing
  operator). A seac component's own width is consumed and discarded.
- **`random` returns 0.5,** a constant, so output is identical on every
  target.
- **CID-keyed FontMatrix.** TN #5176 gives a Font DICT's FontMatrix the usual
  default, and producers disagree when only one of the two matrices is
  written. The shipped rule (`readCIDFonts`):
  - both the Top DICT and the selected FD have a FontMatrix: FD × Top (FD
    applied first);
  - Top DICT only: used **alone** when it is already glyph-scale (every
    linear entry |x| < 0.1, e.g. the usual `[0.001 0 0 0.001 0 0]` written
    once); otherwise default (0.001) × Top (e.g. an explicit identity meant
    to scale the FD matrices);
  - FD only: the FD matrix as is;
  - neither: the default.

  A product that fails the §5.1 matrix check falls back to the default. A
  glyph uses its own FD's matrix (`Glyph.Matrix`).
- **Predefined charsets and encodings.** The Expert and ExpertSubset
  charsets and the Expert encoding are transcribed from TN #5176 Appendices B
  and C (`psfont/cffstd.go`, with compile-time size checks). ISOAdobe is
  SID = GID up to the last ISOAdobe SID. A CID-keyed font with a predefined
  charset uses CID = GID.

### 2.4 Standard-14 and other non-embedded fonts

- **Name normalization** of `/BaseFont`:
  - Strip a subset tag (`ABCDEF+`).
  - Split a `,Bold` / `,Italic` / `,BoldItalic` suffix and the PostScript
    suffixes `-Bold`, `-Italic`, `-Oblique`, `-BoldOblique`, `-Roman`.
  - Then match the 14 names of §9.6.2.2.
  - Then a short alias table, an implementation choice that ISO does not define
    and that the tests document: `Arial`/`ArialMT` → Helvetica, `TimesNewRoman`
    / `TimesNewRomanPS(MT)` → Times, `CourierNew` / `CourierNewPS(MT)` →
    Courier.
- **Other non-embedded simple fonts**, by descriptor flags (§9.8.2 Table 123):
  - `FixedPitch` → mono; `Serif` → serif; else sans.
  - `Italic` or a non-zero `/ItalicAngle` → italic face.
  - `ForceBold`, or a name containing `Bold`/`Black`/`Heavy`/`Semibold` →
    bold face.
  - `Symbolic` without `Nonsymbolic`, and not Symbol or ZapfDingbats by name →
    **not drawn**: the codes' meaning is unknown, so drawing Latin glyphs would
    show wrong text.
- **Faces map one to one** to the substitute families' four styles. Synthetic
  styling (shear 0.2 for oblique, fill + stroke at 0.03 em for bold) is used
  only when the chosen option lacks a style (§3, option G).
- **Widths:** `/Widths` when present, else the AFM width (§2.1 step 3). The
  substitute's own advance is never used for a standard-14 font.
- **Fit:** the substitute outline is drawn **unscaled** when its advance is
  within ±10% of w0. Otherwise it is compressed horizontally by w0 / advance,
  clamped to [0.6, 1], and **never widened** (keeps the ink inside the
  redaction model's box, §4.6). Open item **Q6**.
- **Symbol and ZapfDingbats:**
  - Code → name via Annex D.5 and D.6.
  - Name → Unicode via the AGL data (Symbol, mostly Greek and math) and the
    AGL `zapfdingbats` list (`a1`… → U+2701…, the Unicode Dingbats block).
  - Unicode → glyph in the symbol substitute face (D3).

---

## 3. Standard-14 substitute faces — LICENSE DECISION (maintainer)

> **Decided 2026-11-11 (§11): D1 = L-s, D2 = S2, D3 = D, D4 = accept
> APAFML.** The options below are kept for the record.

Constraints:

- The default build stays MIT-distributable and pure Go.
- The npm package ships `fluxdocs.wasm`, so the font data travels inside a
  binary distributed as part of an MIT library.
- WASM size is a KPI (spec §18.1).
- Whatever face is drawn, **advances come from `/Widths` or the Adobe AFM
  metrics**, so metric compatibility only affects how glyph shapes fit their
  slots, never the text position. That also keeps the redaction model correct
  (§4.6).

### 3.1 Options

Sizes are approximate and must be measured when vendoring. Liberation 2.1.5:
12 TTF faces ≈ 4.2 MB installed, ≈ 1.45–1.57 MB as a compressed package.

| | Option | License | Covers | Metric match | Raw size (est.) | Bundling verdict |
|---|---|---|---|---|---|---|
| **L** | **Liberation Sans/Serif/Mono 2.x**, full | SIL OFL 1.1; Reserved Font Names "Liberation" (Red Hat) and "Arimo", "Tinos", "Cousine" (Google data) | 12 Latin faces (Helvetica, Times, Courier × 4 styles) | Metric-compatible with Arial, Times New Roman and Courier New, which match Helvetica, Times and Courier | ≈ 4.2 MB (12 × ≈ 350 KB) | OFL explicitly allows bundling with software under any license. Each copy keeps its copyright and license; the font's `name` table satisfies that inside the binary, plus a NOTICE file. Unmodified: no rename needed. |
| **L-s** | **Liberation, subset** to the Annex D Latin set (≈ 330 glyphs: Standard, WinAnsi, MacRoman, PDFDoc) | OFL 1.1 | same 12 | same | ≈ 30–50 KB each, ≈ 0.4–0.6 MB total (≈ 0.25–0.35 MB brotli) | A subset is a Modified Version under OFL, so it **must not use the Reserved Font Names**: rename the internal family (e.g. "FluxStd Sans"). Allowed and simple; the internal name is never user-visible. |
| **G** | **Go fonts** (already in our module graph: `golang.org/x/image/font/gofont`) | BSD 3-Clause (Bigelow & Holmes) | sans and mono, 4 styles each; **no serif** | not compatible (more cramped and wider glyphs; relies on the fit rule) | ≈ 130–180 KB per face; ≈ 1.2 MB for 8; ≈ 0.3 MB if subset (BSD: no rename duty) | Cleanest license, no new dependency. Times would render in a sans face: legible but visibly wrong. |
| **T** | TeX Gyre Heros / Termes / Cursor | GUST Font License = LPPL 1.3c legally, plus a non-binding rename request | 12 Latin faces | URW-derived, close to Helvetica/Times/Courier | ≈ 100–250 KB per face (verify) | LPPL clause 6 obligations on modified (subset) files, plus a URW lineage with a past Fedora licensing dispute. More legal surface than OFL for no gain. **Not recommended.** |
| **U** | URW base35 (Nimbus Sans, Nimbus Roman, Nimbus Mono PS, Standard Symbols PS, D050000L) | AGPL-3.0 with the PostScript/PDF font exception | all 14, incl. true Symbol and Dingbats shapes | the designs the base35 clones | ≈ 35 faces, several MB | The exception covers embedding in **documents**, not inclusion in software. Bundling in the MIT library or WASM binary puts AGPL data in it. **Not acceptable in the default build.** Possible only behind an opt-in tag (like `mupdf`) with the AGPL consequence documented. |
| **D** | DejaVu Sans (symbol coverage only) | Bitstream Vera license (permissive; modified versions must not use "Bitstream"/"Vera" names) + public-domain DejaVu changes | Greek, math operators, arrows, most of U+2700–27BF (Dingbats) | n/a (Symbol and Dingbats advances come from AFM) | ≈ 750 KB full; ≈ 60–90 KB subset to the ≈ 390 Symbol + Dingbats glyphs (renamed) | Compatible; notice file required. |
| **N** | Noto Sans Math + Noto Sans Symbols 2 (symbol coverage only) | SIL OFL 1.1 (check the RFN line of the release used) | math and Greek; Dingbats block | n/a | larger than D before subsetting | Compatible; alternative to D. |
| **H** | Host-supplied fonts (no bundled outlines) | n/a | whatever the host registers | n/a | 0 | Needs a **new public API** (Go `RegisterFallbackFont`, a WASM `registerFont`). Default output stays blank, so the "~90% out of the box" goal is not met. Good as an *addition* later. |
| **B** | Boxes or a built-in stroke font | ours (MIT) | all | positions only | ≈ 10–20 KB | Shows *where* text is but not *what*. Not a substitute; at most a debug mode. |

**Widths data.** Adobe's Core 14 AFM files (`APAFML`) allow use, copying,
modification and distribution free of charge, on four conditions:

- retain the copyright notices;
- do not distribute the AFM files without the license file;
- note modifications prominently;
- do not modify the license paragraph.

We would ship **generated Go tables** (name → width, FontBBox, ascent and
descent) derived from the AFMs:

- the generated file's header carries Adobe's notice and a "modified: widths
  extracted to Go tables" statement;
- `core/internal/std14/LICENSES/APAFML.txt` carries the full license (§11.1);
- the npm package's NOTICE repeats it.

Some reviewers call APAFML incompatible in wording with BSD-style licenses
(Adobe community thread). The obligations are notices only, but this is a
maintainer sign-off (**D4**). The alternative for the 12 Latin faces is the
substitute's own advances under option L, since Arial, TNR and Courier New
match the AFMs closely. That alternative is unavailable for Symbol and
ZapfDingbats, whose widths need the AFM.

**Glyph-name data.** We need ≈ 600 entries of the AGL (Annex D names, Symbol,
`zapfdingbats`), generated from `adobe-type-tools/agl-aglfn` (BSD 3-Clause),
with a notice. A full AGL map (≈ 4,300 entries) is not needed.

### 3.2 Shipping mechanism

| | Mechanism | Default wasm | Opt-out | API impact |
|---|---|---|---|---|
| S1 | `go:embed` in a `core/internal` package imported by `render` unconditionally | + faces | none | none |
| **S2** | Same, with the embed file behind `//go:build !nostd14` and a metrics-only stub otherwise (advances correct, no outlines, text invisible as today) | + faces | `-tags nostd14` | none |
| S3 | Separate module or package the host imports for its side effect (`import _ ".../fonts/std14"`) | + faces only if `wasm/main.go` imports it | omit the import | the registration hook is public; server users who forget the import silently get blank text |
| S4 | Host-supplied (option H) | 0 | n/a | new public API on Go and WASM |

### 3.3 Recommendation (maintainer to confirm)

- **D1, faces:** **L-s** (Liberation 2.x subset to the Annex D Latin set,
  renamed per OFL RFN) for the 12 Latin faces. Rationale:
  - best shape fidelity among permissive options;
  - OFL is designed for bundling;
  - ≈ 0.4–0.6 MB raw, ≈ 0.3 MB on the wire.
  - **Fallback if the maintainer wants zero new third-party font licenses:**
    **G**, with a serif gap.
- **D3, symbol faces:** **D** (DejaVu Sans subset for Symbol + ZapfDingbats,
  renamed), ≈ 60–90 KB. Or ship Symbol/ZapfDingbats metrics-only in Wave 1
  (advances correct, glyphs not drawn) and decide later.
- **D2, shipping:** **S2**. On by default on both targets (the Wave 1 goal),
  one opt-out tag for size-sensitive WASM embedders, no public API change.
  S4 (host fonts) can be added later without breaking S2.
- **D4, metrics:** accept APAFML with notices.
- **Subsetting tooling:** an offline, maintainer-run step with a pinned
  open-source subsetter (fontTools `pyftsubset`, MIT; plus a rename of the
  `name` table). Script and expected SHA-256 sums live in
  `testing/tools/std14fonts/`. The tool is not a build dependency, and its
  output is committed with the OFL text.
  - Alternative if no third-party tool is acceptable: write a minimal Go
    TrueType subsetter. Rejected as scope creep.

Estimated WASM impact of the recommendation, raw:

| Item | Size |
|---|---|
| `psfont` code | ≈ 100–150 KB |
| Encoding, AGL and AFM tables | ≈ 50–80 KB |
| Latin faces | ≈ 0.4–0.6 MB |
| Symbol faces | ≈ 0.06–0.09 MB |
| **Total** | **≈ 0.6–0.9 MB raw**, ≈ 0.35–0.5 MB brotli |

The current `wasm/fluxdocs.wasm` size must be recorded in Phase 0 so the delta
is measured, not estimated.

---

## 4. Integration

### 4.1 Packages

- **`core/internal/psfont`** (new, Core MIT, pure Go, no imports from
  `render`):
  - Files: `type1.go` (segments, eexec, token scan), `cff.go` (INDEX, DICT,
    charset, encoding, FDSelect), `t1charstring.go`, `t2charstring.go`,
    `outline.go`, `limits.go`.
  - Output: `type Font` with `Matrix() [6]float64`, `BuiltinEncoding() (names
    [256]string, ok bool)`, `GlyphByName(name) (GID, bool)`, `GlyphByCID(cid)
    (GID, bool)`, `NumGlyphs()`, and `Outline(gid, budget) (Outline, width,
    error)`.
  - `Outline` is a flat list of `{Op: move|line|cube, Pts}` in glyph units. All
    errors are package-internal sentinels (`errMalformed`, `errLimit`). Callers
    treat any error as "glyph not drawn".
- **`core/internal/std14`** (new):
  - The 14 canonical names, the alias table, `Normalize(baseFont) (Face,
    bool)`, `Width(face, glyphName) (float64, bool)`, `BBox(face)`.
  - Annex D encoding tables (Standard, WinAnsi, MacRoman, MacExpert, Symbol,
    ZapfDingbats) and the AGL subset.
  - Tables generated by `go generate` from the public sources (Annex D
    transcription, AFM, AGL); generator checked in.
  - Should the encoding tables instead live in `fontmodel`? See §4.6.
- **Substitute faces:** `core/internal/std14/faces` (embed, `!nostd14`) + a
  stub, per D2.
- **`core/render/glyphfont.go`:** `glyphFont` becomes a struct over a small
  unexported interface `glyphProgram`, with three implementations:
  - `sfntProgram`: today's code;
  - `psProgram`: wraps `*psfont.Font`;
  - `substituteProgram`: an `sfntProgram` over a shared, process-wide parsed
    face (`sync.Once`; `sfnt.Font` is safe for concurrent use with distinct
    `Buffer`s; each glyphFont has its own `Buffer`).

  `glyphFont` precomputes, for simple fonts, `code → (glyph ref, ok)` and
  `code → w0` (256 entries each) at load. `drawText` then uses `w0` instead of
  `advanceEm`.

**As shipped (Phases 1–2).** The real surface differs from the sketch above:

- **`core/internal/psfont`** files: `font.go` (types, `Font` accessors,
  matrix check), `type1.go`, `t1charstring.go`, `cff.go`, `cffstd.go`
  (standard strings, Expert charsets and encoding), `t2charstring.go`,
  `limits.go`. There is no `outline.go`.
  - `ParseType1(data []byte, opts Options) (*Font, error)` and
    `ParseCFF(data []byte, opts Options) (*Font, error)`. `ParseCFF` keeps
    references into `data` (charstrings are read through their INDEX on
    use), so the caller must not modify it.
  - `Options{Charge func(n int64) bool}`: called with the size of each
    decoded copy (PFB stripped, hex decoded, eexec and charstrings
    decrypted) or CFF lookup table (charset, names, name and CID maps, an
    estimate per entry) **before** allocating it; `false` aborts the parse.
    The renderer passes `budget.chargeDecoded`.
  - `Font`: `Matrix()`, `BuiltinEncoding() ([256]string, bool)`,
    `GlyphByName(name) (GID, bool)`, `GlyphByCID(cid uint32) (GID, bool)`
    (charset for a CID-keyed CFF, GID = CID otherwise), `CIDKeyed() bool`,
    `NumGlyphs() int`, and `Outline(gid) (Glyph, error)`. A `Font` is
    immutable and safe for concurrent `Outline` calls.
  - `Glyph{Segs []Segment, Width float64, Ops int, Matrix [6]float64}`:
    `Segment{Op: MoveTo|LineTo|CubeTo, P [3]Point}` in glyph units; `Width`
    in glyph units; `Matrix` is the validated glyph → text matrix (the
    FontMatrix, or the selected FD's for a CID-keyed CFF); `Ops` is the
    operator count, **set on error too** so the caller can charge the work.
  - **`psfont` never sees the render budget.** It enforces only its own
    §5.1 limits. The renderer charges `Glyph.Ops × charstringOpCost` (16)
    raster work per build, success or not (§5.2).
- **`core/internal/std14`** shipped in Phase 1 with **only the three Latin
  base encodings**: StandardEncoding, WinAnsiEncoding and MacRomanEncoding
  (`StandardEncoding()`, `StandardName(code)`, `BaseEncoding(name)`).
  - Hand-transcribed from Annex D Table D.2 and its notes, with size checks
    in `encoding_test.go`; no generator yet.
  - Tables are returned **by value** (copies), so callers cannot modify them.
  - Everything else listed above (the 14 names, aliases, `Normalize`,
    `Width`, `BBox`, MacExpert, Symbol and ZapfDingbats encodings, the AGL
    subset, generated tables) **moves to Phase 3**.
- **`core/render`:** `glyphProgram` has two implementations, `sfntProgram`
  and `psProgram` (`glyphps.go`); `substituteProgram` comes with Phase 3. A
  `psEntry` (one parsed program plus its per-render glyph cache) is shared by
  every `glyphFont` over the same program stream. Simple fonts precompute
  `code → GID` (256 entries) at load; `w0` is computed per glyph by
  `glyphFont.advance` (§2.1), not precomputed.
- **`fontmodel`** gained `Width(code)`, `HasWidths()`, `MissingWidth()
  (float64, bool)`, `Differences()`, `BaseEncoding()`, and
  `Options.SkipToUnicode` and `Options.Charge` (per `/W`/`/W2` item, §5.2).
  `Flags()` is not added yet (Phase 3 needs it). Extraction and redaction
  outputs are unchanged (§4.6).

### 4.2 Load path

`loadOneFont` dispatches on what the descriptor holds, by **content
sniffing**, because mislabeled `/FontFile3 /Subtype` values and Type 1 data
under `/FontFile3` occur:

| First bytes of the decoded program | Program |
|---|---|
| `0x00010000`, `true`, `OTTO`, `ttcf` | sfnt |
| `%!PS-AdobeFont`, `%!FontType1`, `0x80 0x01` | Type 1 |
| byte 0 = 1 and byte 2 (hdrSize) ≥ 4 | CFF |
| none | the key/subtype decides; failure → nil (today's behavior) |

- No program and not Type 3 → the substitute path (§2.4).
- **Lazy loading:** fonts are loaded on first `Tf`, not eagerly for every
  resource entry. Pages that declare many unused fonts (common in merged and
  Word documents) then do no Type 1/CFF parsing for them.
- **Per-render cache** keyed by the font dictionary's `parse.Reference`. The
  same font object listed under several names, or in several Form XObjects'
  resources, is parsed once per page render. `fontCache` in `form.go` is
  replaced by this reference-keyed cache.

**As shipped (Phase 1, `core/render/fontload.go`).** `fontLoader` holds two
reference-keyed caches per render: font dictionaries (`fonts`) and decoded
program streams (`progs`), so dictionaries sharing one program parse it once.
`fontCache` in `form.go` stayed, but now caches only per-form *scopes* (name
→ font) over the shared loader. Descriptor keys are tried in the order
`/FontFile2`, `/FontFile3`, `/FontFile`; the first that decodes wins. An
unrecognized program under `/FontFile` is parsed as Type 1, under the other
keys as sfnt. Each dictionary load costs `fontLoadCost` and is capped at
`maxFontLoads` per render (§5.2).

### 4.3 Reusing `fontmodel`

`render` loads `fontmodel.Load(r, fd, Options{Metrics: true, Decode: <budgeted
decode>})` for each simple font. It then reads `/Widths`, `/FirstChar`,
`/MissingWidth`, `/Differences`, the kind and the descriptor through **new
read-only accessors**:

- `Width(code) (float64, bool)`
- `MissingWidth() float64`
- `Differences() map[byte]parse.Name` (copy or read-only view)
- `BaseEncoding() (parse.Name, bool)`
- `Flags() (uint32, bool)`

None of these accessors change `Text`, `AltText`, `ByteText`, `Tables`,
`Advance`, `UnknownAdvance` or `Ink`.

A new `Options.SkipToUnicode` lets render skip parsing `/ToUnicode`, which it
does not need.

### 4.4 Font matrix

- Type 1 and CFF outlines go through the program's validated FontMatrix (§5.1),
  not a hard-coded 1/1000. Fonts with 2048-unit or skewed matrices exist.
- `/Widths` stay in 1/1000 units regardless (§9.6.2.1).
- sfnt outlines keep the existing `glyphPPEM = 1000` scheme.

### 4.5 `drawGlyph`

`drawGlyph` takes an `Outline` in glyph units plus the glyph → text matrix.
`segmentsToSubpaths` gains an `Outline` twin, sharing `flattenCubic` and
`flattenQuad`. Budget charges are unchanged in kind: points × `glyphPointCost`
via `chargePaint`, then `fillPath`.

**As shipped.** The twin is `outlineToSubpaths` (`glyphps.go`); every matrix
product and point transform is rounded explicitly (`mulExact`, `applyExact`).
Flattening differs by program:

- **CFF glyphs (Phase 2)** use size-adaptive cubic flattening
  (`flattenCubicAdaptive`): the fewest uniform segments, at most 16, that
  keep the polyline within 1/8 device pixel of the curve. It is FMA-free, so
  the segment count and points match on every target. At 16 segments it
  computes exactly `flattenCubic`'s points.
- **Type 1 glyphs, sfnt glyphs and paths** keep the fixed 16 segments per
  cubic, so Phase 2 did not change their hashes.
- **Proposal:** unify on adaptive flattening in one deliberate,
  hash-changing commit of its own, with the corpus diff reviewed like a phase
  (§7.2). It cuts fill work on small text and makes the three paths behave
  alike.

### 4.6 Consistency with extraction and redaction

- **Extraction** (`core/extract`) reads fonts only through `fontmodel.Text`
  (Load without Metrics). Nothing in this design touches that path, so
  `ExtractText` output stays **byte-identical**. This is a phase exit gate
  (§7.3).
- **Redaction's text model** (`core/internal/redact` over `fontmodel`
  `Advance`/`Ink`/`Unbounded`) is unchanged:
  - A Type 1 or CFF font is already classified as embedded (`hasFontFile`
    checks `FontFile`, `FontFile2`, `FontFile3`), so its ink stays `/FontBBox`
    ∪ em defaults.
  - A non-embedded font stays `substituted`, with `substitutedBBox` = [−300
    −450 1450 1250] in glyph units, whatever the `/BaseFont`.
- **Invariant this design adds:** every glyph a FluxDocs substitute draws, at
  the fit scale of §2.4 (≤ 1), lies inside `substitutedBBox` relative to its
  origin. A table test walks every glyph of every bundled face and asserts it
  (§7.1). The never-widen rule exists to keep this invariant.
  - If a face fails it, enlarge the box (a redaction design change, requires
    `testing/security/redaction` 100%), or exclude that glyph. Never ship
    silently.
- Redaction does not call the renderer. Redaction outcomes therefore cannot
  change. The corpus no-regression check (`FLUXDOCS_EXTRA_CORPUS` +
  `FLUXDOCS_EXTRA_BASELINE`) must still show zero differences.
- **AFM widths in `fontmodel`?** They would make `Advance` exact for
  standard-14 fonts without `/Widths` (today [0, 1.1 em]), which tightens
  redaction boxes and changes redaction outcomes. **Not in this increment**
  (Q4); the tables live in `std14` so the change can be made later deliberately.
- Embedded Type 1 or CFF glyphs that reach past a lying `/FontBBox` were already
  painted by other viewers. Redaction paints its box after the kept content,
  so stray ink cannot uncover redacted text. No clip is added (Q8).

---

## 5. Security bounds (SECURITY.md §2)

The font program is untrusted input. A malicious program must not panic, hang,
or allocate without bound. Any limit hit drops the **glyph** (or the font,
for structural limits), never the page. Only the cumulative render budget
produces an error.

### 5.1 Hard limits (`psfont/limits.go`; values for review)

| Limit | Value | Basis |
|---|---|---|
| Program bytes after PFB/hex/eexec | 32 MiB per font | decoded through `budget.decode`; this cap also bounds tests that run with an unlimited budget |
| `/Subrs` entries (Type 1) | 65,536 | INDEX counts are Card16 in CFF |
| `/CharStrings` entries (Type 1) | 65,536 | |
| Charstring length | 65,535 bytes | Type 2 limit; applied to Type 1 too |
| `lenIV` | −1 or 0–16; otherwise the font is rejected | |
| Subroutine nesting | 10 | Type 1 Ch. 6, TN #5177 limits |
| Operand stack | 24 (Type 1), 48 (Type 2) | per spec; overflow aborts the glyph |
| Type 1 PostScript stack (othersubr results) | 24 | |
| `callothersubr` n | ≤ current stack depth | |
| Flex points | exactly 7, else the flex is discarded | |
| Type 2 stem hints | 96 | TN #5177 |
| Transient array | 32 slots; `put`/`get` index checked | TN #5177 |
| Executed charstring ops per glyph (incl. subrs and `seac` components) | 16,384 | self-recursive or looping subrs end here; real glyphs use < 1,000 |
| Outline segments per glyph (before flattening) | 8,192 | |
| `seac` depth | 1 | |
| Coordinates | \|v\| ≤ 2^20 glyph units, finite; otherwise the glyph is aborted | prevents Inf/NaN downstream |
| FontMatrix | 6 finite entries, `\|det\|` > 0, each `\|entry\|` ≤ 1; otherwise the default 0.001 matrix | |
| CFF `offSize` | 1–4; otherwise the font is rejected | |
| CFF INDEX | count+1 offsets: first = 1, non-decreasing, last − 1 ≤ remaining bytes, computed in int64 | |
| CFF DICT | ≤ 48 operands per operator; real-number nibbles ≤ 64; DICT data inside the font | |
| CFF FDArray | ≤ 256 dictionaries; FDSelect ranges sorted, first = 0, every FD index < count | |
| CFF charset/encoding | entry totals ≤ nGlyphs; supplements ≤ 256 | |
| Cleartext `/Encoding` | ≤ 256 `put`s; codes outside 0–255 ignored | |

**As shipped (Phases 1–2).** The table above holds, with these refinements
and additions (`psfont/limits.go`, `type1.go`, `cff.go`, `t2charstring.go`):

- **Coordinates:** the ±2^20 bound applies to the current point **and to
  every emitted point, `seac` component offsets included**, and to curve
  control points. Widths (`hsbw`/`sbw` wx, the Type 2 width,
  `defaultWidthX`, `nominalWidthX`) must be finite and within the same
  bound, so a `div` chain cannot produce a ±Inf or NaN width; such a glyph or
  font is rejected.
- **Type 1 structure:** only the **first** `/Subrs` and the **first
  non-empty** `/CharStrings` are honoured; repeats (nested dictionaries,
  copies) are ignored, so they cannot multiply allocations. `/Subrs`
  indices at or past the **declared array size** (or 65,536) are dropped,
  so the table never grows beyond what the program declared. A duplicate
  glyph name keeps its first entry. A charstring longer than 65,535 bytes is
  kept as an empty, undrawable entry.
- **CFF Private DICTs:** the distinct Private DICT bytes read are capped at
  the program size. Private DICTs are cached by (offset, size) and local
  Subrs INDEXes by offset, so Font DICTs that share or overlap them cannot
  multiply the parse work.
- **CFF DICT:** only the operators the parser reads are kept, and only their
  **first occurrence**; a DICT allocates at most one operand list per kept
  operator whatever its size.
- **Type 2 charstrings:** the 65,535-byte limit applies to every charstring
  and subroutine taken from an INDEX. Interpreter bounds: argument stack 48,
  stems 96, transient array 32, subroutine depth 10, 16,384 executed
  operators and 8,192 segments per glyph; `random` returns 0.5.

### 5.2 Budget charges (extends `docs/design/render-budget.md`)

- **D (decoded bytes):**
  - the font stream decode (already, via `budget.decode`);
  - the PFB-stripped, hex-decoded and eexec-decrypted copies (`chargeDecoded`,
    each a new allocation of the program's size).
- **W (raster work):**
  - a new **`charstringOpCost`** per executed charstring operator, charged when
    a glyph is built. The initial value is 1, calibrated by
    `BenchmarkCalibration/charstring-ops` to ≈ 1 pixel visit per unit, like the
    other constants;
  - plus the existing `glyphPointCost` per flattened point and `fillPath`
    costs.
- **Outline cache:** built glyphs are cached per render, keyed by (font ref,
  GID), and capped at 1 Mi cached segments; past the cap, outlines are built
  each time and charged each time.
  - A cache hit pays only points and fill.
  - Charges stay deterministic for a given document and options (identical on
    native and wasm).
- **Polling:** font parsing polls the context once per font (it is O(bytes)
  and bounded by D).
- **Worst case:** 65,536 distinct glyphs × 16,384 ops ≈ 1 Gi op units. That is
  far above the W default (≈ 67 Mi at 150 DPI), so a glyph-bomb font trips the
  budget, not the clock.
- **Steps (O):** `MaxOperators` still counts one step per glyph shown or
  advanced; unchanged.

**As shipped (`core/render/budget.go`; values calibrated by
`BenchmarkCalibration`).**

| Constant | Value | Charge |
|---|---|---|
| `charstringOpCost` | **16** (not 1) per executed operator | W, per glyph build, success or not; `psfont` only reports `Glyph.Ops`. A glyph at the 16,384-op bound costs 256 Ki |
| `fontLoadCost` | 1,280 per font dictionary loaded | W; a dictionary served by the reference cache is free |
| `maxFontLoads` | 4,096 per render | past it the font is not loaded (its text is skipped); ≈ 5 KiB each, ≈ 20 MiB worst case |
| `maxCachedSegments` | **256 Ki** (not 1 Mi) | outline segments cached per render (one extra per cached glyph); a `psfont.Segment` is 56 bytes, so ≈ 14 MiB worst case per render. Past it glyphs are rebuilt and recharged |
| `cidWidthItemCost` | 28 per CIDFont `/W` or `/W2` item | W; an empty list still costs one item |

- **Decoded bytes (D):** every copy `psfont` reports through
  `Options.Charge` goes to `budget.chargeDecoded`, including the CFF lookup
  tables.
- **Composite metrics** are cached per render by {descendant CIDFont
  reference, Type 0 `/Encoding`}, so Type 0 dictionaries sharing one CIDFont
  read and pay for its `/W` once. `/W` ranges are indexed for binary search
  after the read; when ranges overlap, **the first match wins** (the order
  in which they appear).
- The CFF and Type 1 parses poll the render context once per program.

### 5.3 No-panic discipline

- Every byte read is index-checked.
- No recursion except bounded `callsubr` and `seac`; an explicit depth counter
  sits on the interpreter struct.
- No `panic` on input. Public entry points already recover to `ErrInternal`
  as the last line of defense (SECURITY.md §2), but the fuzz targets fail on
  any panic.

### 5.4 Fuzzing

- Native fuzz targets in `core/internal/psfont`:
  - `FuzzType1` (parse + build every glyph);
  - `FuzzCFF` (parse + build every glyph, CID and non-CID);
  - `FuzzT1Charstring` / `FuzzT2Charstring` (a raw charstring against a
    synthetic font with fuzzed Subrs).
- Seeds: the hand-built test fonts (§7.1) and targeted hostile cases:
  - a self-recursive subr; mutually recursive subrs;
  - a `callothersubr` with n > stack;
  - flex with 3 points; flex never ended;
  - `seac` of a `seac`;
  - `lenIV` 10000;
  - PFB lengths of 0xFFFFFFFF; hex eexec of odd length;
  - an INDEX with `offSize` 5 and with decreasing offsets; INDEX count × offSize
    overflow;
  - FDSelect pointing past FDArray;
  - a huge `hintmask` stem count;
  - `random`/`sqrt`/`div` by zero.
- PDF-level seeds in `testing/security/fuzz/` embed these fonts. DoS gates in
  `testing/security/dos/`:
  - a Type 1 glyph bomb shown 10⁵ times must trip `ErrRenderBudgetExceeded`
    (paint work) or finish under the deadline;
  - a 10,000-entry `/Font` dictionary pointing at one 30 MiB program parses it
    once (reference cache) and only for fonts actually selected (lazy load).

---

## 6. WASM and tiers

- **Tier:** Core (MIT). No Pro/Cloud imports, no CGO, no build tag except the
  optional `nostd14` (D2) and, only if the maintainer wants it, an opt-in
  AGPL `urwfonts` tag (option U), documented like `mupdf`.
- **Privacy path:** no network access. Substitute faces are compiled in (S2).
  If S4 is added later, fonts come from the host and nothing leaves the page.
- **Size:** see §3.3. `psfont` and the tables are always linked: Type 1/CFF
  support has no build tag, since it is needed for the ~90% goal and costs
  ≈ 0.15–0.25 MB raw.
- **Determinism:** explicit float64 rounding (§2.2) and a constant `random`
  (§2.3) keep pixel output identical on amd64, arm64 and wasm.
  - Existing code may already differ on arm64 because of FMA fusion. Phase 0
    records whether render hashes on arm64 match amd64/wasm today, so later
    diffs are attributed correctly.
  - **Status after Phase 2:** the glyph pipeline (`mulExact`, `applyExact`,
    the `psfont` interpreters, `flattenCubicAdaptive`) and `flattenCubic`
    (made FMA-free in `c1b0f59`) are FMA-free. **`raster.blend` still
    contains fusable multiply-adds**, so arm64 pixel parity with amd64/wasm
    is **not guaranteed**. Parity is gated on amd64 vs wasm only.
- **Licenses in distribution:**
  - `THIRD_PARTY_NOTICES` at the repo root and in `packages/core` (the npm
    package carrying `fluxdocs.wasm`) lists OFL 1.1 (Liberation-derived
    faces), the Bitstream Vera license (DejaVu-derived symbol face), APAFML
    (AFM-derived tables) and BSD 3-Clause (AGL data).
  - The code stays MIT.
  - Spec §18.4 gains a paragraph.

---

## 7. Testing

### 7.1 Unit (table-driven, deterministic; no binary goldens)

**Hand-built test fonts, built by us in test code with no third-party tools:**

- A `psfont` test helper (`testfont_test.go`) assembles:
  - **Type 1:** a cleartext header, a Private dict and CharStrings with
    Type 1-encoded numbers, charstring encryption (r = 4330, `lenIV` 4 with
    fixed bytes), and eexec encryption (r = 55665, fixed 4-byte prefix).
    Variants: binary, hex, PFB-wrapped, wrong `/Length1`.
  - **CFF:** an INDEX writer, a DICT encoder and Type 2 charstrings; non-CID
    and CID-keyed (2 FDs, FDSelect formats 0 and 3).
- Glyphs, all with outlines computable by hand:
  - a square;
  - a square with a square hole (winding);
  - a glyph via `callsubr` (Type 2: `callgsubr` too, with each bias tier: 107,
    1131 and 32768 by padding the INDEX);
  - flex (OtherSubrs 0/1/2) and `hflex`/`flex1`;
  - hint replacement (OtherSubr 3);
  - `div`;
  - `seac` / `endchar`-seac composing a base and an accent at a hand-computed
    offset;
  - `hintmask` with 9 stems (2 mask bytes).

**Cipher known-answer test:** a hand-computed 4-byte eexec and charstring
vector, written out in the test with the arithmetic in comments. Encrypter and
decrypter cannot be wrong symmetrically and still pass.

**Assertions:**

- `Outline` segments exactly equal the expected list (glyph units);
- advance/width;
- built-in encoding;
- name → GID and CID → GID;
- every §5.1 limit at limit (passes) and limit + 1 (glyph or font dropped,
  no panic).

**Render level** (`core/render/glyph_test.go` style, 72 DPI):

- ink bounding box of a hand-built square glyph equals the expected pixel
  rectangle ±1 px;
- counters hollow;
- the accent above the base for `seac`;
- `/Widths` overrides the program advance (two glyphs with a 2× `/Widths`
  entry land at the expected x);
- `/Differences` selects a renamed glyph;
- invisible mode draws nothing;
- FontMatrix 1/2048 renders at the same size as 1/1000 with scaled
  coordinates.

**Standard 14:**

- each of the 14 names, the aliases and the suffix forms normalize as
  tabulated;
- with `/Widths` absent, the advance equals the AFM width;
- `TestGlyph_MissingFontDoesNotCrash` changes: non-embedded Helvetica now
  draws ink (expected; the test is rewritten, not deleted);
- a symbolic non-embedded unknown font draws nothing;
- with `-tags nostd14`, text is invisible but advances still use AFM widths.

**Substitute-box gate** (§4.6): every glyph of every bundled face, at fit
scale ≤ 1, lies inside `substitutedBBox`. This is a redaction-consistency gate
and runs in the normal CI.

**As shipped: font builders.** The hand-built fonts live in two
non-test packages so render tests, fuzz seeds and DoS gates share them:

- `core/internal/psfont/t1build` (Type 1: cleartext, Private dict,
  charstring and eexec encryption, binary/hex/PFB variants) and
  `core/internal/psfont/cffbuild` (CFF: INDEX and DICT writers, Type 2
  charstrings, CID-keyed with FDArray/FDSelect). Both are `core/internal`, so
  `testing/` cannot import them.
- `testing/internal/t1min` and `testing/internal/cffmin`: the small subsets
  the gates in `testing/security/dos/` and the seeds in
  `testing/security/fuzz/` need.
- Fuzz targets: `FuzzType1`, `FuzzType1Private`, `FuzzT1Charstring`,
  `FuzzCFF`, `FuzzT2Charstring`.

**Optional real-font test:** commit one small OFL-licensed Type 1 font (e.g.
an AMS Computer Modern `.pfb`, ≈ 30 KB, OFL) as testdata (Q5). *As of Phase 2
no such file is committed* (`core/internal/psfont/testdata/` does not exist);
Q5's decision is not yet implemented.

**Ghostscript oracle** (non-blocking job, already provisioned for redaction):
render the hand-built-font PDFs with `gs -sDEVICE=pnggray -r72` and compare
the ink bounding boxes with ours (±1 px). This is an independent check of the
cipher, charstring and `seac` semantics. gs is an external tool only; nothing
is linked or vendored.

### 7.2 Corpus

**As shipped: the corpus.** The phases ran against **1,491 deduplicated
system PDFs** (skewed towards macOS-bundled documents), not the 772-file list
named below. Read "772-file list" in §7.2, §7.3 and §8 as this corpus. Render
hashes are **SHA-256 of the decoded pixels**, not of the PNG bytes, so encoder
changes cannot show up as render diffs. Census results are in §10.

**Counts (original plan).** The 772-file list referred to in the task
(`…/scratchpad/x/`) was **not present** when this note was written (the
scratchpad was empty). This environment also has no shell, so the
Type 1/standard-14 counts are **not yet measured**. Phase 0 measures them
with a census test, `TestFontCensus` in `testing/corpus/`:

- gated by `FLUXDOCS_EXTRA_CORPUS`, reusing the redaction regress list format;
- walks every page's `/Font` resources, including those of Form XObjects, and
  counts per file and per page: TrueType embedded, OpenType, Type1C,
  CIDFontType0C, Type 1 embedded, standard-14 not embedded, other not
  embedded, Type 3, TrueType without `cmap`, symbolic (3,0)-only TrueType;
- emits a TSV of counts only: no text, no font names beyond the 14 standard
  names (SECURITY.md §4 spirit).

Known now: all 4 text-bearing files in `testing/corpus/clean/` (`text.pdf`,
`multipage.pdf`, `rotated.pdf`, `flate.pdf`) use non-embedded Helvetica.
`vector.pdf` and `form_xobject.pdf` have no text.

**Render hashes.** Record PNG SHA-256 per page (72 and 150 DPI, ≤ 8 pages per
file) before each phase. After each phase:

- **Pages with no font of the phase's kind:** byte-identical. Any diff is a
  bug.
- **Pages with such fonts:** expected to change.
  - Review a stratified sample (≥ 20 pages per font kind, plus every page whose
    ink grows by > 3× or that now trips a budget).
  - Use a side-by-side tool against Ghostscript output, computing ink-mask IoU
    at 72 DPI as a triage metric; < 0.6 → manual look.
- **Phase 1's `/Widths` change** affects TrueType pages too. It lands in a
  **separate commit** so its diffs are attributed: expected to be small
  (`hmtx` usually equals `/Widths`), and every changed page is listed in the
  PR.
- No corpus page may newly trip the default render budget. Record render time
  per page; the p95 regression must be < 25%.

**Waiver (approved by the maintainer, 2026-11-12).** Two Phase 2 results
break the rules above and are accepted:

- **Budget:** page 0 of `fbm_howToUse_2.pdf` (a macOS New Oxford American
  Dictionary asset) overprints ≈ 2.5k glyphs about 40 times: 58,777
  text-showing operators, ≈ 102k glyph fills. Before Phase 2 its CFF text was
  not drawn; now it **exceeds the default render budget at 72 DPI** (it still
  renders at 150 DPI, whose default budget is larger). The cost is real
  paint work, not a parser bug, so the budget is not raised for it.
- **Render time:** on pages whose fonts are all CFF, p95 render time rose
  **+40%**, because those pages now draw their text. Corpus-wide p95 rose
  **+2%**, within the 25% rule.

### 7.2.1 Results

| Phase | Programs | Pages changed | Ghostscript ink F1 (72 DPI) | (150 DPI) |
|---|---|---|---|---|
| P1 (Type 1) | the corpus's embedded Type 1 fonts (3 files, §10) | 99 pages in 3 files (argon2-specs 20, GS9_Color_Management 40, Ghostscript.pdf 39) | 0.161 → 0.939 | 0.132 → 0.964 |
| P2 (CFF) | 1,157 CFF programs parsed, **0 failures** | 246 pages in 154 files, all with CFF fonts | 0.660 → 0.921 | 0.676 → 0.945 |

F1 is over the Type 1 pages (P1) and the changed CFF pages (P2). No Phase 2
page scored worse against Ghostscript than before.

**Known differences from Ghostscript** (open, not phase blockers):

- **Thin stems at 72 DPI.** Our anti-aliased rasteriser drops thin stems
  that Ghostscript keeps, because Ghostscript applies hinting. Fixing it needs
  dropout control in the filler, which **changes every font hash** in the
  corpus; it would be its own deliberate commit.
- **Black vs gray text.** Some text in the PhpStorm reference-card PDFs
  renders black where Ghostscript renders it gray. Cause unknown; not yet
  investigated.

**Goldens:** `core/render/testdata/golden/` is empty today. No binary goldens
are added (assertions above are pixel-geometric) unless the maintainer wants a
small set (Q9).

### 7.3 Phase exit gates (all phases)

- `ExtractText` / `ExtractTextByPage` output over `testing/corpus` and the
  772-file list: byte-identical hashes before and after.
- `testing/security/redaction/` 100%, and the redaction corpus no-regression
  check: zero differences against the recorded baseline.
- `go test -race ./...`, `CGO_ENABLED=0 go build ./...`, `GOOS=js GOARCH=wasm
  go build ./core/... ./wasm/...`; wasm smoke render of a Type 1 page and a
  standard-14 page, hash-equal to native (on amd64).
- Fuzz targets run ≥ 10 min each with no finding; seeds committed.
- WASM size delta recorded in the PR description.

---

## 8. Phasing

| Phase | Content | Depends on | Exit |
|---|---|---|---|
| **P0** | `TestFontCensus`; baseline render, extract and redaction hashes for the 772-file list; current wasm size; arm64 vs amd64 hash parity check | — | counts table appended to this note (§10) |
| **P1** — **done 2026-11-12** (`4929f76`, `a1fb7ca`) | Pipeline refactor (`glyphProgram`, lazy load, reference cache, code → name → glyph, w0 from `/Widths`); `std14` encoding/AGL tables; Annex D base encodings; `psfont` Type 1 parser + Type 1 charstrings; budget charges; fuzz + DoS gates. The `/Widths` advance change goes in its own commit. *Shipped:* `std14` holds only Standard/WinAnsi/MacRoman; the AGL, MacExpert, Symbol and ZapfDingbats tables moved to P3 | — | §7.3; LaTeX/pdfTeX pages render (Type 1 F1 0.161 → 0.939 at 72 DPI, §7.2.1) |
| **P2** — **done 2026-11-12** (`7ef8e90`) | `psfont` CFF + Type 2 (Type1C, CIDFontType0C); optional `seac` retry for OpenType-CFF glyphs. *Shipped:* composite CFF/Type 1 advances from `/W`/`/DW`; adaptive flattening for CFF. *Still open:* the OpenType-CFF `seac` retry (rare) | P1 | §7.3 with the §7.2 waiver; Distiller/InDesign pages render (CFF F1 0.660 → 0.921 at 72 DPI, §7.2.1) |
| **P3** | Standard-14 and non-embedded substitution: name normalization, flag mapping, AFM widths, bundled faces per D1–D3, `nostd14` tag, substitute-box gate, notices (§11.1) | P1 (D1–D4 decided, §11) | §7.3; corpus `clean/*.pdf` text visible |
| **P4** | TrueType gaps: §9.6.6.4 cmap selection incl. (3,0) + 0xF0xx, `/Differences` via `post` names (name → GID map built once, ≤ NumGlyphs), `/CIDToGIDMap` streams (bounded: 2 bytes × CIDs ≤ stream length) | P1 | §7.3 |
| later | Type 3 (own note), TrueType without `cmap`, document-level font cache, host fonts (S4), MM, vertical | — | — |

Spec updates once decisions land: §4.1 (rendering row: font formats), §9.1
(Wave 1 list), §13.2 (font budget charges), §18.4 (font licenses); refresh the
package doc in `core/render/render.go` and the `glyphFont` comment.

---

## 9. Open questions

D1–D4, Q5 and Q9 are decided (§11); their entries below are kept for the
record. Still open: Q6, Q7, Q8; deferred: Q4, Q10.

- **D1 [maintainer, blocks P3]: Latin substitute faces.** Recommendation
  **L-s** (Liberation 2.x subset, renamed, OFL 1.1, ≈ 0.4–0.6 MB). Alternatives:
  **G** (Go fonts, BSD, no serif), **L** unsubset (≈ 4.2 MB), none (metrics
  only).
- **D2 [maintainer, blocks P3]: shipping.** Recommendation **S2** (embedded,
  on by default, `-tags nostd14` opt-out). Alternatives: S1, S3, S4.
- **D3 [maintainer, blocks Symbol/ZapfDingbats in P3]: symbol faces.**
  Recommendation **D** (DejaVu subset, renamed, ≈ 60–90 KB). Alternatives:
  **N** (Noto, OFL), or metrics-only in Wave 1.
- **D4 [maintainer, blocks P3]:** accept the Adobe Core 14 AFM license
  (APAFML) for generated width tables with notices, or derive Latin widths from
  the substitute (option L only) and drop Symbol/ZapfDingbats widths.
- **Q5 [maintainer]:** commit one third-party OFL Type 1 font (AMS CM) as
  testdata, or keep only our hand-built fonts.
- **Q6:** the substitute fit rule (±10% tolerance, compress-only to ≥ 0.6).
  Non-blocking; tunable after the corpus review.
- **Q7:** a document-level parsed-font cache. Re-parsing a 100 KB Type 1 font
  per page is ≈ sub-millisecond, but a 2,000-page book multiplies it. It needs
  a concurrency-safe cache bounded in memory. Performance follow-up.
- **Q8:** clipping embedded-font glyphs to `fontmodel.Ink` so FluxDocs' own
  render never paints outside the redaction model. Recommendation: no (§4.6).
- **Q9 [maintainer]:** a small set of binary PNG goldens for Type 1/CFF/std14
  pages, or geometric assertions only (recommended).
- **Q10 [maintainer, later]:** an opt-in AGPL `urwfonts` tag (option U) for
  users who want the base35 designs, documented like `mupdf`.
- **Q4 (later, needs a redaction design review):** use AFM widths in
  `fontmodel.Advance` for standard-14 fonts without `/Widths` (tighter
  redaction boxes).

Follow-ups raised by Phases 1–2 (none blocks Phase 3; each changes render
hashes and so needs its own commit and corpus review):

- **F1:** unify cubic flattening on the adaptive rule for Type 1, sfnt and
  paths (§4.5).
- **F2:** composite TrueType advances from `/W`/`/DW` (§2.1), proposed for
  P4.
- **F3:** dropout control for thin stems at 72 DPI (§7.2.1); changes every
  font hash.
- **F4:** black-vs-gray text in the PhpStorm reference cards (§7.2.1); cause
  unknown.
- **F5:** remove the remaining FMAs in `raster.blend` if arm64 parity is
  wanted (§6).
- **F6:** the OpenType-CFF `seac` retry (§1.1, P2 optional; rare).
- **F7:** Q5 is decided but not implemented: no AMS Computer Modern font is
  committed yet (§7.1).

---

## 10. Census results

Phase 0 (`TestFontCensus`) over the 1,491-file corpus (§7.2). A file that
uses several kinds counts under each, so shares do not sum to 100%. Page
counts are not reproduced here.

| Font kind | Files | Share of 1,491 | Phase |
|---|---|---|---|
| TrueType, simple (embedded) | 201 | 13.5% | P1 widths, P4 |
| Type1C (bare CFF, simple) | 150 | 10.1% | P2 |
| CIDFontType0C (bare CFF, composite) | 86 | 5.8% | P2 |
| CIDFontType2 (composite TrueType) | 23 | 1.5% | P4 |
| Type 1, embedded (`/FontFile`) | 3 | 0.2% | P1 |
| Standard 14, not embedded | 4 | 0.3% | P3 |
| Other simple font, not embedded | 15 | 1.0% | P3 (substitute) |
| TrueType without `cmap` | 13 | 0.9% | P4 |
| Type 0, not embedded | 1 | 0.1% | — |
| OpenType (`/FontFile3 /OpenType`), Type 3, MMType1, (3,0)-only TrueType, Type 1 data under `/FontFile3` | 0 | 0% | — |

The corpus is macOS-skewed. Type 1 and non-embedded standard-14 fonts are
likely rarer here than in a LaTeX- or report-generator-heavy population, so
these shares should not be read as Phase 1's or Phase 3's real-world reach.

**WASM size** (`wasm/fluxdocs.wasm`, gzip):

| Build | Bytes (gzip) | Delta |
|---|---|---|
| Baseline, `01d47a9` | 2,352,828 | — |
| After Phase 1 | 2,388,146 | +35,318 |
| After Phase 2 | ≈ 2,420,319 | ≈ +32,173 |

---

## 11. Decisions (maintainer-approved, 2026-11-11 and 2026-11-12)

These are settled. Implement them as written and do not reopen them without
the maintainer. **Phase 3 is unblocked.**

- **D1. Latin faces:** Liberation Sans, Serif and Mono 2.x (SIL OFL 1.1),
  subset to the ISO 32000-1 Annex D Latin set (option L-s).
  - The internal family names are renamed (e.g. "FluxStd Sans", "FluxStd
    Serif", "FluxStd Mono"), as the OFL Reserved Font Names require:
    "Liberation", "Arimo", "Tinos" and "Cousine" must not appear in the
    `name` table of a subset.
  - The subsetting script and expected SHA-256 sums live in
    `testing/tools/std14fonts/` (§3.3).
- **D2. Shipping:** the faces are embedded in an internal package
  (`core/internal/std14/faces`). They are on by default on native and WASM.
  - `-tags nostd14` swaps in a metrics-only stub: advances stay correct and
    glyphs are not drawn.
  - No public API change.
- **D3. Symbol and ZapfDingbats faces:** a DejaVu Sans subset (Bitstream Vera
  license plus public-domain DejaVu changes), renamed internally. Per the Vera
  license, "Bitstream" and "Vera" must not appear in the family name.
- **D4. Metrics:** the Adobe Core 14 AFM license (APAFML) is accepted for the
  generated width tables, on its conditions:
  - Adobe's copyright notice is kept in the generated file's header;
  - the license file ships with the tables;
  - the modification ("widths extracted to Go tables") is stated in the header.
- **Q5. Real-font test:** yes. Commit one OFL AMS Computer Modern Type 1
  font (`.pfb`, ≈ 30 KB) under `core/internal/psfont/testdata/`, with its
  OFL license and copyright next to it.
- **Q9. Goldens:** geometric pixel assertions only, no binary PNG goldens.
- **Phase 3 sources (approved 2026-11-12):** the implementer may download
  Liberation 2.x, DejaVu and the Adobe Core 14 AFM files from their official
  upstream distributions, as inputs to the offline subsetting and generation
  steps (§3.3, §11.1).
- **§7.2 budget waiver (approved 2026-11-12):** `fbm_howToUse_2.pdf` page 0
  may exceed the default budget at 72 DPI (§7.2).
- **Deferred, not decided:**
  - Q10: an opt-in AGPL `urwfonts` tag;
  - Q4: `fontmodel.Advance` using the standard-14 metrics. This needs a
    redaction design review first.

### 11.1 Third-party notices plan

The license texts sit next to the data they cover, verbatim and unmodified:

| File | Covers |
|---|---|
| `core/internal/std14/LICENSES/OFL-1.1-Liberation.txt` | Liberation-derived faces: OFL 1.1 text plus the Liberation copyright lines (Google, with Reserved Font Names Arimo/Tinos/Cousine; Red Hat, with Reserved Font Name Liberation) and a note that the files are renamed subsets |
| `core/internal/std14/LICENSES/Bitstream-Vera-DejaVu.txt` | DejaVu-derived symbol face: Bitstream Vera license, the DejaVu public-domain statement, and a note that the file is a renamed subset |
| `core/internal/std14/LICENSES/APAFML.txt` | AFM-derived width tables: the Adobe Core 14 AFM license paragraph, unmodified |
| `core/internal/std14/LICENSES/BSD-3-Clause-AGL.txt` | Adobe Glyph List data used for the name-to-Unicode tables |
| `core/internal/psfont/testdata/OFL-1.1-AMS.txt` | the AMS Computer Modern test font (test data only, not linked into any binary) |

The generated Go files and the embedded font files each carry a header comment
or `name`-table copyright string pointing to their license file. OFL accepts
the font's own `name` table as the in-binary notice.

`THIRD_PARTY_NOTICES.md` (new, repo root) lists each component with:

- its source and version;
- its license;
- the path of its license text;
- what was modified (subset, rename, table extraction).

The notices file ends by stating that FluxDocs code is MIT and that the
bundled font data keeps its own licenses.

References to the notices:

- **README:** the license section gains one paragraph pointing to
  `THIRD_PARTY_NOTICES.md`. It names the font data licenses (OFL 1.1,
  Bitstream Vera, APAFML, BSD 3-Clause) and says the `nostd14` build leaves
  the font faces out.
- **npm (`packages/core`):** the package that ships `fluxdocs.wasm` includes a
  copy of `THIRD_PARTY_NOTICES.md` and the four `LICENSES/*.txt` files, listed
  in `package.json` `files`. `@fluxdocs/react` and `@fluxdocs/vue` refer to
  it from their READMEs.
- **CI check:**
  - if any file under `core/internal/std14/faces` or the generated tables
    changes, `THIRD_PARTY_NOTICES.md` must be updated in the same PR (a
    file-presence and checksum test in `core/internal/std14`);
  - a test asserts that the reserved names do not occur in any embedded face's
    `name` table.
- **Spec:** §18.4 gains a short paragraph on bundled font licenses when
  Phase 3 lands.
