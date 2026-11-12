# Encrypted corpus

Third-party encrypted PDFs for the Standard Security Handler (ISO 32000-1
§7.6.3). They are written by Ghostscript's `pdfwrite` device, not by FluxDocs,
so the password checks are tested against another implementation of the same
algorithms. `core/parse/crypt_open_test.go` (`TestOpenBytes_GhostscriptFixtures`)
opens each file with `core.OpenBytes`:

| File | /R | Key | User password | Expected |
|---|---|---|---|---|
| `r2_40_empty.pdf` | 2 | 40-bit RC4 | empty | opens, text `Encrypted fixture text` |
| `r2_40_user.pdf` | 2 | 40-bit RC4 | `user` | `ErrEncryptedDocument` |
| `r3_40_empty.pdf` | 3 | 40-bit RC4 | empty | opens, text `Encrypted fixture text` |
| `r3_40_user.pdf` | 3 | 40-bit RC4 | `user` | `ErrEncryptedDocument` |
| `r3_128_empty.pdf` | 3 | 128-bit RC4 | empty | opens, text `Encrypted fixture text` |
| `r3_128_user.pdf` | 3 | 128-bit RC4 | `user` | `ErrEncryptedDocument` |

Without a password the `*_user` files fail with reason `password_required`;
with `core.OpenOptions{Password: "user"}` or `"owner"` they open
(`TestOpenBytes_GhostscriptFixturesWithPassword`).

The owner password of every file is `owner`. No R4 (AESV2 or RC4 crypt filter)
fixtures yet: Ghostscript's `pdfwrite` writes R2 and R3 only, and qpdf was not
installed when these were made. R4 is covered by the synthetic documents in
`core/parse` (`StdSecurity`, `EncryptedPDF`).

## How they were made

GPL Ghostscript 10.07.1 (`gs --version`), on macOS. The source is a one-page
PDF 1.4 file with a classic xref table, a 200×200 media box, non-embedded
Helvetica and this content stream:

```
BT /F1 12 Tf 20 100 Td (Encrypted fixture text) Tj ET
```

Each file was then written with:

```
gs -q -dNOPAUSE -dBATCH -dSAFER -sDEVICE=pdfwrite -sOwnerPassword=owner \
   [-sUserPassword=user] -dEncryptionR=<R> -dKeyLength=<bits> \
   -o <name>.pdf source.pdf
```

with `-dEncryptionR=2 -dKeyLength=40` (`r2_40_*`), `-dEncryptionR=3
-dKeyLength=40` (`r3_40_*`) and `-dEncryptionR=3 -dKeyLength=128`
(`r3_128_*`), and `-sUserPassword=user` for the `*_user` files only.
Ghostscript writes a fresh `/ID` and creation date each run, so regenerated
files differ byte for byte but must pass the same test.

## AES-256 (R6, R5)

The `qpdf_*.pdf` and `enc_*.pdf` files are AES-256 documents
(docs/design/aes256-decryption.md §11.1). Every page shows one synthetic
marker line, `AES256-OK-<case>` (no PII). `MANIFEST.tsv` records, per file, the
producer and its version, the revision, the user and owner passwords as UTF-8
hex, the marker, whether Ghostscript and qpdf found the marker with each
password, and the file's SHA-256. `TestOpenBytes_AES256Fixtures`
(`core/parse/crypt_open_test.go`) opens each one with no password, its user
password, its owner password and a wrong one, and checks the marker, the
error reasons and the save policy.

| File | Producer | /R | User password | Case |
|---|---|---|---|---|
| `qpdf_r6_owner_only.pdf` | qpdf | 6 | empty | owner password `owner` only |
| `qpdf_r6_user.pdf` | qpdf | 6 | `user` | user and owner password |
| `qpdf_r6_same.pdf` | qpdf | 6 | `same` | user = owner |
| `qpdf_r6_umlaut.pdf` | qpdf | 6 | `pässwörd` | non-ASCII (precomposed) |
| `qpdf_r6_soft_hyphen.pdf` | qpdf | 6 | `pass` U+00AD `word` | SASLprep maps U+00AD to nothing |
| `qpdf_r6_ideographic_space.pdf` | qpdf | 6 | `pass` U+3000 `word` | SASLprep maps U+3000 to a space |
| `qpdf_r6_fullwidth.pdf` | qpdf | 6 | `ＰＡＳＳ` (fullwidth) | NFKC changes it |
| `qpdf_r6_cleartext_metadata.pdf` | qpdf | 6 | `user` | `--cleartext-metadata`: an XMP stream stored unencrypted |
| `qpdf_r6_objstm.pdf` | qpdf | 6 | `user` | `--object-streams=generate`: an encrypted object stream |
| `qpdf_r5_user.pdf` | qpdf | 5 | `user` | `--force-R5`, the Acrobat 9 revision |
| `qpdf_r5_owner_only.pdf` | qpdf | 5 | empty | R5 owner-only: the reproduction of the Ghostscript R5 quirk below |
| `qpdf_r6_chain_user.pdf` | qpdf | 6 | `user` | a 2.B chain that separates the two readings of the stop test |
| `qpdf_r6_chain_owner_only.pdf` | qpdf | 6 | empty | the same, owner password only |
| `enc_r5_empty_user.pdf` | FluxDocs test encryptor | 5 | empty | R5 with an empty user password |
| `enc_r6_objstm_meta.pdf` | FluxDocs test encryptor | 6 | `user` | object stream, cross-reference stream, `/EncryptMetadata false`, a `/Crypt /Identity` stream |

Every owner password is `owner`, except `qpdf_r6_same.pdf` (`same`).

### How they were made

`testing/tools/aes256vectors/generate.sh` (maintainer-run, never in CI) writes
them: synthetic sources from `testing/tools/aes256vectors` encrypted by the
qpdf 12.4.2 command line with documented options only (`--encrypt
--user-password=… --owner-password=… --bits=256 [--force-R5]
[--cleartext-metadata] --`, `--object-streams=generate`,
`--allow-weak-crypto` for R5), and the `enc_*` files by the test-only
encryptor (`core/parse/crypt_v5_fixtures_test.go`, never shipped). A file is
recorded only after Ghostscript 10.07.1 (txtwrite with `-sPDFPassword`) and
qpdf (`--check`, then `--decrypt` with the password) found its marker with
both passwords: one program wrote it, another read it, before FluxDocs did.
The one exception, recorded in the manifest (`gs_user` = `fail`, with a
comment line giving the evidence): Ghostscript does not open an R5 file with
the empty user password (it does with the owner password). That is the
reader's fault, not the file's: it reproduces on qpdf's own R5 output,
`qpdf_r5_owner_only.pdf`, whose user password `qpdf --show-encryption` reports
as empty and which qpdf opens with it, as it does `enc_r5_empty_user.pdf`. The
script tolerates that failure for R5 with an empty user password only.

The `qpdf_r6_chain_*` files guard the Algorithm 2.B stop test (round count
versus 0-based round index, design §14): qpdf picks random salts, and the
script encrypts again until `TestSeparatesCheck` finds a file on which the
off-by-one reading derives a different hash (about 1 in 20).
`TestQpdfFixturesSeparateStopReadings` pins that they still do. Without qpdf installed the
script skips the `qpdf_*` cases. qpdf picks random salts, so regenerated files
differ byte for byte but must pass the same checks.

qpdf (Apache-2.0) and Ghostscript (AGPL) ran only as separate programs on
synthetic inputs; their output files are data, and neither is linked or
shipped.
