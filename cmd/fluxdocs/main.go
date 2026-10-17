// Command fluxdocs is the FluxDocs CLI: a thin wrapper over the pure-Go core for
// inspecting, extracting, rendering, and reorganizing PDFs from the shell.
//
//	fluxdocs info    file.pdf
//	fluxdocs extract file.pdf [--page N]
//	fluxdocs render  file.pdf --page N [--dpi 150] [--format png] [-o out.png]
//	fluxdocs merge   -o out.pdf a.pdf b.pdf [c.pdf ...]
//	fluxdocs split   file.pdf --ranges 0-1,2-3 [-o prefix]
//	fluxdocs rotate  file.pdf --page N --degrees 90 [-o out.pdf]
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/thai-pc/fluxdocs/core"
)

const version = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches a subcommand and returns a process exit code. It takes explicit
// writers so the commands are testable without touching the real stdio.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "info":
		return cmdInfo(rest, stdout, stderr)
	case "extract":
		return cmdExtract(rest, stdout, stderr)
	case "render":
		return cmdRender(rest, stdout, stderr)
	case "merge":
		return cmdMerge(rest, stdout, stderr)
	case "split":
		return cmdSplit(rest, stdout, stderr)
	case "rotate":
		return cmdRotate(rest, stdout, stderr)
	case "watermark":
		return cmdWatermark(rest, stdout, stderr)
	case "redact":
		return cmdRedact(rest, stdout, stderr)
	case "autoredact":
		return cmdAutoRedact(rest, stdout, stderr)
	case "sign":
		return cmdSign(rest, stdout, stderr)
	case "compare":
		return cmdCompare(rest, stdout, stderr)
	case "pdfa":
		return cmdPDFA(rest, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, "fluxdocs "+version)
		return 0
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "fluxdocs: unknown command %q\n", cmd)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `fluxdocs — PDF toolkit CLI

Usage:
  fluxdocs info    <file.pdf>
  fluxdocs extract <file.pdf> [--page N]
  fluxdocs render  <file.pdf> --page N [--dpi 150] [--format png|jpeg] [--quality 90] [-o out]
  fluxdocs merge   -o <out.pdf> <in1.pdf> <in2.pdf> [in3.pdf ...]
  fluxdocs split   <file.pdf> --ranges 0-1,2-3 [-o prefix]
  fluxdocs rotate  <file.pdf> --page N --degrees D [-o out.pdf]
  fluxdocs watermark <file.pdf> --text "DRAFT" [--opacity 0.3] [--size 48] [--rotation 45] [-o out.pdf]
  fluxdocs redact  <file.pdf> --pattern "REGEX" [--page N] [-o out.pdf]
  fluxdocs autoredact <file.pdf> [--kinds email,ssn,phone,credit_card] [-o out.pdf]
  fluxdocs sign    <file.pdf> --cert cert.pem --key key.pem [--reason R] [-o out.pdf]
  fluxdocs compare <a.pdf> <b.pdf>
  fluxdocs pdfa    <file.pdf> [--validate] [-o out.pdf]
  fluxdocs version
`)
}

// fail prints an error to stderr and returns exit code 1.
func fail(stderr io.Writer, format string, a ...any) int {
	fmt.Fprintf(stderr, "fluxdocs: "+format+"\n", a...)
	return 1
}

// newFlagSet returns a FlagSet that writes its errors/usage to stderr and does
// not call os.Exit (so run() controls the exit code).
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseArgs parses flags and positional arguments in any order (the standard
// flag package stops at the first non-flag token). It returns the collected
// positionals, so `render file.pdf --page 0` and `render --page 0 file.pdf`
// both work.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
	return positionals, nil
}

func cmdInfo(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("info", stderr)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "info: expected exactly one PDF path")
	}
	doc, err := core.OpenDocument(pos[0])
	if err != nil {
		return fail(stderr, "info: %v", err)
	}
	defer doc.Close()

	if doc.Title != "" {
		fmt.Fprintf(stdout, "Title:    %s\n", doc.Title)
	}
	if doc.Metadata.Author != "" {
		fmt.Fprintf(stdout, "Author:   %s\n", doc.Metadata.Author)
	}
	if doc.Metadata.Producer != "" {
		fmt.Fprintf(stdout, "Producer: %s\n", doc.Metadata.Producer)
	}
	fmt.Fprintf(stdout, "Pages:    %d\n", doc.PageCount)
	for _, p := range doc.Pages {
		fmt.Fprintf(stdout, "  page %d: %.0f x %.0f pt", p.Index, p.Width, p.Height)
		if p.Rotation != 0 {
			fmt.Fprintf(stdout, " (rotated %d)", p.Rotation)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func cmdExtract(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("extract", stderr)
	page := fs.Int("page", -1, "extract a single 0-based page (default: all)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "extract: expected exactly one PDF path")
	}
	doc, err := core.OpenDocument(pos[0])
	if err != nil {
		return fail(stderr, "extract: %v", err)
	}
	defer doc.Close()

	var text string
	if *page >= 0 {
		text, err = doc.ExtractTextByPage(*page, core.ExtractOptions{})
	} else {
		text, err = doc.ExtractText(core.ExtractOptions{})
	}
	if err != nil {
		return fail(stderr, "extract: %v", err)
	}
	fmt.Fprintln(stdout, text)
	return 0
}

func cmdRender(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("render", stderr)
	page := fs.Int("page", 0, "0-based page index to render")
	dpi := fs.Int("dpi", 150, "output resolution in DPI")
	format := fs.String("format", "png", "output format: png or jpeg")
	quality := fs.Int("quality", 90, "JPEG quality (1-100)")
	out := fs.String("o", "", "output file (default: <input>-p<page>.<format>)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "render: expected exactly one PDF path")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "render: %v", err)
	}
	defer doc.Close()

	img, err := doc.RenderPage(*page, core.RenderOptions{DPI: *dpi, Format: *format, Quality: *quality})
	if err != nil {
		return fail(stderr, "render: %v", err)
	}
	outPath := *out
	if outPath == "" {
		ext := *format
		if ext == "jpeg" {
			ext = "jpg"
		}
		outPath = fmt.Sprintf("%s-p%d.%s", trimPDFExt(in), *page, ext)
	}
	if err := os.WriteFile(outPath, img, 0o644); err != nil {
		return fail(stderr, "render: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", outPath, len(img))
	return 0
}

func cmdMerge(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("merge", stderr)
	out := fs.String("o", "", "output PDF path (required)")
	inputs, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if *out == "" {
		return fail(stderr, "merge: -o output path is required")
	}
	if len(inputs) < 2 {
		return fail(stderr, "merge: expected at least two input PDFs")
	}

	var merged *core.Document
	for _, path := range inputs {
		doc, err := core.OpenDocument(path)
		if err != nil {
			return fail(stderr, "merge: %s: %v", path, err)
		}
		if merged == nil {
			merged = doc
			continue
		}
		next, err := merged.Merge(doc)
		doc.Close()
		if err != nil {
			return fail(stderr, "merge: %v", err)
		}
		merged = next
	}
	if err := merged.Save(*out); err != nil {
		return fail(stderr, "merge: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s (%d pages)\n", *out, merged.PageCount)
	return 0
}

func cmdSplit(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("split", stderr)
	ranges := fs.String("ranges", "", "comma-separated 0-based inclusive page ranges, e.g. 0-1,2-3")
	prefix := fs.String("o", "", "output filename prefix (default: <input>-part)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "split: expected exactly one PDF path")
	}
	if *ranges == "" {
		return fail(stderr, "split: --ranges is required")
	}
	pr, err := parseRanges(*ranges)
	if err != nil {
		return fail(stderr, "split: %v", err)
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "split: %v", err)
	}
	defer doc.Close()

	parts, err := doc.Split(pr)
	if err != nil {
		return fail(stderr, "split: %v", err)
	}
	pfx := *prefix
	if pfx == "" {
		pfx = trimPDFExt(in) + "-part"
	}
	for i, part := range parts {
		path := fmt.Sprintf("%s-%d.pdf", pfx, i+1)
		if err := part.Save(path); err != nil {
			return fail(stderr, "split: %v", err)
		}
		fmt.Fprintf(stdout, "wrote %s (%d pages)\n", path, part.PageCount)
	}
	return 0
}

func cmdRotate(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("rotate", stderr)
	page := fs.Int("page", -1, "0-based page index to rotate (required)")
	degrees := fs.Int("degrees", 0, "rotation in degrees, a multiple of 90")
	out := fs.String("o", "", "output PDF path (default: <input>-rotated.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "rotate: expected exactly one PDF path")
	}
	if *page < 0 {
		return fail(stderr, "rotate: --page is required")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "rotate: %v", err)
	}
	defer doc.Close()

	if err := doc.Rotate(*page, *degrees); err != nil {
		return fail(stderr, "rotate: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-rotated.pdf"
	}
	if err := doc.Save(outPath); err != nil {
		return fail(stderr, "rotate: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

func cmdWatermark(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("watermark", stderr)
	text := fs.String("text", "", "watermark text (required)")
	opacity := fs.Float64("opacity", 0.3, "opacity 0..1")
	size := fs.Float64("size", 48, "font size in points")
	rotation := fs.Float64("rotation", 45, "rotation in degrees")
	color := fs.String("color", "", "hex color, e.g. #888888")
	out := fs.String("o", "", "output PDF path (default: <input>-watermarked.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "watermark: expected exactly one PDF path")
	}
	if *text == "" {
		return fail(stderr, "watermark: --text is required")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "watermark: %v", err)
	}
	defer doc.Close()

	marked, err := doc.Watermark(core.WatermarkOptions{
		Text: *text, Opacity: *opacity, FontSize: *size, Rotation: *rotation, Color: *color,
	})
	if err != nil {
		return fail(stderr, "watermark: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-watermarked.pdf"
	}
	if err := marked.Save(outPath); err != nil {
		return fail(stderr, "watermark: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

func cmdRedact(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("redact", stderr)
	pattern := fs.String("pattern", "", "regular expression to redact (required)")
	page := fs.Int("page", -1, "0-based page to redact (default: all pages)")
	out := fs.String("o", "", "output PDF path (default: <input>-redacted.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "redact: expected exactly one PDF path")
	}
	if *pattern == "" {
		return fail(stderr, "redact: --pattern is required")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "redact: %v", err)
	}
	defer doc.Close()

	total := 0
	for p := 0; p < doc.PageCount; p++ {
		if *page >= 0 && p != *page {
			continue
		}
		rects, err := doc.RedactText(p, *pattern)
		if err != nil {
			return fail(stderr, "redact: page %d: %v", p, err)
		}
		total += len(rects)
	}
	redacted, err := doc.RedactAndFlatten()
	if err != nil {
		return fail(stderr, "redact: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-redacted.pdf"
	}
	if err := redacted.Save(outPath); err != nil {
		return fail(stderr, "redact: %v", err)
	}
	fmt.Fprintf(stdout, "redacted %d region(s), wrote %s\n", total, outPath)
	if r, ok := redacted.RedactionReport(); ok && r.Summary() != "" {
		fmt.Fprintln(stdout, r.Summary())
	}
	return 0
}

func cmdAutoRedact(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("autoredact", stderr)
	kinds := fs.String("kinds", "", "comma-separated PII kinds to redact: email,ssn,phone,credit_card (default: all)")
	out := fs.String("o", "", "output PDF path (default: <input>-redacted.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "autoredact: expected exactly one PDF path")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "autoredact: %v", err)
	}
	defer doc.Close()

	var selected []core.PIIKind
	if *kinds != "" {
		selected = core.PIIKindsFromStrings(strings.Split(*kinds, ","))
		if len(selected) == 0 {
			return fail(stderr, "autoredact: no recognized kinds in %q", *kinds)
		}
	}
	counts, err := doc.AutoRedactPII(selected...)
	if err != nil {
		return fail(stderr, "autoredact: %v", err)
	}
	redacted, err := doc.RedactAndFlatten()
	if err != nil {
		return fail(stderr, "autoredact: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-redacted.pdf"
	}
	if err := redacted.Save(outPath); err != nil {
		return fail(stderr, "autoredact: %v", err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	fmt.Fprintf(stdout, "auto-redacted %d PII region(s) %v, wrote %s\n", total, counts, outPath)
	if r, ok := redacted.RedactionReport(); ok && r.Summary() != "" {
		fmt.Fprintln(stdout, r.Summary())
	}
	return 0
}

func cmdSign(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("sign", stderr)
	certPath := fs.String("cert", "", "PEM certificate file (required)")
	keyPath := fs.String("key", "", "PEM private key file (required)")
	reason := fs.String("reason", "", "signing reason")
	location := fs.String("location", "", "signing location")
	out := fs.String("o", "", "output PDF path (default: <input>-signed.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "sign: expected exactly one PDF path")
	}
	if *certPath == "" || *keyPath == "" {
		return fail(stderr, "sign: --cert and --key are required")
	}
	certPEM, err := os.ReadFile(*certPath)
	if err != nil {
		return fail(stderr, "sign: %v", err)
	}
	keyPEM, err := os.ReadFile(*keyPath)
	if err != nil {
		return fail(stderr, "sign: %v", err)
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "sign: %v", err)
	}
	defer doc.Close()

	signed, err := doc.SignDocument(core.SignatureOptions{
		CertPEM: certPEM, KeyPEM: keyPEM, Reason: *reason, Location: *location,
	})
	if err != nil {
		return fail(stderr, "sign: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-signed.pdf"
	}
	// Write the exact bytes: re-serializing would invalidate the signature's
	// byte range.
	if err := writeExact(signed, outPath); err != nil {
		return fail(stderr, "sign: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

// writeExact writes a document's verbatim bytes to path (preserving signatures /
// PDF-A markers that a rebuild would drop).
func writeExact(doc *core.Document, path string) error {
	b, err := doc.Bytes()
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func cmdCompare(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("compare", stderr)
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 2 {
		return fail(stderr, "compare: expected exactly two PDF paths")
	}
	a, err := core.OpenDocument(pos[0])
	if err != nil {
		return fail(stderr, "compare: %s: %v", pos[0], err)
	}
	defer a.Close()
	b, err := core.OpenDocument(pos[1])
	if err != nil {
		return fail(stderr, "compare: %s: %v", pos[1], err)
	}
	defer b.Close()

	result, err := a.Compare(b)
	if err != nil {
		return fail(stderr, "compare: %v", err)
	}
	if result.Identical {
		fmt.Fprintln(stdout, "documents are identical")
		return 0
	}
	for _, pd := range result.Pages {
		if pd.Identical {
			continue
		}
		fmt.Fprintf(stdout, "page %d:\n", pd.PageIndex)
		for _, s := range pd.Segments {
			switch s.Op {
			case core.DiffInsert:
				fmt.Fprintf(stdout, "  + %s\n", s.Text)
			case core.DiffDelete:
				fmt.Fprintf(stdout, "  - %s\n", s.Text)
			}
		}
	}
	return 0
}

func cmdPDFA(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pdfa", stderr)
	validate := fs.Bool("validate", false, "validate PDF/A markers instead of converting")
	out := fs.String("o", "", "output PDF path (default: <input>-pdfa.pdf)")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return fail(stderr, "pdfa: expected exactly one PDF path")
	}
	in := pos[0]
	doc, err := core.OpenDocument(in)
	if err != nil {
		return fail(stderr, "pdfa: %v", err)
	}
	defer doc.Close()

	if *validate {
		report, err := doc.ValidatePDFA()
		if err != nil {
			return fail(stderr, "pdfa: %v", err)
		}
		for _, c := range report.Checks {
			mark := "PASS"
			if !c.Pass {
				mark = "FAIL"
			}
			fmt.Fprintf(stdout, "[%s] %s", mark, c.Rule)
			if c.Note != "" {
				fmt.Fprintf(stdout, " — %s", c.Note)
			}
			fmt.Fprintln(stdout)
		}
		if !report.Conformant {
			return 1
		}
		return 0
	}

	pdfaDoc, err := doc.ConvertToPDFA()
	if err != nil {
		return fail(stderr, "pdfa: %v", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = trimPDFExt(in) + "-pdfa.pdf"
	}
	// Write the exact bytes: a rebuild would drop the PDF/A metadata + OutputIntent.
	if err := writeExact(pdfaDoc, outPath); err != nil {
		return fail(stderr, "pdfa: %v", err)
	}
	fmt.Fprintf(stdout, "wrote %s\n", outPath)
	return 0
}

// parseRanges parses "0-1,2-3,5" into inclusive [start,end] pairs. A bare "5"
// means the single-page range [5,5].
func parseRanges(s string) ([][2]int, error) {
	var out [][2]int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, found := strings.Cut(part, "-"); found {
			a, err1 := strconv.Atoi(strings.TrimSpace(lo))
			b, err2 := strconv.Atoi(strings.TrimSpace(hi))
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid range %q", part)
			}
			out = append(out, [2]int{a, b})
		} else {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid page %q", part)
			}
			out = append(out, [2]int{n, n})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no ranges given")
	}
	return out, nil
}

// trimPDFExt strips a trailing ".pdf"/".PDF" from a path for building output
// names.
func trimPDFExt(path string) string {
	if len(path) >= 4 && strings.EqualFold(path[len(path)-4:], ".pdf") {
		return path[:len(path)-4]
	}
	return path
}
