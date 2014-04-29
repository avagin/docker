// +build amd64

package libct

import (
	"syscall"
)

func setHostname(hostname string) error {
	return syscall.Sethostname([]byte(hostname))
}
