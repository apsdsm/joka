package db

import "strings"

// SplitSQLStatements splits a SQL script into individual statements on
// top-level semicolons. It is comment- and literal-aware: a `;` does not
// terminate a statement when it appears inside a line comment, a block
// comment, a quoted string or identifier, or a dollar-quoted string.
//
// Each returned statement is trimmed of surrounding whitespace. Fragments
// that contain only whitespace and/or comments (for example a trailing
// `-- comment` after the final `;`) are not emitted, since they carry no
// SQL for the server to execute.
func SplitSQLStatements(script string) []string {
	var statements []string
	var current strings.Builder

	flush := func() {
		stmt := strings.TrimSpace(current.String())
		current.Reset()
		if containsSQL(stmt) {
			statements = append(statements, stmt)
		}
	}

	for i := 0; i < len(script); {
		c := script[i]

		switch {
		case c == '-' && i+1 < len(script) && script[i+1] == '-':
			// Line comment: consume to end of line (keep it attached).
			j := i + 2
			for j < len(script) && script[j] != '\n' {
				j++
			}
			current.WriteString(script[i:j])
			i = j

		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			// Block comment: consume to closing */ (or end of input).
			j := i + 2
			for j < len(script) {
				if script[j] == '*' && j+1 < len(script) && script[j+1] == '/' {
					j += 2
					break
				}
				j++
			}
			current.WriteString(script[i:j])
			i = j

		case c == '\'':
			// Single-quoted string; '' is an escaped quote.
			j := i + 1
			for j < len(script) {
				if script[j] == '\'' {
					if j+1 < len(script) && script[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			current.WriteString(script[i:j])
			i = j

		case c == '"':
			// Double-quoted identifier; "" is an escaped quote.
			j := i + 1
			for j < len(script) {
				if script[j] == '"' {
					if j+1 < len(script) && script[j+1] == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			current.WriteString(script[i:j])
			i = j

		case c == '$':
			if tag, ok := dollarTag(script, i); ok {
				// Dollar-quoted string: consume through the matching tag.
				end := strings.Index(script[i+len(tag):], tag)
				var j int
				if end < 0 {
					j = len(script)
				} else {
					j = i + len(tag) + end + len(tag)
				}
				current.WriteString(script[i:j])
				i = j
			} else {
				current.WriteByte(c)
				i++
			}

		case c == ';':
			flush()
			i++

		default:
			current.WriteByte(c)
			i++
		}
	}

	flush()
	return statements
}

// dollarTag reports whether a dollar-quote opening tag begins at position i.
// A tag is `$`, optional identifier characters, then a closing `$`
// (for example `$$` or `$body$`). On success it returns the full tag text
// including both `$` delimiters.
func dollarTag(script string, i int) (string, bool) {
	if script[i] != '$' {
		return "", false
	}
	j := i + 1
	for j < len(script) && isTagChar(script[j]) {
		j++
	}
	if j < len(script) && script[j] == '$' {
		return script[i : j+1], true
	}
	return "", false
}

// isTagChar reports whether b is valid inside a dollar-quote tag identifier.
func isTagChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9')
}

// containsSQL reports whether the statement contains any SQL once comments
// and whitespace are stripped. It is used to filter out trailing fragments
// that are only comments.
func containsSQL(stmt string) bool {
	for i := 0; i < len(stmt); {
		c := stmt[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		case c == '-' && i+1 < len(stmt) && stmt[i+1] == '-':
			i += 2
			for i < len(stmt) && stmt[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(stmt) && stmt[i+1] == '*':
			i += 2
			for i < len(stmt) {
				if stmt[i] == '*' && i+1 < len(stmt) && stmt[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
		default:
			return true
		}
	}
	return false
}
