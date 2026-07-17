package e2e_test

import "fmt"

func errStatus(code int) error {
	return fmt.Errorf("status %d", code)
}
