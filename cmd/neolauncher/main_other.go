//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("NeoLauncher desktop currently supports Windows. Core settings and profile tests run on other platforms.")
}
