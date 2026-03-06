package shared

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

// captureStdout runs fn while capturing os.Stdout and returns the output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}

	orig := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()

	return buf.String()
}

func TestValidateOutputFlag(t *testing.T) {
	t.Run("it accepts text", func(t *testing.T) {
		if err := ValidateOutputFlag("text"); err != nil {
			t.Errorf("expected no error for 'text', got: %v", err)
		}
	})

	t.Run("it accepts json", func(t *testing.T) {
		if err := ValidateOutputFlag("json"); err != nil {
			t.Errorf("expected no error for 'json', got: %v", err)
		}
	})

	t.Run("it rejects invalid formats", func(t *testing.T) {
		err := ValidateOutputFlag("xml")
		if err == nil {
			t.Fatal("expected error for 'xml', got nil")
		}
		if want := `unsupported output format "xml"`; !contains(err.Error(), want) {
			t.Errorf("expected error to contain %q, got %q", want, err.Error())
		}
	})

	t.Run("it rejects empty string", func(t *testing.T) {
		err := ValidateOutputFlag("")
		if err == nil {
			t.Fatal("expected error for empty string, got nil")
		}
	})
}

func TestPrintJSON(t *testing.T) {
	t.Run("it outputs valid JSON with correct fields", func(t *testing.T) {
		output := captureStdout(t, func() {
			PrintJSON(map[string]string{"status": "ok", "message": "done"})
		})

		var result map[string]string
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
		}

		if result["status"] != "ok" {
			t.Errorf("expected status 'ok', got %q", result["status"])
		}
		if result["message"] != "done" {
			t.Errorf("expected message 'done', got %q", result["message"])
		}
	})
}

func TestPrintErrorJSON(t *testing.T) {
	t.Run("it outputs error status and message as JSON", func(t *testing.T) {
		output := captureStdout(t, func() {
			PrintErrorJSON(errors.New("something went wrong"))
		})

		var result map[string]string
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("output is not valid JSON: %v\noutput: %s", err, output)
		}

		if result["status"] != "error" {
			t.Errorf("expected status 'error', got %q", result["status"])
		}
		if result["error"] != "something went wrong" {
			t.Errorf("expected error 'something went wrong', got %q", result["error"])
		}
	})

	t.Run("it returns the original error", func(t *testing.T) {
		orig := errors.New("original error")

		captureStdout(t, func() {
			returned := PrintErrorJSON(orig)
			if returned != orig {
				t.Errorf("expected original error to be returned, got %v", returned)
			}
		})
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && bytes.Contains([]byte(s), []byte(substr))
}
