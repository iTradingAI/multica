package plugincontract

import "strings"

// FileExtension uses only the final ASCII suffix. Plain dotfiles, extensionless
// names and trailing dots have no viewer match.
func FileExtension(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	dot := strings.LastIndex(name, ".")
	if dot <= 0 || dot == len(name)-1 {
		return ""
	}
	extension := name[dot+1:]
	for _, b := range []byte(extension) {
		if b < 'a' || b > 'z' {
			if b < 'A' || b > 'Z' {
				if b < '0' || b > '9' {
					return ""
				}
			}
		}
	}
	return strings.ToLower(extension)
}

func (s Surface) MatchesFile(path, platform string) bool {
	if s.Type != SurfaceFileViewer || platform != "web" && platform != "desktop" {
		return false
	}
	if len(s.Platforms) > 0 {
		match := false
		for _, value := range s.Platforms {
			match = match || value == platform
		}
		if !match {
			return false
		}
	}
	extension := FileExtension(path)
	for _, candidate := range s.Extensions {
		if extension != "" && extension == candidate {
			return true
		}
	}
	return false
}
