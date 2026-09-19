//go:build !unix

package termhost

// openPtyDefault is left pointing at the termhost package's ErrUnsupported
// spawner: this platform cannot host pty sessions, the daemon does not
// advertise the terminal capability, and the desktop never offers the entry
// point for it.
