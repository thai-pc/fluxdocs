// Package reltime is test support for timing gates that bound a cost relative
// to a reference measured in the same test, never by an absolute time: CI
// runs on 2-core runners, often loaded, and a slower or busier host slows the
// reference and the measured work alike, so their ratio holds where a fixed
// threshold flakes. It must only be imported from _test.go files: it is
// test support, and production code must not time itself against a host.
//
// A gate measures the work it bounds and a reference — the same operation on
// a quarter of the input for a linearity gate (linear: ≈ 4×, quadratic:
// ≈ 16×), or a benign input of the same size — in runs alternated with each
// other, each repeated to about its clock's target (CPUTarget, Target), and
// bounds the median of the per-run ratios. A reference many gates share may
// instead be measured once (PerCall) and each gate bounded against it
// (AgainstFixed).
//
// Every gate is skipped under -race (Skip). CI's native job runs
// `go test -race ./...`, so the core packages' gates (core/parse,
// core/internal/psfont, core/render, core) run only in the jobs that test
// core without -race (the nostd14 job's `go test -tags nostd14 ./core/...`);
// the testing/security/dos gates run in the native job's own non-race step.
package reltime

import (
	"fmt"
	"runtime"
	"slices"
	"testing"
	"time"
)

// Runs is the number of alternated runs a gate takes the median over by
// default. FewRuns is for a gate whose measured ratio is under half its bound
// (AgainstN, LinearN): the median of three is then as safe and costs less.
const (
	Runs    = 5
	FewRuns = 3
)

// Target is the measured time each side of a run is repeated to on the Wall
// clock: about 1 s on a 2-core CI runner, which runs these workloads ≈ 2×
// slower than the development machines the gates were calibrated on.
// CPUTarget is the same on the CPU clock, which host load moves less, so a
// shorter repetition holds its ratio as well.
const (
	Target    = 500 * time.Millisecond
	CPUTarget = 250 * time.Millisecond
)

// Clock reads a monotonic time: CPU (process CPU time) or Wall, and carries
// the repetition target of gates timed by it.
type Clock struct {
	now    func() time.Duration
	target time.Duration
}

// Now returns the clock's reading.
func (c Clock) Now() time.Duration { return c.now() }

// Target returns the time each side of a run timed by c is repeated to:
// Target for Wall, CPUTarget for CPU.
func (c Clock) Target() time.Duration { return c.target }

var epoch = time.Now()

func wallNow() time.Duration { return time.Since(epoch) }

// Wall is monotonic wall-clock time, for gates on deadlines and on parallel
// work.
var Wall = Clock{now: wallNow, target: Target}

// CPU is the process's CPU time (user + system) where the platform reports it
// (cputime_unix.go), else wall-clock time. A loaded host delays work without
// making it use more CPU, so ratios of CPU times move less than wall-clock
// ones.
var CPU = Clock{now: cpuTime, target: CPUTarget}

// Skip skips a timing gate under -short and under the race detector, which
// slows code unevenly (about tenfold, more for allocation-heavy paths) and
// makes ratios meaningless.
func Skip(t testing.TB) {
	t.Helper()
	if testing.Short() {
		t.Skip("timing gate: skipped under -short")
	}
	if Race {
		t.Skip("timing gate: skipped under -race")
	}
}

// Time runs f once after a GC and returns what clock measured.
func Time(clock Clock, f func()) time.Duration {
	runtime.GC()
	start := clock.Now()
	f()
	return clock.Now() - start
}

// Reps returns how many runs of f take about d by clock, at least 1: f is run
// once to calibrate (which also warms caches).
func Reps(clock Clock, d time.Duration, f func()) int {
	one := Time(clock, f)
	if one <= 0 {
		one = time.Microsecond
	}
	return max(int(d/one), 1)
}

// Repeat returns f repeated n times.
func Repeat(n int, f func()) func() {
	return func() {
		for i := 0; i < n; i++ {
			f()
		}
	}
}

// Result is a comparison: the median of the per-run ratios work/ref, every
// ratio sorted, and the median time of each side.
type Result struct {
	Ratio     float64
	Ratios    []float64
	Ref, Work time.Duration
}

func (r Result) String() string {
	rs := make([]string, len(r.Ratios))
	for i, v := range r.Ratios {
		rs[i] = fmt.Sprintf("%.2f", v)
	}
	return fmt.Sprintf("work/ref median %.2f (runs %v; ref %v, work %v)", r.Ratio, rs, r.Ref, r.Work)
}

// Compare runs ref and work runs times each, alternated — ref first in even
// runs, work first in odd ones, so a drift in host load falls on both — each
// after a GC and timed by clock, and returns the median of the ratios.
func Compare(runs int, clock Clock, ref, work func()) Result {
	return ComparePrepared(runs, clock, 1, Unprepared(ref), Unprepared(work))
}

// Prepared is an operation with an untimed setup: calling it prepares the
// operation (builds and opens a document, say) and returns the part to time.
type Prepared func() func()

// Unprepared is f with no setup.
func Unprepared(f func()) Prepared { return func() func() { return f } }

// TimePrepared runs p reps times after a GC and returns what clock measured of
// the timed parts only.
func TimePrepared(clock Clock, reps int, p Prepared) time.Duration {
	var total time.Duration
	runtime.GC()
	for i := 0; i < reps; i++ {
		f := p()
		start := clock.Now()
		f()
		total += clock.Now() - start
	}
	return total
}

// RepsPrepared is Reps for a Prepared operation.
func RepsPrepared(clock Clock, d time.Duration, p Prepared) int {
	one := max(TimePrepared(clock, 1, p), time.Microsecond)
	return max(int(d/one), 1)
}

// ComparePrepared is Compare for Prepared operations, each side run reps
// times per run.
func ComparePrepared(runs int, clock Clock, reps int, ref, work Prepared) Result {
	return compare(runs, clock, reps, ref, work, nil, 0)
}

// compare is ComparePrepared; first, when set, is an already measured first
// run (ref, then work, reps each). With bound > 0 it stops after FewRuns runs
// when their median ratio is at most half of bound (Bounded).
func compare(runs int, clock Clock, reps int, ref, work Prepared, first *[2]time.Duration, bound float64) Result {
	var ratios []float64
	var refs, works []time.Duration
	for i := 0; i < runs; i++ {
		if bound > 0 && i == FewRuns {
			sorted := slices.Sorted(slices.Values(ratios))
			if sorted[FewRuns/2] <= bound/2 {
				runs = FewRuns
				break
			}
		}
		var r, w time.Duration
		if i == 0 && first != nil {
			r, w = first[0], first[1]
		} else if i%2 == 0 {
			r = TimePrepared(clock, reps, ref)
			w = TimePrepared(clock, reps, work)
		} else {
			w = TimePrepared(clock, reps, work)
			r = TimePrepared(clock, reps, ref)
		}
		r = max(r, time.Microsecond)
		ratios = append(ratios, float64(w)/float64(r))
		refs, works = append(refs, r), append(works, w)
	}
	slices.Sort(ratios)
	slices.Sort(refs)
	slices.Sort(works)
	return Result{Ratio: ratios[runs/2], Ratios: ratios, Ref: refs[runs/2], Work: works[runs/2]}
}

// Bounded is Against for a gate with the given bound: FewRuns runs, extended
// to Runs when their median ratio is within 2× of bound — a wide margin
// needs fewer runs, a tight one keeps the median of Runs.
func Bounded(clock Clock, ref, work Prepared, bound float64) Result {
	return against(Runs, clock, ref, work, bound)
}

// LinearBounded is LinearPrepared with Bounded's run count, for bound.
func LinearBounded(clock Clock, scale int, f func(n int) func(), n int, bound float64) Result {
	small := Prepared(func() func() { return f(n) })
	large := Prepared(func() func() { return f(scale * n) })
	return against(Runs, clock, small, large, bound)
}

// Against compares work to ref, both Prepared, repeated so that the slower
// of the two takes about clock.Target() (the repetitions are fixed from one
// calibration run of each and shared, so the ratio is per call), over Runs
// runs.
func Against(clock Clock, ref, work Prepared) Result {
	return AgainstN(Runs, clock, ref, work)
}

// AgainstN is Against over runs runs (FewRuns for a gate with a wide margin).
// When the calibration shows one repetition is enough, it is the first run.
func AgainstN(runs int, clock Clock, ref, work Prepared) Result {
	return against(runs, clock, ref, work, 0)
}

// against is AgainstN, stopping early for bound > 0 (Bounded).
func against(runs int, clock Clock, ref, work Prepared, bound float64) Result {
	first := [2]time.Duration{TimePrepared(clock, 1, ref), TimePrepared(clock, 1, work)}
	slower := max(first[0], first[1], time.Microsecond)
	reps := max(int(clock.Target()/slower), 1)
	if reps == 1 {
		return compare(runs, clock, 1, ref, work, &first, bound)
	}
	return compare(runs, clock, reps, ref, work, nil, bound)
}

// PerCall returns the median, over runs runs, of p's time per call by clock,
// each run repeated to about clock.Target() (the repetitions fixed from one
// calibration call).
func PerCall(runs int, clock Clock, p Prepared) time.Duration {
	reps := RepsPrepared(clock, clock.Target(), p)
	var per []time.Duration
	for i := 0; i < runs; i++ {
		per = append(per, TimePrepared(clock, reps, p)/time.Duration(reps))
	}
	slices.Sort(per)
	return max(per[runs/2], time.Nanosecond)
}

// AgainstFixed compares work to ref, a per-call time measured beforehand
// (PerCall), over runs runs of work each repeated to about clock.Target():
// for a reference many gates of one test share, measured once per test
// instead of alternated with each gate. Use it on the CPU clock, which host
// load moves little between the reference and the gates.
func AgainstFixed(runs int, clock Clock, ref time.Duration, work Prepared) Result {
	// The calibration call is the first run when one call is enough.
	one := max(TimePrepared(clock, 1, work), time.Microsecond)
	reps := max(int(clock.Target()/one), 1)
	var ratios []float64
	var works []time.Duration
	for i := 0; i < runs; i++ {
		w := one
		if i > 0 || reps > 1 {
			w = TimePrepared(clock, reps, work) / time.Duration(reps)
		}
		ratios = append(ratios, float64(w)/float64(ref))
		works = append(works, w)
	}
	slices.Sort(ratios)
	slices.Sort(works)
	return Result{Ratio: ratios[runs/2], Ratios: ratios, Ref: ref, Work: works[runs/2]}
}

// LinearBound bounds a Linear ratio at scale 4: the geometric midpoint of a
// linear cost (4) and a quadratic one (16), so allocation growth and cache
// effects at the larger size (measured up to ≈ 5.6) stay inside it.
const LinearBound = 8

// Linear compares f at size n to f at size scale·n, repeated so that the
// slower takes about clock.Target() (Against). At scale 4 a linear cost gives
// ≈ 4, a quadratic one ≈ 16.
func Linear(clock Clock, scale int, f func(n int), n int) Result {
	return LinearPrepared(clock, scale, func(n int) func() { return func() { f(n) } }, n)
}

// LinearPrepared is Linear for an operation with an untimed setup: f(n)
// prepares the operation at size n and returns the part to time.
func LinearPrepared(clock Clock, scale int, f func(n int) func(), n int) Result {
	return LinearN(Runs, clock, scale, f, n)
}

// LinearN is LinearPrepared over runs runs (FewRuns for a gate with a wide
// margin).
func LinearN(runs int, clock Clock, scale int, f func(n int) func(), n int) Result {
	small := Prepared(func() func() { return f(n) })
	large := Prepared(func() func() { return f(scale * n) })
	return AgainstN(runs, clock, small, large)
}

// Check fails t when r.Ratio exceeds bound, and logs r either way.
func Check(t testing.TB, what string, r Result, bound float64) {
	t.Helper()
	t.Logf("%s: %v; bound %.2f", what, r, bound)
	if r.Ratio > bound {
		t.Errorf("%s: %.2f× the reference, want ≤ %.2f× (%v)", what, r.Ratio, bound, r)
	}
}

// DeadlineBound bounds an AfterDeadline ratio: an operation that honours a
// deadline of a quarter of the reference returns at ≈ 0.25 of it plus its
// polling interval; one that ignores the deadline runs its whole workload,
// many times the reference.
const DeadlineBound = 0.75

// AfterDeadline measures how soon an operation returns once its deadline
// passes, in wall time (deadlines are wall-clock), relative to ref, the same
// operation stopped after a fixed amount of work of about Target (Wall). The
// deadline is a quarter of ref's median over three runs; work(d) runs the
// operation, with no other limit, under a deadline d from its start. The
// result is the median ratio of work's time to ref's over Runs alternated
// runs.
func AfterDeadline(ref func(), work func(d time.Duration)) Result {
	var refs []time.Duration
	for i := 0; i < 3; i++ {
		refs = append(refs, Time(Wall, ref))
	}
	slices.Sort(refs)
	d := refs[1] / 4
	return Compare(Runs, Wall, ref, func() { work(d) })
}

// Scaled returns the amount of work, a multiple of unit, for which f takes
// about clock.Target(): f(unit) is timed once and the amount scaled from it
// (at least unit).
func Scaled(clock Clock, unit int64, f func(amount int64)) int64 {
	d := max(Time(clock, func() { f(unit) }), time.Microsecond)
	return max(int64(float64(unit)*float64(clock.Target())/float64(d)), unit)
}
