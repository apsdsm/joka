package shared

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Stdin is the one reader joka takes answers through.
//
// It has to be shared. A bufio.Reader reads ahead, so a second reader over
// os.Stdin finds the bytes the first one already pulled into its buffer. One
// command asking two questions is exactly that case — `entity sync
// --on-conflict=ask` asks which side is right and then asks for confirmation —
// and the symptom is the second prompt silently taking an empty answer and
// cancelling a run the operator just agreed to.
var Stdin = bufio.NewReader(os.Stdin)

// Confirm prints a prompt and reports whether the answer was exactly "yes".
func Confirm(prompt string) bool {
	fmt.Print(prompt)

	line, err := Stdin.ReadString('\n')
	if err != nil && line == "" {
		return false
	}

	return strings.TrimSpace(line) == "yes"
}
