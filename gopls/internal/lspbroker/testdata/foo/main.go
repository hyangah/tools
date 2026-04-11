// main.go is a test fixture for the LSP broker integration tests.
//
// textDocument/definition on the call to Greeting at line 11, col 9
// (1-based) should resolve to lib.go line 5 col 6 where func Greeting
// is defined.  Run: gopls lspcli def main.go 11 9
package foo

import "fmt"

func main() {
	msg := Greeting("world")
	fmt.Println(msg)
}
