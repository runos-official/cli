//go:build !darwin && !linux && !windows

package securestore

// No OS store on other systems: Open refuses unless the encrypted file is
// explicitly allowed.
func platformStore(string) (Store, bool) { return nil, false }
