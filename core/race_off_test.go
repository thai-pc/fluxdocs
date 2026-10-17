//go:build !race

package core

// raceEnabled reports whether the race detector is on (it slows code
// unevenly, so CPU-time ratio checks skip).
const raceEnabled = false
