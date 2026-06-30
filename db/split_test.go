package db

import (
	"reflect"
	"testing"
)

func TestSplitSQLStatements(t *testing.T) {
	// The splitter consumes the terminating `;`, so emitted statements do not
	// include it. Each statement still executes correctly without it.

	t.Run("it should not split on a semicolon inside a line comment", func(t *testing.T) {
		got := SplitSQLStatements("CREATE TABLE t (id int); -- drop; me\nSELECT 1;")
		// The comment after the first `;` attaches to the statement it precedes.
		want := []string{"CREATE TABLE t (id int)", "-- drop; me\nSELECT 1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't split on a semicolon inside a block comment", func(t *testing.T) {
		got := SplitSQLStatements("SELECT 1 /* a ; b */ ;")
		want := []string{"SELECT 1 /* a ; b */"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't split on a semicolon inside a single-quoted string", func(t *testing.T) {
		got := SplitSQLStatements("INSERT INTO t VALUES ('a;b');")
		want := []string{"INSERT INTO t VALUES ('a;b')"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't split on a semicolon inside an escaped single quote", func(t *testing.T) {
		got := SplitSQLStatements("INSERT INTO t VALUES ('it''s; ok');")
		want := []string{"INSERT INTO t VALUES ('it''s; ok')"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't split on a semicolon inside a double-quoted identifier", func(t *testing.T) {
		got := SplitSQLStatements(`SELECT "weird;col" FROM t;`)
		want := []string{`SELECT "weird;col" FROM t`}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't split on a semicolon inside a dollar-quoted string", func(t *testing.T) {
		got := SplitSQLStatements("SELECT $$a;b$$;")
		want := []string{"SELECT $$a;b$$"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it should split a normal multi-statement script", func(t *testing.T) {
		got := SplitSQLStatements("CREATE TABLE a (id int);\nCREATE TABLE b (id int);\nINSERT INTO a VALUES (1);")
		want := []string{
			"CREATE TABLE a (id int)",
			"CREATE TABLE b (id int)",
			"INSERT INTO a VALUES (1)",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it shouldn't emit a statement for a trailing comment after the last semicolon", func(t *testing.T) {
		got := SplitSQLStatements("SELECT 1;\n-- all done\n")
		want := []string{"SELECT 1"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it should keep a CREATE FUNCTION with a dollar-quoted body as one statement", func(t *testing.T) {
		script := `CREATE FUNCTION f() RETURNS int AS $$
BEGIN
  x := 1;
  y := 2;
  RETURN x + y;
END;
$$ LANGUAGE plpgsql;
SELECT f();`
		got := SplitSQLStatements(script)
		want := []string{
			`CREATE FUNCTION f() RETURNS int AS $$
BEGIN
  x := 1;
  y := 2;
  RETURN x + y;
END;
$$ LANGUAGE plpgsql`,
			"SELECT f()",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it should match dollar-quote tags exactly so an inner $$ doesn't close a tagged body", func(t *testing.T) {
		got := SplitSQLStatements("SELECT $body$ a; $$ b; $body$;")
		want := []string{"SELECT $body$ a; $$ b; $body$"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	})

	t.Run("it should return no statements for an empty or comment-only script", func(t *testing.T) {
		if got := SplitSQLStatements("   \n-- just a comment\n/* block */\n"); len(got) != 0 {
			t.Fatalf("expected no statements, got %#v", got)
		}
	})
}
