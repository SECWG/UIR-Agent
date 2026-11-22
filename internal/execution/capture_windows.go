package execution

import (
	"os"
)

func openCapture(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
