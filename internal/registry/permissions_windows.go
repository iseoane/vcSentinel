//go:build windows

package registry

// Windows does not use Unix permission bits for this contract. ACL handling
// remains platform-native rather than pretending chmod modes provide it.
func protectDirectory(string) error { return nil }

func protectFile(string) error { return nil }
