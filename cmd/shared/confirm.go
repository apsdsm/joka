package shared

import "fmt"

func Confirm(prompt string) bool {
	fmt.Print(prompt)
	var response string
	fmt.Scanln(&response)
	return response == "yes"
}
