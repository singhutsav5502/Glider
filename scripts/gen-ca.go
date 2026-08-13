//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/glider-ai/glider/internal/mitm"
)

func main() {
	cert, key := mitm.DefaultCAPaths()
	if len(os.Args) >= 3 {
		cert, key = os.Args[1], os.Args[2]
	}
	a, err := mitm.LoadOrCreateAuthority(cert, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(cert)
	fmt.Println("CA ready:", abs)
	fmt.Println("PEM bytes:", len(a.CertPEM()))
}
