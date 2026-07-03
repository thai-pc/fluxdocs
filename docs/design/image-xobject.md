# Design: Image XObject rendering (Wave 1)

Status: design (docs only). Tier: **Core (MIT)**. Package: `core/render`.
Spec basis: ISO 32000-1:2008 §8.9 (Images), §8.10 (Form and image XObjects),
§8.8 (external objects / `Do`). Clean-room — derived from the public ISO spec
only.

This note tells the implementer exactly what to build. It does not contain Go
code. Hand off to `go-implementer`.

---

## 1. Scope

Render **image XObjects** invoked by the `Do` operator, for the common ~90% of
real PDFs.

In scope this increment:

- `Do` resource lookup for XObjects whose `/Subtype` is `/Image`.
- Sample sources: **DCTDecode** (baseline JPEG via stdlib), **FlateDecode / no
  filter** raw samples in **DeviceRGB (3×8)** and **DeviceGray (1×8)**.
- **`/ImageMask`** stencils (1 bpc) painted in the current fill colour.
- **`/SMask`** soft-mask alpha (DeviceGray, 8 bpc), optional.
- Placement via the current CTM, nearest-neighbour sampling, source-over
  compositing.

Explicitly **out of scope** (defer, must degrade gracefully — no crash, rest of
page still renders):

- **Form XObjects** (`/Subtype /Form`) — nested content streams and their own
  resources/matrix. Skip for now (see open question O5).
- Filters: **CCITTFaxDecode, JBIG2Decode, JPXDecode, LZWDecode, RunLengthDecode**.
- Colour spaces: **Indexed, ICCBased, Separation, DeviceN, CalRGB/CalGray, Lab,
  Pattern**. (ICCBased/Indexed are the most likely Wave-2 follow-ups.)
- BitsPerComponent other than 1 (mask only) and 8. No 2/4/16-bit samples.
- `/Mask` (colour-key or explicit stencil mask), `/Decode` on non-mask images
  beyond the identity default, blend modes, `/Matte`.

Any image the decoder cannot handle is **skipped**: `Do` becomes a no-op for it.

---

## 2. Resource lookup and the `Do` operator

`Do` takes one operand: a `/Name`. That name is resolved against the **current
resource dictionary**, path `/Resources /XObject /<name>` (§7.8.3, §8.8).

Problem: the interpreter today has **no access** to the `Resolver` or the page
`/Resources`. `run(rs, base, ops, fonts)` only receives fonts. To implement
`Do` the interpreter must be able to resolve XObject streams.

Required plumbing change (flag for implementer):

- Extend `interp` with two fields: `res *parse.Resolver` and
  `resources parse.Dict` (the page's `/Resources`, already available as
  `pi.Resources`).
- Extend `run(...)` (and its call site in `render.go` `Page`) to pass the
  resolver and resources through.

Lookup steps for `Do <name>`:

1. `res.ResolveDict(resources["XObject"])` → the XObject sub-dictionary. If
   absent, no-op.
2. Resolve `xobjDict[name]` to a `*parse.Stream` (via `res.Resolve`). If it is
   not a stream, no-op.
3. Read `stream.Dict.GetName("Subtype")`. If `/Form` → skip (out of scope). If
   `/Image` (or absent but the dict has `/Width`+`/Height`, be lenient) →
   proceed to decode + draw.

Wave 1 does not model nested resources, so the page `/Resources` is the only
resource scope. (Form XObjects would introduce their own `/Resources`; deferred.)

---

## 3. Image dictionary keys (§8.9.5, Table 89)

| Key | Use in Wave 1 |
|---|---|
| `/Width` (int, required) | sample columns `W` |
| `/Height` (int, required) | sample rows `H` |
| `/BitsPerComponent` (`/BPC`) | 8 for colour/gray; 1 for `/ImageMask`. Others → skip |
| `/ColorSpace` (`/CS`) | `/DeviceRGB` (`/RGB`), `/DeviceGray` (`/G`), `/DeviceCMYK` (`/CMYK`). Array forms (Indexed/ICCBased) → skip (see O2). Absent when `/ImageMask true` |
| `/Filter` (`/F`) | branch the decode path (see §4). Read from the stream dict directly |
| `/ImageMask` (`/IM`, bool) | true → 1-bpc stencil, paint in current fill colour |
| `/Decode` (`/D`, array) | honoured **only for image masks** (`[0 1]` default vs `[1 0]` invert). Ignored elsewhere in Wave 1 |
| `/SMask` (stream) | optional soft-mask alpha; DeviceGray 8 bpc (§11.6.5.3) |
| `/Interpolate` | ignored (nearest-neighbour regardless) |

Abbreviated inline-image key names are listed for completeness; XObject image
dicts normally use full names. Accept both.

---

## 4. Wave 1 support matrix and decode paths

**Critical constraint:** `parse.DecodeStream` currently errors on any non-Flate
filter, and `parse.nameList` is unexported. Therefore the image decoder in
`render` must **read `stream.Dict["Filter"]` itself**, normalise it (a single
`Name`, or an `Array` of `Name`), and branch:

| `/Filter` (sole/last) | Colour handling | Decode path |
|---|---|---|
| `DCTDecode` / `DCT` | as decoded by JPEG | Pass **`stream.Raw`** straight to `image/jpeg.Decode`; read pixels from the returned `image.Image`. Do **not** call `DecodeStream` |
| `FlateDecode` / `Fl`, or **no filter** | DeviceRGB 3×8, DeviceGray 1×8, or ImageMask 1×1 | `parse.DecodeStream(stream)` (it already inflates + applies PNG/TIFF predictors), then slice raw samples per BPC/CS |
| `CCITTFaxDecode`, `JBIG2Decode`, `JPXDecode`, `LZWDecode`, `RunLengthDecode` | — | **Unsupported → skip** |

Notes and edge rules:

- **Filter chains:** handle the common single-filter case. If `DCTDecode` is
  preceded by another filter (e.g. `[FlateDecode DCTDecode]`), that is rare;
  treat as unsupported and skip rather than mis-decode.
- **DeviceGray 1×8 / DeviceRGB 3×8 raw layout:** rows are packed MSB-first with
  each **row byte-aligned** (row stride `= ceil(W * BPC * nComp / 8)`). Compute
  the stride explicitly; do not assume `W*nComp` with no padding.
- **CMYK JPEGs:** baseline `image/jpeg` returns `image.CMYK`; Adobe-marker
  inversion is a known wrinkle. Convert CMYK→RGB with the same formula already
  used in `interpret.go` `cmyk()`. If colours look inverted on the Adobe-marker
  case, that is acceptable Wave-1 fidelity (see O3); still must not crash.
- **`/ImageMask true`:** 1 bpc, no colour space. Sample bit selects paint vs
  transparent. Default `/Decode [0 1]`: sample value **0 ⇒ paint** with the
  current fill colour, **1 ⇒ transparent**. `/Decode [1 0]` inverts. This is the
  only place `/Decode` is honoured.

---

## 5. Placement math (unit square → device)

Per §8.9.5.2, an image is mapped so that it fills the **unit square** `[0,1]²`
in image space, and that square is placed by the current CTM. Image **sample row
0 is the TOP** of the image; PDF image space has its origin at the **bottom-left**
with `v` increasing upward. So the sample grid is y-flipped relative to image
space.

Map from sample pixel `(col, row)` (0-based, `col ∈ [0,W)`, `row ∈ [0,H)`) to the
unit square, using **pixel centres**:

```
u = (col + 0.5) / W
v = 1 - (row + 0.5) / H        # flip: row 0 → v ≈ 1 (top)
```

Then device coordinates are `CTM.apply(u, v)` using the existing `matrix` type.

Equivalently, fold the flip into one affine `S` and compose with the CTM:

```
S = matrix{ 1/W, 0, 0, -1/H, 0, 1 }     # (col,row) -> (col/W, 1 - row/H)
imgToDevice = S.mul(in.gs.ctm)          # sample-space -> device pixels
```

`S` maps `(col,row)` to unit-square `(u,v)` at the pixel's **top-left**; add the
`+0.5` centre offset either in `S`'s translation or when sampling.

**Recommended rasterisation: inverse mapping** (gap-free, standard):

1. Compute the image's device bounding box by transforming the four unit-square
   corners `(0,0),(1,0),(1,1),(0,1)` through `in.gs.ctm`; take min/max in x and
   y; clamp to `[0, rs.w) × [0, rs.h)`. If the box is empty, no-op.
2. Invert `imgToDevice` once (`inv`). For each device pixel centre `(dx+0.5,
   dy+0.5)` in the bounding box, `(fcol, frow) = inv.apply(...)`; take
   `col = floor(fcol)`, `row = floor(frow)`. If inside `[0,W)×[0,H)`, fetch the
   sample and composite.

This requires a **matrix inverse helper** on the `matrix` type, which does not
exist yet (flag for implementer). Standard 2×3 inverse:

```
det = a*d - b*c                     # guard |det| < epsilon -> skip image
ia =  d/det;  ib = -b/det
ic = -c/det;  id =  a/det
ie = -(e*ia + f*ic)
if_= -(e*ib + f*id)
inv = matrix{ia, ib, ic, id, ie, if_}
```

(Forward mapping — iterate source pixels and stamp each pixel's device footprint
— is an acceptable alternative when the implementer prefers it, but it needs
footprint fill to avoid gaps on upscaling. Inverse mapping is cleaner and reuses
the existing per-device-pixel `blend`.) Nearest-neighbour sampling is fine for
Wave 1; no bilinear.

---

## 6. Compositing

Per-device-pixel, source-over onto the existing surface via the existing
`raster.blend(x, y, color.RGBA)`:

- **Opaque image** (no `/SMask`, not a mask): `A = 255`, RGB from the sample.
- **`/ImageMask`:** binary alpha. Painted pixels use the **current fill colour**
  (`in.gs.fill`) with `A = 255`; transparent pixels are skipped. The fill colour
  must be captured at draw time (pass `in.gs.fill` into the draw call), because a
  stencil carries no colour of its own.
- **`/SMask`:** the soft mask is a separate DeviceGray 8-bpc image, possibly of
  **different dimensions** than the base. Decode it through the same
  Flate/raw-sample path. For each base sample at unit-square `(u,v)`, sample the
  SMask at the **same `(u,v)`** (its own `W_s,H_s`), take its gray value as
  `A ∈ [0,255]`, and blend RGB with that alpha. If the SMask uses an unsupported
  filter/format, treat the base image as opaque (do not skip the whole image).

`blend` already handles `A==0` (skip), `A==255` (overwrite), and partial alpha
(source-over) — reuse it unchanged.

---

## 7. Security bounds (SECURITY.md §2–3)

Untrusted input. Enforce **before** allocating or decoding:

- **Dimensions:** reject `W <= 0` or `H <= 0` (degenerate → skip).
- **Pixel count:** compute `int64(W) * int64(H)`; reject if it exceeds a bound
  (reuse `maxCanvasPixels`, 64 Mpx, or a dedicated `maxImagePixels`). Use `int64`
  math and check for overflow before any `int` multiply.
- **Decoded sample size (raw path):** compute expected bytes
  `stride * H` with `stride = ceil(W * BPC * nComp / 8)` in `int64`; reject if it
  exceeds a bound. `parse.DecodeStream` independently caps output at 256 MiB
  (`maxDecoded`) — rely on it as a backstop but pre-check to avoid work.
- **Length check:** after decode, verify the sample buffer is at least
  `stride * H`; if short (truncated/hostile), skip rather than index out of range.
- **JPEG path:** `image/jpeg` allocates from the JPEG's own header dimensions,
  which may differ from `/Width`×`/Height`. Pre-check the dict `W*H` bound, and
  after decode clamp sampling to the returned image's real bounds. A decode error
  → skip.
- **Matrix degeneracy:** `|det| < epsilon` (zero-area placement) → skip; prevents
  divide-by-zero in the inverse.
- **Bounded loop:** the draw loop only iterates the device bounding box already
  clamped to the canvas, so total work is bounded by canvas size regardless of
  `W,H`.

Every failure path is a **graceful skip**, never a panic or error return — the
page must still render (matches the existing "ignore what we can't do" policy in
`render.go`'s package doc).

WASM: the whole path is pure-Go (`image/jpeg`, `image/color`, existing raster);
no filesystem, no network. Nothing to add for the privacy path.

---

## 8. Package-internal API sketch (no Go code)

New file `core/render/image.go`, package-internal only (no public API change
beyond the `run`/`interp` plumbing):

- **Interpreter wiring**
  - `interp` gains `res *parse.Resolver` and `resources parse.Dict`.
  - `run(...)` signature gains those two params; `render.go` `Page` passes
    `r` and `pi.Resources`.
  - `interp.apply` gains `case "Do":` → `in.doXObject(name)` where `name` is the
    trailing `/Name` operand.

- **`doXObject(name parse.Name)`**
  - Resolves `/Resources /XObject /<name>` to a `*parse.Stream`; checks
    `/Subtype`; on `/Image` calls the decoder then the drawer; otherwise no-op.

- **Decoder** (returns nil on any unsupported/failed case → caller skips)
  - Sketch: `decodeImage(res *parse.Resolver, st *parse.Stream, fill color.RGBA)
    (*decodedImage, bool)`.
  - `decodedImage` holds `w, h int` plus per-pixel RGBA **with alpha already
    resolved** (SMask/ImageMask baked in). Simplest representation: `pix
    []color.RGBA` of length `w*h`, or a small `at(col,row) color.RGBA` accessor.
  - `fill` is passed so the ImageMask stencil can bake the current fill colour at
    decode time.
  - Internally: read `/Filter`, branch JPEG vs Flate/raw (§4); read
    BPC/CS/ImageMask; if `/SMask` present, recursively decode it as gray alpha
    and combine.
  - A separate helper `imageFilters(st *parse.Stream) []parse.Name` normalises
    `st.Dict["Filter"]` (Name or Array) — needed because `parse.nameList` is
    unexported.

- **Drawer**
  - Sketch: `drawImage(rs *raster, ctm matrix, img *decodedImage)`.
  - Builds `imgToDevice`, its inverse, the clamped device bounding box, then
    inverse-maps + `rs.blend` per §5–6.

- **Matrix helper**
  - Add `func (m matrix) invert() (matrix, bool)` to `raster.go` (returns false on
    near-zero determinant). Reused by the drawer.

---

## 9. Testing strategy

- **Golden-file (render):** small fixtures under `core/render/testdata/`, PNG
  goldens under `.../golden/`:
  - a DeviceRGB 8-bpc FlateDecode image placed with a plain `cm` scale;
  - a DeviceGray image;
  - a baseline DCTDecode (JPEG) image;
  - an `/ImageMask` stencil painted in a non-black fill colour;
  - an image with an `/SMask` (verify partial transparency over vector content);
  - a rotated/skewed `cm` placement (verify the y-flip and inverse mapping).
- **Table-driven (unit):** `imageFilters` normalisation; `matrix.invert`
  round-trip (`m.mul(inv) ≈ identity`); stride/size computation for odd `W`.
- **Security / robustness:** oversized `W*H`, `W<=0`, truncated sample buffer,
  degenerate CTM (`det≈0`), unsupported filter (JPX/JBIG2/CCITT), a corrupt JPEG
  — each must render the page without panicking and simply omit the image. Add a
  fuzz seed (malformed image dict + short stream) under
  `testing/security/fuzz/` if a target exists.
- **CI:** must stay green for **native and WASM** targets (pure-Go path, so WASM
  should build cleanly).

---

## 10. Open questions (maintainer decision)

- **O1 — SMask resampling:** nearest-neighbour sampling of a differently-sized
  SMask at shared `(u,v)` is proposed. Acceptable for Wave 1, or require the
  SMask to match base dimensions? (Proposed: nearest-neighbour, no constraint.)
- **O2 — Indexed / ICCBased priority:** both are common in real PDFs
  (Indexed especially for screenshots/GIF-origin images; ICCBased wraps
  RGB/CMYK). Should one or both be pulled into Wave 1, or firmly deferred?
  (Proposed: defer; ICCBased-as-alternate-of-N-components is a cheap follow-up
  since we can often ignore the profile and treat by component count.)
- **O3 — CMYK JPEG / Adobe inversion:** accept possible colour inversion on
  Adobe-marker CMYK JPEGs for Wave 1, or invest in the APP14 transform now?
  (Proposed: accept; document the limitation.)
- **O4 — Rasterisation direction:** inverse mapping (needs `matrix.invert`) vs
  forward footprint stamping. (Proposed: inverse mapping.)
- **O5 — Form XObjects:** confirm they stay out of this increment. They need
  recursive content interpretation, their own `/Matrix` and `/Resources`, and a
  recursion-depth bound (SECURITY.md §2). (Proposed: separate design note.)
