//go:build !unix

package daemon

// terminalCapabilityEnabled stays false on platforms without a pty
// implementation: terminal.open would only ever fail with "unsupported", so
// the daemon must not advertise the terminal-v1 capability.
const terminalCapabilityEnabled = false
