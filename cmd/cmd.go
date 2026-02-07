package cmd

import "fmt"

// confirm prompts the user with the given text and returns true only if
// the user types "yes" exactly.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	var response string
	fmt.Scanln(&response)
	return response == "yes"
}
