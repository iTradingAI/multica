//go:build unix

package daemon

// terminalCapabilityEnabled reports that this build can host pty sessions
// (termhost has a real spawner on unix), so the daemon advertises
// terminal-v1 in X-Client-Capabilities and the server relay may open
// terminal subscriptions for its runtimes.
const terminalCapabilityEnabled = true
