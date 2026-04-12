package main

import "fmt"

// Greeting returns a greeting for the given name.
func Greeting(name string) string {
	return "Hello, " + name
}

func main() {
	fmt.Println(Greeting("world"))
}
