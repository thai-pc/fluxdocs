package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/thai-pc/fluxdocs/core/parse"
)

// This file holds the per-page render budget (SECURITY.md §2; design:
// docs/design/render-budget.md). Each paint operator is already bounded by the
// canvas; the budget bounds how many of them a page may run, so a hostile
// content stream cannot pin a CPU for minutes. It also carries the caller's
// context, polled at an amortized interval, so a server deadline or a client
// disconnect stops the render.

// ErrBudgetExceeded is returned (wrapped, naming the resource) when a page render
// exceeds a resource limit: raster work, interpreter steps, image samples,
// content bytes, decoded bytes, or path points. core wraps it with ErrRenderBudgetExceeded.
var ErrBudgetExceeded = errors.New("render: budget exceeded")

// ErrCanvasTooLarge is returned (wrapped) when the page's output canvas exceeds
// maxCanvasPixels. Its message keeps "exceeds"; core wraps it with
// ErrRenderBudgetExceeded, so both sentinels match with errors.Is.
var ErrCanvasTooLarge = errors.New("render: canvas exceeds pixel cap")

const (
	// paintOverdraw is the default raster-work allowance in multiples of the
	// canvas area. Real pages touch each pixel a few times (background, images,
	// text, overlay); 32× leaves an order of magnitude of headroom.
	paintOverdraw = 32
	// minPaintBudget is the default raster-work floor, so tiny thumbnails do not
	// fail on per-row or per-edge overhead.
	minPaintBudget = 16 << 20
	// maxPaintBudget is the default raster-work ceiling: ≈ 5 s native, ≈ 25 s
	// wasm at the measured per-visit cost (render-budget.md §3).
	maxPaintBudget = 1 << 30

	// defaultMaxOperators is the default interpreter-step limit (content
	// operators at every form depth plus glyphs shown). The costliest step
	// without painting, a glyph advanced in an invisible render mode, measured
	// ≈ 87 ns native (≈ 0.73 s for 8 Mi) and ≈ 270 ns under js/wasm
	// (BenchmarkCalibration/step-*; render-budget.md §3).
	defaultMaxOperators = 8 << 20

	// maxImageSamples bounds the decoded image samples per page. It is
	// DPI-independent: a scanned page decodes its full sample grid even for a
	// 72-DPI thumbnail.
	maxImageSamples = 256 << 20
	// maxContentBytes bounds the decoded content bytes per page: the page's own
	// content plus every Form XObject invocation (2 × the parser's decode cap).
	maxContentBytes = 512 << 20
	// maxDecodedBytes bounds, per page, the stream bytes decoded for images,
	// image masks, soft masks and font programs beyond what their samples
	// already pay for (budget.decode): a bomb's excess, a font program, a filter
	// chain's intermediate stage, a decode that fails part-way. Without it a
	// 1×1 image whose Flate body inflates to 256 MiB cost one sample per Do.
	// Legitimate images decode about their sample bytes, so this is spent by
	// font programs and malformed data only; 512 MiB is ≈ 1–2 s of inflate.
	maxDecodedBytes = 512 << 20
	// maxPathPoints bounds the points in the current path (≈ 64 MiB of points).
	// It is not cumulative: painting the path or n resets it.
	maxPathPoints = 4 << 20

	// lineCallCost is the raster-work charge per stroke segment (line call),
	// charged before anything else, so segments clipped away entirely still
	// pay their setup (clipping, sample count, row range). Calibrated by
	// BenchmarkCalibration/line-dots.
	lineCallCost = 16
	// lineRowCost is the raster-work charge per row of a stroke segment, on top
	// of the row's span width. It covers the per-row search for the samples
	// that reach the row, which resumes from the previous row's result
	// (firstTrueNear), so a thin segment costs a few predicate calls per row.
	// Calibrated by BenchmarkCalibration/line-row (8192-row segment); the
	// per-row bisection it replaced measured ≈ 50 (render-budget.md §10).
	lineRowCost = 14

	// fillPointCost is fillPath's setup charge per path point: building the
	// edge, finding its entry row, ordering the edges and entering them into
	// the active table. Calibrated by BenchmarkCalibration/fill-setup.
	fillPointCost = 4
	// fillCrossingCost is fillPath's per-row charge per active edge, on top of
	// bits.Len(k) per edge for sorting a row of k crossings (fillRowCost):
	// filtering the edge and computing its crossing. Calibrated by
	// BenchmarkCalibration/fill-dense-row.
	fillCrossingCost = 2

	// imagePixelCost is drawImage's charge per pixel of the placement's device
	// box: an inverse-mapped sample lookup plus the composite. Calibrated by
	// BenchmarkCalibration/image-pixel.
	imagePixelCost = 2

	// doCost is the raster-work charge per Do operator, charged before the
	// XObject is resolved: the resource lookup, the image's dictionary checks,
	// and the decode and allocation of even a 1×1 image, which cost far more
	// than the one interpreter step a Do also pays. Calibrated by
	// BenchmarkCalibration/do-tiny-image.
	doCost = 96

	// glyphPointCost is the raster-work charge per point of a visible glyph's
	// flattened outline, on top of fillPath's fillPointCost setup charge. The
	// two together cover LoadGlyph, the curve flattening, the per-glyph
	// allocations and the fill setup, which dominate a small glyph. Calibrated
	// by BenchmarkCalibration/glyph-tiny-visible.
	glyphPointCost = 6

	// pollPaintQuantum and pollStepQuantum set how much work accumulates
	// between context polls: ≈ 5 ms native / 25 ms wasm of raster work.
	pollPaintQuantum = 1 << 20
	pollStepQuantum  = 4096
)

// unlimited is the internal limit for a disabled counter. A counter only grows
// by bounded per-row or per-call amounts, so it cannot reach it.
const unlimited = math.MaxInt64

// budgetLimits holds one render's limits. A value of unlimited disables that
// counter.
type budgetLimits struct {
	paint      int64 // raster work, pixel visits
	steps      int64 // interpreter steps
	samples    int64 // decoded image samples
	content    int64 // decoded content bytes
	decoded    int64 // decoded image and font bytes beyond their samples (decode)
	pathPoints int64 // points in the current path
}

// budget meters one page render. It is owned by the page's single goroutine, so
// it needs no synchronization. The first trip (a limit or a done context) is
// sticky: every later charge reports false, so the operator loop and every
// paint routine stop at their next row or operator.
//
// The two hot counters (paint, steps) compare against their limit and against
// the poll quantum with one add and one compare each; everything else happens
// in the slow path. Once stopped, both limits are forced to -1 so every charge
// takes the slow path and fails.
type budget struct {
	paint, paintLimit int64
	steps, stepLimit  int64
	samples           int64
	content           int64
	decoded           int64
	lim               budgetLimits // the configured limits, for messages and the cold counters

	// Work since the last context poll.
	paintSincePoll int64
	stepSincePoll  int64

	ctx         context.Context
	deadline    time.Time
	hasDeadline bool

	err error // first trip; nil while running
}

// newBudget returns a budget enforcing lim under ctx (nil = Background). The
// context's deadline is read once here, so polls can compare the clock directly
// (see poll).
func newBudget(ctx context.Context, lim budgetLimits) *budget {
	if ctx == nil {
		ctx = context.Background()
	}
	b := &budget{
		paintLimit: lim.paint,
		stepLimit:  lim.steps,
		lim:        lim,
		ctx:        ctx,
	}
	b.deadline, b.hasDeadline = ctx.Deadline()
	return b
}

// unlimitedBudget returns a budget that never trips and has no context. A raster
// built directly (newRaster, as in unit tests) uses it; render.Page installs the
// real one.
func unlimitedBudget() *budget {
	return newBudget(context.Background(), budgetLimits{
		paint: unlimited, steps: unlimited, samples: unlimited, content: unlimited, decoded: unlimited, pathPoints: unlimited,
	})
}

// resolveLimits turns the public option values into limits for a canvas of
// canvasPx pixels: 0 = default, < 0 = unlimited, > 0 = absolute. The image,
// content and path caps are internal and always on.
func resolveLimits(maxPaint, maxOps, canvasPx int64) budgetLimits {
	lim := budgetLimits{
		paint:      maxPaint,
		steps:      maxOps,
		samples:    maxImageSamples,
		content:    maxContentBytes,
		decoded:    maxDecodedBytes,
		pathPoints: maxPathPoints,
	}
	switch {
	case maxPaint < 0:
		lim.paint = unlimited
	case maxPaint == 0:
		lim.paint = defaultPaintBudget(canvasPx)
	}
	switch {
	case maxOps < 0:
		lim.steps = unlimited
	case maxOps == 0:
		lim.steps = defaultMaxOperators
	}
	return lim
}

// defaultPaintBudget is paintOverdraw × canvasPx clamped to [minPaintBudget,
// maxPaintBudget]. canvasPx ≤ maxCanvasPixels, so the product fits an int64.
func defaultPaintBudget(canvasPx int64) int64 {
	v := paintOverdraw * canvasPx
	if v < minPaintBudget {
		return minPaintBudget
	}
	if v > maxPaintBudget {
		return maxPaintBudget
	}
	return v
}

// stopped reports whether the render has tripped a limit or seen a done context.
func (b *budget) stopped() bool { return b.err != nil }

// chargePaint adds n raster-work units (pixel visits or edge visits). It reports
// false, and paints nothing more, once the budget is stopped; the caller must
// return without doing the charged work.
func (b *budget) chargePaint(n int64) bool {
	b.paint += n
	b.paintSincePoll += n
	if b.paint > b.paintLimit || b.paintSincePoll >= pollPaintQuantum {
		return b.paintSlow()
	}
	return true
}

func (b *budget) paintSlow() bool {
	if b.err != nil {
		return false
	}
	if b.paint > b.paintLimit {
		b.trip("paint work", b.paint, b.lim.paint)
		return false
	}
	return b.poll()
}

// step adds one interpreter step (an operator, a glyph, or a TJ element). Like
// chargePaint it reports false once stopped.
func (b *budget) step() bool {
	b.steps++
	b.stepSincePoll++
	if b.steps > b.stepLimit || b.stepSincePoll >= pollStepQuantum {
		return b.stepSlow()
	}
	return true
}

func (b *budget) stepSlow() bool {
	if b.err != nil {
		return false
	}
	if b.steps > b.stepLimit {
		b.trip("operators", b.steps, b.lim.steps)
		return false
	}
	return b.poll()
}

// chargeSamples adds n decoded image samples before the decode runs; false means
// skip the decode (and stop the render). It also polls the context: one decode
// can cost far more than a poll quantum of paint or steps (a few Do operators
// can each decode millions of samples), so each decode is a poll point.
func (b *budget) chargeSamples(n int64) bool {
	if b.err != nil {
		return false
	}
	b.samples += n
	if b.samples > b.lim.samples {
		b.trip("image samples", b.samples, b.lim.samples)
		return false
	}
	return b.poll()
}

// chargeContent adds n decoded content bytes before they are scanned; false
// means skip the scan (and stop the render). Like chargeSamples it polls the
// context, since each form decode can be large.
func (b *budget) chargeContent(n int64) bool {
	if b.err != nil {
		return false
	}
	b.content += n
	if b.content > b.lim.content {
		b.trip("content bytes", b.content, b.lim.content)
		return false
	}
	return b.poll()
}

// chargeDecoded adds n decoded bytes (see decode) for a decode that does not go
// through decode itself (a JPEG's compressed data); false means skip it.
func (b *budget) chargeDecoded(n int64) bool {
	if b.err != nil {
		return false
	}
	if n <= 0 {
		return true
	}
	b.decoded += n
	if b.decoded > b.lim.decoded {
		b.trip("decoded bytes", b.decoded, b.lim.decoded)
		return false
	}
	return b.poll()
}

// decode runs st's filters for an image, mask or font program.
// free is the output its own counter already paid for (an image's sample
// bytes, charged to image samples). Every other byte the filters produce is
// charged to decoded bytes: a bomb's excess, a font program, a chain's
// intermediate stage, a decode that fails part-way. The decode is capped at
// free plus what is left of that counter (parse.DecodeStreamLimit), so one
// call cannot run past the budget, and its data and error are otherwise
// exactly parse.DecodeStream's. ok=false means skip the stream: a decode error
// (silent, as before), or a trip, which stops the render.
func (b *budget) decode(st *parse.Stream, free int64) ([]byte, bool) {
	return b.decodeInto(st, free, &b.decoded, b.lim.decoded, "decoded bytes", false)
}

// decodeContent is decode for a Form XObject's content stream: every byte the
// filters produce is charged to content bytes, before the content is scanned,
// and so is an unfiltered stream's length, since all of it is scanned.
func (b *budget) decodeContent(st *parse.Stream) ([]byte, bool) {
	return b.decodeInto(st, 0, &b.content, b.lim.content, "content bytes", true)
}

// decodeInto is decode charging *used against limit, naming resource on a
// trip. With chargeRaw, an unfiltered stream is charged its length.
func (b *budget) decodeInto(st *parse.Stream, free int64, used *int64, limit int64, resource string, chargeRaw bool) ([]byte, bool) {
	if b.err != nil {
		return nil, false
	}
	allow := limit - *used // ≥ 0 while running
	if allow > math.MaxInt64-free {
		allow = math.MaxInt64
	} else {
		allow += free
	}
	data, produced, err := parse.DecodeStreamLimit(st, allow)
	if chargeRaw && err == nil && produced == 0 {
		produced = int64(len(data)) // no filter ran: the raw bytes are the content
	}
	if charge := produced - free; charge > 0 {
		*used += charge
	}
	if errors.Is(err, parse.ErrDecodeLimit) || *used > limit {
		b.trip(resource, *used, limit)
		return nil, false
	}
	// Like chargeSamples, a decode can outweigh a poll quantum: poll.
	if !b.poll() || err != nil {
		return nil, false
	}
	return data, true
}

// checkPathPoints reports whether a current path of n points is allowed; false
// means drop the path-construction operator (and stop the render).
func (b *budget) checkPathPoints(n int64) bool {
	if b.err != nil {
		return false
	}
	if n > b.lim.pathPoints {
		b.trip("path points", n, b.lim.pathPoints)
		return false
	}
	return true
}

// trip records a limit error.
func (b *budget) trip(resource string, used, limit int64) {
	b.stop(fmt.Errorf("%w: %s %d > %d", ErrBudgetExceeded, resource, used, limit))
}

// stop records err as the render's result and forces every later hot-path
// charge into the slow path, where it fails.
func (b *budget) stop(err error) {
	if b.err == nil {
		b.err = err
	}
	b.paintLimit = -1
	b.stepLimit = -1
}

// poll checks the context and resets the poll counters. Besides ctx.Err() it
// compares the clock with the deadline read at construction: under js/wasm an
// export runs synchronously on the single JS thread with no preemption, so the
// timer that would close ctx.Done() never runs while this loop is busy, and
// ctx.Err() alone would never report the deadline.
func (b *budget) poll() bool {
	b.paintSincePoll = 0
	b.stepSincePoll = 0
	if b.err != nil {
		return false
	}
	if b.ctx.Err() != nil {
		b.stop(contextError(b.ctx))
		return false
	}
	if b.hasDeadline && !time.Now().Before(b.deadline) {
		b.stop(fmt.Errorf("render: %w", context.DeadlineExceeded))
		return false
	}
	return true
}

// contextError wraps a done context's error, plus its cause when the canceler
// supplied a distinct one, so errors.Is matches context.Canceled /
// context.DeadlineExceeded and the cause alike.
func contextError(ctx context.Context) error {
	err := ctx.Err()
	if cause := context.Cause(ctx); cause != nil && cause != err {
		return fmt.Errorf("render: %w: %w", err, cause)
	}
	return fmt.Errorf("render: %w", err)
}
