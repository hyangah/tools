// Package unicode is a test fixture for position encoding round-trip tests.
//
// It contains non-ASCII identifiers and comments to verify that the
// broker correctly handles UTF-8 byte column positions per ADR-006.
package unicode

// Héllo is a function with a non-ASCII name.
// The identifier starts at column 6 (1-based, UTF-8 byte offset).
func Héllo() string {
	return "héllo"
}

// Grüß returns a German greeting. The ü is 2 UTF-8 bytes, ß is 2 bytes.
// Definition is at line 14, col 6 (1-based UTF-8 byte offset).
func Grüß() string {
	return "Grüße!"
}

// Café has multi-byte characters in its name (é = 2 UTF-8 bytes).
func Café() string {
	msg := Héllo()
	return msg + " from " + Grüß()
}
