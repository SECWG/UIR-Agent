package management

import (
	"golang.org/x/sys/windows"
)

func Hostname() (string, error) {
	var size uint32 = 256
	buffer := make([]uint16, size)
	err := windows.GetComputerName(&buffer[0], &size)
	return windows.UTF16ToString(buffer), err
}
