//go:build race

package dos

// raceEnabled is true under the race detector, which slows rendering about
// tenfold and makes these wall-clock gates meaningless; CI runs them in a
// separate step without -race.
const raceEnabled = true
