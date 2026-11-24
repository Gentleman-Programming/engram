//go:build !linux

package hostbind

func ProcessDomain() (string, error) {
	return "", ErrProcessDomainUnavailable
}

func ProcessTerminated(pid int, start, domain string) (bool, error) {
	return false, ErrProcessDomainUnavailable
}
