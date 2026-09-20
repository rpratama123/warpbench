//go:build windows

package tui

import "golang.org/x/sys/windows"

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleOutputCP = kernel32.NewProc("GetConsoleOutputCP")
)

// cpUTF8 is CP_UTF8, the one console code page in which the characters in
// UnicodeGlyphs survive the round trip from Go to the screen.
const cpUTF8 = 65001

// unicodeEnabled reports whether the console can render the block and arrow
// characters.
//
// Go writes console output by converting UTF-8 to UTF-16 and calling
// WriteConsoleW (see internal/poll/fd_windows.go). The Windows console then
// re-encodes that UTF-16 into its output code page, using best-fit mapping for
// characters the code page does not contain. In a legacy code page neither
// U+2588 nor U+2591 maps to anything, and Windows picks the same substitute for
// both, which is what turns a chart into a uniform run of '¦'. Only a UTF-8
// console is safe, and that is what this asks about.
//
// The code page is read rather than set. Changing it would fix this process and
// then leave the user's console altered after exit, which is not this tool's
// decision to make; the ASCII set is a complete answer on its own. A user who
// wants the block charts can run `chcp 65001` first.
func unicodeEnabled() bool {
	cp, _, _ := procGetConsoleOutputCP.Call()
	if cp == 0 {
		// No console is attached, so output is going to a pipe or a file and
		// Go writes raw UTF-8, which no code page can mangle.
		return true
	}
	return cp == cpUTF8
}
