package main

import (
	"bufio"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// readSecret takes one password from in.
//
// On a terminal the echo is turned off, so the password does not end up on the
// screen or in a scrollback buffer. Piped input is read as a plain line, which
// keeps the account commands usable from a script.
func readSecret(in *os.File) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		typed, err := term.ReadPassword(int(in.Fd()))
		return string(typed), err
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
