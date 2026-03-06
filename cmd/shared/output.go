package shared

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	OutputText = "text"
	OutputJSON = "json"
)

// PrintJSON marshals v as indented JSON and writes it to stdout.
func PrintJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// PrintErrorJSON prints a JSON error object and returns the original error.
func PrintErrorJSON(err error) error {
	PrintJSON(map[string]string{
		"status": "error",
		"error":  err.Error(),
	})
	return err
}

// ValidateOutputFlag checks that the output flag is a supported value.
func ValidateOutputFlag(output string) error {
	if output != OutputText && output != OutputJSON {
		return fmt.Errorf("unsupported output format %q (use \"text\" or \"json\")", output)
	}
	return nil
}
