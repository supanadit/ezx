package system

// procExit carries the outcome of a reaped process.
type procExit struct {
	code int
	err  error
}
