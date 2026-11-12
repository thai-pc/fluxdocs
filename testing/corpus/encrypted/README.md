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
