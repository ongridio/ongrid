//go:build !linux

package resultanalysis

import "fmt"

func restrictMemory() error { return fmt.Errorf("result analysis requires Linux memory limits") }
