package main

// plural returns "s" when n != 1, "" otherwise. Used to write
// "1 finding" vs "3 findings" with one expression. The diff/
// subpackage carries its own copy.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
