package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thai-pc/fluxdocs/core/render"
)

// budgetPageSpec is one page of budgetDoc: its /MediaBox and content stream.
type budgetPageSpec struct {
	mediaBox string
	content  string
}

// budgetDoc builds an opened Document with one page per spec.
func budgetDoc(t *testing.T, pages ...budgetPageSpec) *Document {
	t.Helper()
	objs := map[int]string{1: "<< /Type /Catalog /Pages 2 0 R >>"}
	kids := ""
	num := 3
	for _, p := range pages {
		pageObj, contentObj := num, num+1
		num += 2
		kids += fmt.Sprintf("%d 0 R ", pageObj)
		objs[pageObj] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox %s /Contents %d 0 R >>", p.mediaBox, contentObj)
		objs[contentObj] = coreStreamObj(p.content)
	}
	objs[2] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(pages))
	doc, err := OpenBytes(buildCorePDF(objs, fmt.Sprintf("<< /Size %d /Root 1 0 R >>", num)))
	if err != nil {
		t.Fatalf("OpenBytes: %v", err)
	}
	return doc
}

var (
	// plainPage is a cheap, well-formed 20x20pt page.
	plainPage = budgetPageSpec{"[0 0 20 20]", "0 0 0 rg 0 0 20 20 re f"}
	// floodPage repeats a full-page fill: 20x20 px per fill at DPI 72, so 2000
	// fills cost ≈ 0.9 M paint units and trip any limit below that.
	floodPage = budgetPageSpec{"[0 0 20 20]", strings.Repeat("0 0 20 20 re f\n", 2000)}
	// canvasPage carries a 2^33 pt MediaBox: over the canvas cap at any DPI.
	canvasPage = budgetPageSpec{"[0 0 8589934592 8589934592]", "0 0 0 rg 0 0 20 20 re f"}
)

// heavyPage is a page whose fills take seconds with an unlimited budget at the
// given DPI, so a deadline fires mid-render.
func heavyPage() budgetPageSpec {
	return budgetPageSpec{"[0 0 100 100]", strings.Repeat("0 0 100 100 re f\n", 20000)}
}

func TestRenderPage_BudgetExceeded(t *testing.T) {
	doc := budgetDoc(t, plainPage, floodPage)
	var fired []int
	doc.OnPageRendered(func(idx int, _ []byte) { fired = append(fired, idx) })

	img, err := doc.RenderPage(1, RenderOptions{DPI: 72, MaxPaintPixels: 100_000})
	if !errors.Is(err, ErrRenderBudgetExceeded) || !errors.Is(err, render.ErrBudgetExceeded) {
		t.Fatalf("err = %v, want ErrRenderBudgetExceeded wrapping render.ErrBudgetExceeded", err)
	}
	for _, wrong := range []error{ErrInvalidPDF, ErrPageNotFound, ErrInternal, render.ErrCanvasTooLarge, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, wrong) {
			t.Errorf("err = %v also matches %v", err, wrong)
		}
	}
	if !strings.Contains(err.Error(), "page 1") || !strings.Contains(err.Error(), "paint work") {
		t.Errorf("err = %q, want the page index and the resource", err)
	}
	if img != nil {
		t.Errorf("got %d bytes with the error, want nil", len(img))
	}
	if len(fired) != 0 {
		t.Errorf("OnPageRendered fired for %v, want no callback for a failed render", fired)
	}

	// The default budget allows the same page; unlimited does too.
	for _, opts := range []RenderOptions{{DPI: 72}, {DPI: 72, MaxPaintPixels: -1, MaxOperators: -1}} {
		if _, err := doc.RenderPage(1, opts); err != nil {
			t.Errorf("RenderPage(%+v): %v", opts, err)
		}
	}

	if _, err := doc.RenderPage(1, RenderOptions{DPI: 72, MaxOperators: 100}); !errors.Is(err, ErrRenderBudgetExceeded) {
		t.Errorf("MaxOperators 100: err = %v, want ErrRenderBudgetExceeded", err)
	}
}

func TestRenderPageContext_Deadline(t *testing.T) {
	doc := budgetDoc(t, heavyPage())
	var fired atomic.Int32
	doc.OnPageRendered(func(int, []byte) { fired.Add(1) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	img, err := doc.RenderPageContext(ctx, 0, RenderOptions{DPI: 300, MaxPaintPixels: -1, MaxOperators: -1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrRenderBudgetExceeded) {
		t.Errorf("a deadline must not be ErrRenderBudgetExceeded: %v", err)
	}
	if img != nil || fired.Load() != 0 {
		t.Errorf("got %d bytes and %d callbacks, want none", len(img), fired.Load())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("returned after %v, want shortly after the deadline", elapsed)
	}

	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := doc.RenderPageContext(canceled, 0, RenderOptions{DPI: 72}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled ctx: err = %v, want context.Canceled", err)
	}
	// Argument errors still win over a done ctx.
	if _, err := doc.RenderPageContext(canceled, 9, RenderOptions{}); err != ErrPageNotFound {
		t.Errorf("bad index with canceled ctx: err = %v, want ErrPageNotFound", err)
	}
	var nilCtx context.Context // documented as context.Background()
	if _, err := budgetDoc(t, plainPage).RenderPageContext(nilCtx, 0, RenderOptions{DPI: 36}); err != nil {
		t.Errorf("nil ctx: %v", err)
	}
}

// TestRenderAllPagesContext_FailureCancelsBatch checks the batch contract: the
// error is the hostile page's budget error (never the internal cancellation it
// caused), results are nil, and the pages not yet started are canceled rather
// than rendered.
func TestRenderAllPagesContext_FailureCancelsBatch(t *testing.T) {
	// Many slow-ish pages after a hostile first page: without cancellation all
	// of them would render and fire the callback.
	slow := budgetPageSpec{"[0 0 100 100]", strings.Repeat("0 0 100 100 re f\n", 200)}
	specs := []budgetPageSpec{floodPage}
	for i := 0; i < 63; i++ {
		specs = append(specs, slow)
	}
	doc := budgetDoc(t, specs...)
	var fired atomic.Int32
	doc.OnPageRendered(func(int, []byte) { fired.Add(1) })

	imgs, err := doc.RenderAllPagesContext(context.Background(), RenderOptions{DPI: 150, MaxPaintPixels: 100_000_000, MaxOperators: 1000})
	if !errors.Is(err, ErrRenderBudgetExceeded) {
		t.Fatalf("err = %v, want ErrRenderBudgetExceeded", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, errBatchAborted) {
		t.Errorf("err = %v, want the root cause, not the batch cancellation", err)
	}
	if !strings.Contains(err.Error(), "page 0") {
		t.Errorf("err = %q, want page 0's error", err)
	}
	if imgs != nil {
		t.Errorf("got %d images with the error, want nil", len(imgs))
	}
	if n := fired.Load(); n >= int32(len(specs)-1) {
		t.Errorf("OnPageRendered fired %d times, want the remaining pages canceled", n)
	}
}

// TestRenderAllPagesContext_RootCauseNotLowestAborted puts the hostile page in
// the middle: lower pages may be aborted by it, but the batch must return its
// error, never an aborted page's.
func TestRenderAllPagesContext_RootCauseNotLowestAborted(t *testing.T) {
	slow := budgetPageSpec{"[0 0 100 100]", strings.Repeat("0 0 100 100 re f\n", 300)}
	specs := []budgetPageSpec{slow, slow, slow, slow, slow, canvasPage, slow, slow}
	doc := budgetDoc(t, specs...)
	for i := 0; i < 5; i++ {
		_, err := doc.RenderAllPagesContext(context.Background(), RenderOptions{DPI: 150})
		if !errors.Is(err, render.ErrCanvasTooLarge) || !errors.Is(err, ErrRenderBudgetExceeded) {
			t.Fatalf("run %d: err = %v, want page 5's canvas error", i, err)
		}
		if !strings.Contains(err.Error(), "page 5") || !strings.Contains(err.Error(), "exceeds") {
			t.Errorf("run %d: err = %q, want page 5's canvas error", i, err)
		}
	}
}

// TestRenderAllPagesContext_ParentWins checks the caller's ctx error is returned
// whenever the parent is done, even if a page also failed.
func TestRenderAllPagesContext_ParentWins(t *testing.T) {
	t.Run("canceled before start", func(t *testing.T) {
		doc := budgetDoc(t, plainPage, canvasPage, plainPage)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		imgs, err := doc.RenderAllPagesContext(ctx, RenderOptions{DPI: 72})
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrRenderBudgetExceeded) {
			t.Fatalf("err = %v, want the parent's context.Canceled", err)
		}
		if imgs != nil {
			t.Errorf("got %d images, want nil", len(imgs))
		}
	})
	t.Run("deadline mid-batch", func(t *testing.T) {
		doc := budgetDoc(t, heavyPage(), heavyPage(), heavyPage())
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := doc.RenderAllPagesContext(ctx, RenderOptions{DPI: 300, MaxPaintPixels: -1})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("returned after %v, want shortly after the deadline", elapsed)
		}
	})
	t.Run("custom cause", func(t *testing.T) {
		doc := budgetDoc(t, plainPage)
		cause := errors.New("shutting down")
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		_, err := doc.RenderAllPagesContext(ctx, RenderOptions{DPI: 72})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want both context.Canceled and the parent's cause", err)
		}
	})
	t.Run("custom timeout cause", func(t *testing.T) {
		doc := budgetDoc(t, plainPage)
		cause := errors.New("request budget spent")
		ctx, cancel := context.WithTimeoutCause(context.Background(), time.Nanosecond, cause)
		defer cancel()
		<-ctx.Done()
		_, err := doc.RenderAllPagesContext(ctx, RenderOptions{DPI: 72})
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, cause) {
			t.Fatalf("err = %v, want both context.DeadlineExceeded and the parent's cause", err)
		}
	})
	// The rule is "the caller's ctx wins" even when it ends after the last page
	// rendered: the batch reports the ctx error and discards the results, so a
	// caller never mistakes a canceled request for a delivered one.
	t.Run("canceled after every page rendered", func(t *testing.T) {
		doc := budgetDoc(t, plainPage)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var fired int
		doc.OnPageRendered(func(int, []byte) { fired++; cancel() })
		imgs, err := doc.RenderAllPagesContext(ctx, RenderOptions{DPI: 72})
		if fired != 1 {
			t.Fatalf("OnPageRendered fired %d times, want 1", fired)
		}
		if !errors.Is(err, context.Canceled) || imgs != nil {
			t.Fatalf("err = %v (%d images), want context.Canceled and no images", err, len(imgs))
		}
	})
}

// TestRenderAllPagesContext_Concurrent hammers the pool with failing and
// succeeding batches at once; run with -race.
func TestRenderAllPagesContext_Concurrent(t *testing.T) {
	good := budgetDoc(t, plainPage, plainPage, plainPage, plainPage)
	bad := budgetDoc(t, plainPage, floodPage, plainPage, floodPage)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if imgs, err := good.RenderAllPagesContext(context.Background(), RenderOptions{DPI: 72}); err != nil || len(imgs) != 4 {
				t.Errorf("good batch: %d images, err %v", len(imgs), err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := bad.RenderAllPagesContext(context.Background(), RenderOptions{DPI: 72, MaxPaintPixels: 50_000}); !errors.Is(err, ErrRenderBudgetExceeded) {
				t.Errorf("bad batch: err = %v, want ErrRenderBudgetExceeded", err)
			}
		}()
	}
	wg.Wait()
}
