package protocol

// WorkspaceFilesBinaryContent implements the conservative preview policy. It
// does not claim to recognize every binary format; UTF-8 is checked separately.
func WorkspaceFilesBinaryContent(data []byte) bool {
	for _, b := range data {
		if b < 32 && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
}
