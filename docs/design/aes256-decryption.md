# Design: AES-256 decryption (standard security handler V5, R6; R5 read-only)

Status: design (docs only), not yet approved. The open questions are in §13.
Tier: **Core (MIT)**. Decryption is part of opening a document, so it belongs
in the same tier as today's RC4/AES-128 path. Packages: `core/parse`
(`crypt.go`, new `crypt_v5.go` and `saslprep.go`), `core` (open path,
`Document.Encrypted`). Optional `wasm` and `packages/core` changes come only
with a password option (§9). Pure Go, no CGO, no build tag. Both targets
(native, `GOOS=js GOARCH=wasm`).

Basis (clean-room, public specifications only):

- ISO 32000-2:2020 §7.6 (encryption): the general encryption algorithm and
  Algorithm 1.A (AES; the AESV3 case uses the file key directly), the standard
  security handler's encryption dictionary for R6 (`/O`, `/U`, `/OE`, `/UE`,
  `/Perms`), and the password algorithms: 2.A (retrieve the file key), 2.B
  (the R6 hash), 8 and 9 (compute `/U`,`/UE` and `/O`,`/OE`), 10 (compute
  `/Perms`), 11 and 12 (authenticate user and owner passwords), and 13
  (validate permissions). Section and table numbers must be confirmed against
  the licensed copy when implementing. This note names the algorithms by
  number.
- ISO 32000-1:2008 §7.5.7 (what cannot be in an object stream), §7.6.1–7.6.3
  (today's R2–R4 path).
- Adobe *Supplement to ISO 32000, BaseVersion 1.7, ExtensionLevel 3*: the R5
  handler that Acrobat 9 wrote. ISO 32000-2 deprecates it.
- RFC 4013 (SASLprep), RFC 3454 (stringprep: tables B.1, C.1.2–C.9, §6 bidi),
  FIPS 197 (AES), FIPS 180-4 (SHA-256/384/512), NIST SP 800-38A (ECB, CBC).

**Never** read or paraphrase the source of pdf.js, MuPDF, PDFium, poppler,
qpdf, pdfcpu, Ghostscript, iText, Nutrient or Apryse (SECURITY.md §6). qpdf and
Ghostscript are used only as external black-box programs, to produce and check
test files (§11). They are never linked and their source is never read.

---

## 1. Current state (HEAD `5828c31`)

| Fact | Where |
|---|---|
| Accepts `/Filter /Standard`, `/R` 2–4 only: RC4 (V1/V2) and AES-128 (V4 `/AESV2`). R5/R6 fail with "unsupported /R" | `core/parse/crypt.go:44-48` |
| R6 rejection is pinned by a test | `core/parse/crypt_test.go:102-106` |
| `OpenBytes` maps any `EnableDecryption` error to `ErrEncryptedDocument`, so every AES-256 file fails to open | `core/open.go:61-66` |
| Only the **empty user password** is tried. There is no public password API | `core/open.go:63` (`EnableDecryption(nil)`) |
| **No password check for R2–R4.** The key is derived (Algorithm 2) and used without comparing `/U`. A file that needs a user password decrypts to garbage instead of `ErrEncryptedDocument`, contrary to `EnableDecryption`'s doc comment | `crypt.go:86`, `resolver.go:243-246` |
| Crypt filters: only `/CF /StdCF /CFM` is read. `/StmF`, `/StrF`, `/EFF`, `/Identity` and per-stream `/Crypt` filters are ignored | `crypt.go:92-108` |
| `/EncryptMetadata false` affects only the R4 key. Metadata streams are still decrypted | `crypt.go:81-87`, `decryptObject` |
| Object decryption skips the `/Encrypt` object. The xref stream is read before decryption is enabled. ObjStm payloads are decrypted as a whole stream | `resolver.go:186-190`, `objstm.go:132-136` |
| `aesCBCDecrypt` works with any AES key length: IV prefix, lenient on bad length or padding | `crypt.go:197-211` |
| `Document.Encrypted` exists but is never set | `core/types.go:30` |
| One external fixture: an RC4 R3 file written by Ghostscript | `core/testdata/encrypted_rc4.pdf`, `core/encrypt_test.go` |

**What saving does today (stated as asked).** `SaveTo`, `RedactAndFlatten`,
`Merge`, `Split`, `FlattenAnnotations`, `FlattenForm` and `ConvertToPDFA` all
rebuild through `parse.BuildDocument` / `BuildDocumentPDFA` from objects the
resolver has **already decrypted**. The writer emits no `/Encrypt`
(`core/parse/writer.go:134`: "No /Encrypt, no /Prev"), and `BuildDocument`
sets no `/ID`. **The output is plaintext.** A redaction of an encrypted
document is written unencrypted. `Document.Bytes()` returns the original
encrypted bytes verbatim. `SignDocument` appends an *unencrypted* incremental
update to the *encrypted* original. Its new trailer has no `/Encrypt` and no
`/ID` (`core/sign/sign.go:124`), so a reader of the signed file treats the
whole document as unencrypted and the original objects as garbage (F1, §12).

---

## 2. Encryption dictionary, V5 (read and validated before any use)

| Entry | Accepted | Otherwise |
|---|---|---|
| `/Filter` | `/Standard` | unsupported (as today) |
| `/V` | 5 | — (V ≤ 4 keeps today's path) |
| `/R` | 6; 5 (see §8) | unsupported. V5 with R ≤ 4, or V ≤ 4 with R ≥ 5, is unsupported |
| `/O`, `/U` | strings ≥ 48 bytes; the first 48 are used (some writers pad to 127) | unsupported |
| `/OE`, `/UE` | strings ≥ 32 bytes; the first 32 are used | unsupported |
| `/Perms` | string ≥ 16 bytes; the first 16 are used | unsupported |
| `/P` | integer; the low 32 bits are compared (§5) | treated as absent |
| `/EncryptMetadata` | boolean, default true | default |
| `/CF` | dictionary of crypt filters. For each one used: `/CFM /AESV3` or `/None`. Its `/Length` is ignored (the key is always 32 bytes) | a filter in use with another `/CFM` is unsupported |
| `/StmF`, `/StrF` | a name in `/CF`, or `/Identity`; **default `/Identity`** | unsupported |
| `/EFF` | a name in `/CF`, or `/Identity`; default: the `/StmF` value | unsupported only if an embedded-file stream is actually decrypted |
| `/Length` (top level) | ignored for V5 | — |

Every length is checked before a slice is taken. Nothing in the dictionary
sizes an allocation, because the key length is fixed at 32. This closes the
class that `TestNewDecryptor_HostileKeyLength` pins for R2–R4.

---

## 3. File key retrieval (Algorithm 2.A), steps

Input: the password bytes `pw` (§4), `/O`, `/U`, `/OE`, `/UE`. Slices:
`O = hash(32) ‖ validation salt(8) ‖ key salt(8)`, and `U` the same.

1. **Owner test.** `h = Hash(pw, O[32:40], U[0:48])`. If `h == O[0:32]`, the
   password is the owner password. Then
   `ik = Hash(pw, O[40:48], U[0:48])`, and the file key is the AES-256-CBC
   decryption of `OE[0:32]` with key `ik`, an all-zero IV and no padding.
2. **Otherwise, user test.** `h = Hash(pw, U[32:40], ε)`. If
   `h == U[0:32]`, the password is the user password. Then
   `ik = Hash(pw, U[40:48], ε)`, and the file key is the AES-256-CBC
   decryption of `UE[0:32]` with key `ik`, a zero IV and no padding.
3. **Otherwise** the password is wrong: `ErrEncryptedDocument`.
4. **Validate `/Perms`** with the file key (§5).

Comparisons use `crypto/subtle.ConstantTimeCompare`. This is not strictly
needed in a reader, but it costs nothing. Owner and user both decrypt to the
same file key. FluxDocs does not enforce permissions (§13 Q6), so the role
only goes into the report.

`Hash` is Algorithm 2.B for R6. For R5 it is a single
`SHA-256(pw ‖ salt ‖ udata)` (§8).

### 3.1 Algorithm 2.B (R6 hash), steps

Input: `pw`, an 8-byte `salt`, and `udata`, which is either the 48-byte `U` or
empty.

1. `K = SHA-256(pw ‖ salt ‖ udata)` (32 bytes).
2. Repeat one **round**, numbered from 0:
   1. `K1 = (pw ‖ K ‖ udata)` concatenated 64 times.
   2. `E = AES-128-CBC-encrypt(K1)` with key `K[0:16]`, IV `K[16:32]`, and no
      padding. `len(K1)` is 64 × something, so it is a multiple of 16.
   3. `m = (E[0:16] read as an unsigned big-endian integer) mod 3`. Because
      256 ≡ 1 (mod 3), this equals `(Σ E[i], i < 16) mod 3`, so no big integer
      is needed.
   4. `K = SHA-256(E)`, `SHA-384(E)` or `SHA-512(E)` for `m` = 0, 1 or 2.
      `K` is therefore 32, 48 or 64 bytes.
   5. Stop if the round number is ≥ 63 (at least 64 rounds were done) **and**
      `E[len(E)-1] ≤ round − 32`. Otherwise run another round.
3. The result is `K[0:32]`.

**Termination is structural.** `E`'s last byte is at most 255, so the stop test
holds at round 287 at the latest. 2.B runs **between 64 and 288 rounds**. The
implementation uses a `for` loop bounded at 288 rounds. If the stop test has
not held by then, it fails closed with an error (unreachable; a guard against a
coding slip).

Primitives, all from the standard library: `crypto/sha256` (`Sum256`),
`crypto/sha512` (`Sum384`, `Sum512`), `crypto/aes` + `crypto/cipher`
(`NewCBCEncrypter` for 2.B, `NewCBCDecrypter` for `/OE`/`/UE` and for
objects). A single `Block.Decrypt` on one block serves as the "ECB" of
`/Perms`. `crypto/subtle` handles comparison. No third-party crypto.

---

## 4. Password preparation (SASLprep)

ISO 32000-2 requires the R5/R6 password to be processed with SASLprep
(RFC 4013), encoded as UTF-8 and truncated to 127 bytes. Steps, following
RFC 4013 §2 and RFC 3454:

1. **Map.** Non-ASCII space characters (RFC 3454 table C.1.2) become U+0020.
   Characters in table B.1 ("commonly mapped to nothing", for example U+00AD
   SOFT HYPHEN and U+200B) are removed.
2. **Normalize** to NFKC.
3. **Prohibit.** Tables C.1.2, C.2.1, C.2.2, C.3, C.4, C.5, C.6, C.7, C.8 and
   C.9 → error.
4. **Bidi** (RFC 3454 §6). If the string contains a RandALCat character, it
   must contain no LCat character, and its first and last characters must be
   RandALCat; otherwise → error.
5. **Unassigned code points** (table A.1) are **allowed**. A password typed
   at open is a "query" in RFC 3454 terms.
6. UTF-8 encode, then truncate to 127 **bytes** (the standard counts bytes; a
   cut may split a multi-byte character, and that is what the writer did
   too).

Implementation notes:

- NFKC comes from `golang.org/x/text/unicode/norm`. It is **not standard
  library**, but it is already a dependency of the root module (`go.mod`:
  `golang.org/x/text v0.21.0`), is BSD-3, pure Go, and is used by redaction's
  needle matching. Go's standard library has no stringprep. Tables B.1 and
  C.x are transcribed from RFC 3454 into Go range tables by a generator under
  `testing/tools/` that reads the RFC text. That is data from a public
  standard, not code from a library (§13 Q5 asks about a notice).
- x/text implements a newer Unicode version than 3.2, which stringprep pins.
  The difference only affects code points that were unassigned in 3.2. Those
  are allowed in queries, and a writer on a newer Unicode normalizes them the
  same way. Documented, not worked around.
- **Do not substitute PRECIS** (`x/text/secure/precis`, RFC 8264/8265).
  OpaqueString uses NFC and different mappings, so it would derive different
  bytes than ISO 32000-2 specifies.
- **When SASLprep runs.** Only on a caller-supplied, non-empty password. The
  empty password maps to empty. Today only the empty password is tried, so
  SASLprep is reachable only once a password option exists (§9).
- **Fallback.** Some writers encrypt with the raw UTF-8 password and skip
  SASLprep. If the prepared bytes fail both tests in §3, and the raw UTF-8
  bytes (truncated to 127) differ from them, try the raw bytes once. If SASLprep
  itself errors (a prohibited character), only the raw bytes are tried (§13 Q4).
- R2–R4 passwords are not SASLprep'd. They are PDFDocEncoding bytes, padded
  per Algorithm 2. A future password option must convert a Go string
  differently per revision.

---

## 5. `/Perms` validation (Algorithm 13)

Decrypt `Perms[0:16]` with the file key, as one AES-256 block (ECB, no IV).
The result `p` has bytes 0–3 = the permissions as little-endian, bytes 4–7 =
`0xFF`, byte 8 = `T` or `F` (EncryptMetadata), bytes 9–11 = `a d b`, and
bytes 12–15 random.

`/U` (or `/O`) authenticates only the *password*. A modified `/UE` (or `/OE`)
would yield a wrong file key that nothing else detects, so content would
decrypt to garbage. `adb` is the only check that the **file key** is right.

Options:

- **A, strict.** Require `adb`, `p[0:4] == uint32(/P)`, and `p[8]` to agree
  with `/EncryptMetadata`. Any mismatch → `ErrEncryptedDocument`.
- **B (recommended).** Require `adb`; a mismatch → `ErrEncryptedDocument`
  (wrong key, or a tampered file). A `/P` mismatch is tolerated, and the
  authenticated value from `/Perms` is the one recorded. If `p[8]` is `T` or
  `F`, it overrides `/EncryptMetadata`, because it is authenticated and the
  dictionary entry is not. FluxDocs does not enforce permissions (§13 Q6), so
  rejecting on `/P` would only refuse files other readers open.
- **C.** Ignore `/Perms`. Rejected: garbage content would surface as
  "unreadable content" instead of a clear error, and redaction would refuse it
  as `content_ambiguous` without saying why.

---

## 6. Object decryption (AESV3)

- **Key.** The 32-byte file key, **used directly**: no per-object MD5, no
  `sAlT` (Algorithm 1.A, AESV3 case). `objectKey` is bypassed for
  `methodAESV3`.
- **Cipher.** AES-256-CBC: the first 16 bytes are the IV, followed by PKCS#5
  padding. This is the existing `aesCBCDecrypt` with a 32-byte key. Leniency
  is unchanged: malformed data is returned as-is.
- **Which filter applies.** Strings use `/StrF`; streams use `/StmF`;
  streams with `/Type /EmbeddedFile` use `/EFF`. `/Identity` means no
  decryption. A stream whose `/Filter` starts with `/Crypt` uses the crypt
  filter named in its `/DecodeParms /Name` (`/Identity` if absent), and the
  `/Crypt` stage is then dropped from the decode chain.
- **Not decrypted** (today's exceptions, kept): the `/Encrypt` dictionary's own
  strings, cross-reference streams, the trailer (`/ID`), and objects inside
  an ObjStm (the ObjStm is decrypted as one stream).
- **Fixed for V4 and V5 alike:** with `/EncryptMetadata false`, streams with
  `/Type /Metadata` are not decrypted.
- **Cost.** AES-256 runs 14 rounds against AES-128's 10, about 1.4× per byte.
  This is the only per-object change.

Changes to `newDecryptor`: dispatch on `(V, R)` *before* any length check,
then `V ≤ 4` → today's code unchanged, `V = 5` → §2–§5.
`crypt_test.go:102-106` changes from "R6 is rejected" to "R6 with a short `/O`
is rejected", and asserts the reason.

---

## 7. Hostile-input bounds (SECURITY.md §2)

| Bound | Value | Note |
|---|---|---|
| 2.B rounds | 64 ≤ rounds ≤ 288, by construction | loop bounded at 288 rounds; past it the call fails closed (§3.1) |
| `K1` / `E` buffer | ≤ 64 × (127 + 64 + 48) = 15,296 bytes | password ≤ 127 after truncation; `|K|` ≤ 64; `|udata|` ∈ {0, 48}. Allocated once per `Hash` call, reused across rounds |
| `Hash` calls per open | ≤ 3 for one password (one failed test, one matching test, one key); ≤ 5 with the raw-bytes fallback (§4) | the empty password has no fallback |
| Field lengths | checked before slicing (§2) | no allocation sized by the file |
| `/P` | compared as `uint32` of the low 32 bits | no narrowing that could panic |
| Errors | `ErrEncryptedDocument` at the boundary. Messages never contain the password, keys or decrypted bytes | — |

**The work an attacker can force.** The attacker controls the salts, so they
choose the round count. Offline they can search for salts that give long
chains. The chance of continuing past round `i` is `(287 − i)/256` per round,
so about 100 rounds is a practical maximum (estimate: roughly 1 in 5,000 salts
reaches 100). The theoretical worst case is 288. With the empty password,
`K1` ≤ 7,168 bytes, so the worst open costs ≤ 3 × 288 × 7 KB ≈ 6 MB of
AES-128-CBC plus as much SHA-2. A 127-byte caller password raises that to
≈ 22 MB with the fallback.

**WASM cost** (estimates; the implementation PR measures them, §11.4). Go's
`crypto/aes` under js/wasm uses the generic table implementation (no AES-NI),
and SHA-2 is generic too.

| Case | Bytes processed | Native (AES-NI host) | js/wasm |
|---|---|---|---|
| Typical: empty password, ≈ 64–70 rounds | ≈ 3 × 70 × (2–5 KB) ≈ 0.5–1 MB | < 5 ms | ≈ 20–60 ms |
| Worst: empty password, 288 rounds | ≈ 6 MB | ≈ 10–30 ms | ≈ 0.2–0.6 s |
| Worst: 127-byte password + fallback | ≈ 22 MB | ≈ 50–100 ms | ≈ 1–2 s |

This cost is paid **once per open** (and once per password attempt). Object
decryption is per-object streaming work, as for AES-128. No goroutines are
needed. Under js/wasm the cost blocks the tab while `open()` runs, like any
synchronous export (render-budget.md §6, "Out of scope").

**Privacy path.** Pure computation, with no network and no filesystem.
Decryption does not need `crypto/rand`. A password never leaves the call: no
logging, no telemetry, and best-effort zeroing of the prepared password and
intermediate keys after use (Go cannot guarantee it).

---

## 8. R5 decision: support it, read-only

**Decision (proposed): decrypt R5, never write it.**

- **Cost.** R5 has the same dictionary layout (`/O`, `/U`, `/OE`, `/UE`,
  `/Perms`, AESV3) and the same 2.A steps. Only `Hash` differs: one SHA-256
  instead of the 2.B loop. The cost is one branch and a handful of test rows.
- **Benefit.** Acrobat 9 wrote R5 (Adobe ExtensionLevel 3, 2008), and such
  archives still circulate. Without R5 support those files keep failing with
  `ErrEncryptedDocument`.
- **Security.** R5's weakness is that one fast SHA-256 makes the password cheap
  to brute-force from `/U`. That is a property of the *file*, and it hurts its
  author, not a reader. Reading R5 adds no attack surface beyond the 2.A steps
  already needed for R6, and it is bounded trivially (one hash per test).
  ISO 32000-2 deprecates R5 for writers. FluxDocs writes no encryption today,
  and §10 never writes R5.
- **Rejected alternative.** Reject R5, as "deprecated means unsupported". This
  saves almost nothing and turns away real files.

---

## 9. API

- **No new API for the main case.** A file with an owner password only (empty
  user password, the common "restricted" PDF) opens with `OpenBytes`, exactly
  as RC4/AES-128 files do today.
- **`Document.Encrypted` is set** when the trailer has `/Encrypt` and
  decryption succeeded (V ≤ 4 too). Today it is always false. This is a
  behavior change on an existing exported field, so it needs a doc comment and
  a spec §6.1 note.
- **Password option (proposed, §13 Q1).** Use
  `OpenOptions{Password string, …}` with
  `OpenBytesWithOptions`/`OpenDocumentWithOptions`, the same struct the
  xref-reconstruction note proposes for a strict flag (its §11 Q6).
  `EnableDecryption(password []byte)` already exists in `parse`. The core layer
  applies SASLprep for V5 and PDFDocEncoding for V ≤ 4.
  - **WASM:** `open(bytes, {password?: string})`, with the
    `packages/core` mirror.
  - **Viewers (React/Vue, kept identical):** a password prompt is a separate UI
    design (a dialog using the `fd-` BEM classes, an a11y focus trap, and the
    keyboard map in spec §8.4). It is not part of this note.
- **Telling "needs a password" from "unsupported".** Today both are
  `ErrEncryptedDocument`. A viewer must know whether to prompt. Options:
  - an `*EncryptionError{Reason}` that matches `ErrEncryptedDocument`, with
    reasons `password_required`, `wrong_password` and `unsupported_handler`
    (recommended: it mirrors `*RedactionError`);
  - or a new sentinel `ErrPasswordRequired`.

  The WASM error envelope would map these reasons to new codes
  (render-budget.md §6.1: codes are additive).

---

## 10. Interaction with save and redaction

Today an encrypted source is **saved and redacted as plaintext** (§1). AES-256
inherits that unchanged. Options:

- **S1 (recommended for this increment).** Keep plaintext output, and make it
  explicit:
  - document it on `SaveTo`, `RedactAndFlatten`, `Merge` and `Split`;
  - set `Document.Encrypted` so callers can tell;
  - add `RedactionReport.SourceEncrypted bool`.

  Redaction stays correct. Its verifier byte-searches the output, which only
  works on plaintext.
- **S2 (follow-up design).** Re-encrypt full rebuilds with the source's handler
  and file key. For V5 the file key is independent of `/ID`, so the `/Encrypt`
  dictionary (O, U, OE, UE, Perms, P) can be copied verbatim. Every string and
  stream is then encrypted with the same key and a **fresh random IV**
  (`crypto/rand`, which is available under js/wasm with no network). R2–R4
  would also need the source `/ID[0]` in the output trailer, because their key
  depends on it. For redaction, verification must run on the plaintext build
  first. Then the build is encrypted, and the encrypted output is re-opened
  with the same password and its decrypted objects compared with the verified
  build. Any difference → `ErrRedactionVerification`. The tier for S2 is
  undecided ("protect with password" is not in the tier table).
- **S3.** Refuse to save an encrypted source unless the caller opts into
  plaintext output.

**Hard rules, whatever is chosen.**

1. **Never re-encrypt by an incremental update of the encrypted original.**
   The unredacted earlier revision would stay in the file, decryptable with
   the same password. Redaction output stays a full rebuild
   (`xref-reconstruction.md` §5 rule R).
2. Never reuse an IV across objects.
3. **`SignDocument` refuses an encrypted source** (`ErrEncryptedDocument`)
   until an encrypted incremental update is designed. Today's output is broken
   (F1).

---

## 11. Testing

### 11.1 Producing vectors clean-room

There are no external AES-256 fixtures in the repo. Vectors come from two
independent black-box programs and from the spec text, never from library
source.

1. **Generator (qpdf, external).** Use a script under
   `testing/tools/aes256vectors/`, run **manually** by a maintainer, never in
   CI. It writes synthetic one-page PDFs whose page shows `AES256-OK-<case>`
   (no PII, SECURITY.md §7), then encrypts them with the qpdf command-line
   tool, using only its documented options (`--encrypt <user> <owner> 256 --`
   and its R5 option, documented as forcing R5). Cases:
   - empty user password with an owner password;
   - a user password;
   - user and owner passwords that are the same;
   - non-ASCII passwords: `pässwörd`, one containing U+00AD, one containing
     U+3000, and one in fullwidth letters (NFKC);
   - cleartext metadata (EncryptMetadata false);
   - object streams generated (an encrypted ObjStm);
   - R5.

   Outputs go to `testing/corpus/encrypted/`, which already exists and which
   the redaction no-regression walk already visits. A `MANIFEST.tsv` records
   the case, the qpdf version, the passwords, the expected marker and the
   SHA-256 of each file.
2. **Independent reader check (Ghostscript, external).** For each fixture, the
   same script runs Ghostscript's text output device with the password and
   requires the marker. The result goes into the manifest. A fixture is
   committed only when one program wrote it and another read it, before
   FluxDocs reads it. This is the same oracle pattern as
   `core/testdata/encrypted_rc4.pdf` and the redaction oracle job.
3. **Reverse direction.** A **test-only encryptor** (`_test.go`, never shipped)
   implements Algorithms 8, 9 and 10 from the ISO text. It builds in-test PDFs
   with chosen salts: long 2.B chains found by an offline salt search, a
   tampered `/UE`, a wrong `/Perms`, and P mismatches. An optional oracle
   test has qpdf (`--check`, and decrypting with the password) and Ghostscript
   read those files. It skips without the tools, and fails without them under
   `FLUXDOCS_REQUIRE_QPDF=1` / `FLUXDOCS_REQUIRE_GS=1`. If the encryptor and
   decryptor shared a misreading of the spec, round-trips alone would pass;
   the external readers catch that.
4. **SASLprep vectors** are the examples in RFC 4013 §3:
   - `I` U+00AD `X` → `IX`;
   - `user` → `user`;
   - `USER` → `USER`;
   - U+00AA → `a`;
   - U+2168 → `IX`;
   - U+0007 → error (prohibited);
   - U+0627 `1` → error (bidi).

   Add one row per table (B.1, C.1.2, and each prohibited table).

qpdf is Apache-2.0 and Ghostscript is AGPL. Both run only as separate
processes on synthetic inputs; neither is linked, vendored or read. Their
output files are data (§13 Q5).

### 11.2 Unit tests (`core/parse/crypt_v5_test.go`, table-driven)

- **2.B.** A property test over random salts and password lengths 0, 1, 127
  and 200 (truncated) asserts that rounds ∈ [64, 288] and the output is 32
  bytes. A deterministic count on a chosen long-chain salt. The R5 hash is one
  SHA-256.
- **2.A on fixtures.** Empty, user, owner and wrong passwords. The owner path
  and the user path yield the same key.
- **`/Perms`.** `adb` wrong → error. P mismatch → accepted (option B).
  `T`/`F` overrides `/EncryptMetadata`.
- **Hostile dictionaries.** `/O`/`/U` of 0, 32 and 47 bytes. `/OE`, `/UE` or
  `/Perms` short or missing. V5 with R4, V4 with R6, an unknown `/CFM`,
  `/StmF` naming a missing filter, `/Length` 2^62 (ignored), and P outside
  int32. Each must return an error with no panic, and no allocation beyond the
  parsed strings.
- **Object decryption.** Strings and streams; `/Identity` for `/StmF` and for
  `/StrF`; `/EncryptMetadata false` (metadata stream untouched, for V4 too);
  an ObjStm under AESV3; the xref stream not decrypted; a `/Crypt` filter
  stream.
- **Regression.** Every existing RC4/AES-128 test is unchanged.
  `TestOpenEncrypted_RealRC4` still passes.

### 11.3 Corpus, fuzz, DoS

- Corpus: `testing/corpus/encrypted/*.pdf` opens (with the empty password
  where the case has one). Text contains the marker. `SaveTo` round-trips to
  plaintext (S1, asserted). Re-record the redaction baseline; every new row is
  logged and reviewed.
- Fuzz (`testing/security/fuzz/crypt_v5_seeds_test.go`): V5 dictionaries with
  each field mutated, fed through `FuzzOpenBytes`. A `FuzzSASLprep` target
  checks no panic, and idempotence (`prep(prep(x)) == prep(x)` when no
  error).
- DoS (`testing/security/dos/crypt_v5_dos_test.go`):
  `TestDoS_R6KeyDerivationIsBounded` forces 288 rounds through a test hook on
  the stop test, with a 127-byte password and the fallback. It asserts CPU
  time under a bound (cputime helpers, scaled for `-race` and js/wasm) and a
  hash call count ≤ 5.

### 11.4 Benchmarks (calibration gate in the implementation PR)

`BenchmarkHash2B` at 64, 100 and 288 rounds, with password lengths 0 and 127,
natively and under js/wasm. The results replace the estimates in §7.
`BenchmarkDecryptAESV3` measures stream decryption throughput against AESV2.

---

## 12. Related findings (out of scope here, flagged)

- **F1. `SignDocument` on an encrypted source writes a broken file**
  (`sign.go:124`: no `/Encrypt`, no `/ID` in the appended trailer). Refuse it
  (§10 rule 3). One line of code plus a test.
- **F2. R2–R4 never authenticate the password** (§1). Add the `/U` comparison
  (ISO 32000-1 Algorithms 4/5 to compute, 6 to authenticate the user; 7 for
  the owner) so a password-protected R2–R4 file returns
  `ErrEncryptedDocument` instead of garbage. This should land with, or before,
  any password option. It matters for repair too: a repaired R2–R4 file whose
  `/ID` was lost is then detected (`xref-reconstruction.md` §3.6).
- **F3. A signature dictionary's `/Contents` is decrypted.** ISO 32000-2
  exempts it from encryption (confirm the clause). Decrypting it garbles an
  embedded signature for any future verifier. Skip strings under
  `/Type /Sig` → `/Contents`.
- **F4. Permissions are ignored** for every revision (§13 Q6).

---

## 13. Open questions (maintainer)

- **Q1. Password API.** Add `OpenOptions{Password}` now, shared with
  xref-reconstruction §11 Q6, or ship empty-password-only first (which covers
  owner-restricted files)? This also covers the WASM `open(bytes, {password})`
  shape and a viewer password prompt (separate UI note).
- **Q2. Error shape for encrypted files.** An `*EncryptionError{Reason}` that
  matches `ErrEncryptedDocument` (recommended), or a new sentinel
  `ErrPasswordRequired`. Either way, add the WASM codes.
- **Q3. Save policy (§10).** S1 now with S2 later (recommended), S2 now, or
  S3. If S2, decide its tier.
- **Q4. SASLprep fallback.** Try the raw UTF-8 password when the prepared one
  fails (recommended; it costs at most 2 more hashes), or follow the spec
  strictly.
- **Q5. Notices.** Do the transcribed RFC 3454 tables need an entry in
  `THIRD_PARTY_NOTICES.md` (IETF Trust provisions)? Do the qpdf-generated
  fixture files need one? Proposed: an RFC attribution comment in the
  generated table file, and no notice for tool output.
- **Q6. Permissions.** Keep ignoring `/P` (as for RC4/AES-128 today), or
  enforce some bits (for example "extract")? Enforcement would change what
  `ExtractText` and redaction may do on owner-restricted files. It is a product
  decision, and it decides between `/Perms` options A and B (§5).
- **Q7. R5.** Confirm read-only support (§8), or reject it.
- **Effort (for planning).**

  | Work | Estimate |
  |---|---|
  | V5/R6 with the empty password, plus `/Perms`, crypt-filter names and the EncryptMetadata fix | 2–3 d |
  | R5 | 0.5 d |
  | F1 + F2 | 1 d |
  | SASLprep and its table generator | 1.5 d |
  | Vector tooling, fixtures and the oracle test | 1.5–2 d |
  | Password option, WASM and TS (if Q1 says now) | 1 d |
  | Review round | 1 d |

  **Total ≈ 8–10 engineer-days.**
