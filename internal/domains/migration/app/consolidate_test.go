package app

import (
	"strings"
	"testing"
)

func TestParseFKDependencies(t *testing.T) {
	t.Run("it returns empty deps for tables with no foreign keys", func(t *testing.T) {
		schema := map[string]string{
			"users":    "CREATE TABLE users (id INT PRIMARY KEY, name VARCHAR(255))",
			"settings": "CREATE TABLE settings (id INT PRIMARY KEY, key VARCHAR(255))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(deps))
		}
		if len(deps["users"]) != 0 {
			t.Errorf("expected no deps for users, got %v", deps["users"])
		}
		if len(deps["settings"]) != 0 {
			t.Errorf("expected no deps for settings, got %v", deps["settings"])
		}
	})

	t.Run("it detects a foreign key dependency", func(t *testing.T) {
		schema := map[string]string{
			"users":  "CREATE TABLE users (id INT PRIMARY KEY)",
			"orders": "CREATE TABLE orders (id INT PRIMARY KEY, user_id INT, FOREIGN KEY (user_id) REFERENCES users(id))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["orders"]) != 1 || deps["orders"][0] != "users" {
			t.Errorf("expected orders to depend on [users], got %v", deps["orders"])
		}
		if len(deps["users"]) != 0 {
			t.Errorf("expected no deps for users, got %v", deps["users"])
		}
	})

	t.Run("it detects backtick-quoted references from MySQL", func(t *testing.T) {
		schema := map[string]string{
			"users":  "CREATE TABLE `users` (id INT PRIMARY KEY)",
			"orders": "CREATE TABLE `orders` (id INT, CONSTRAINT `fk_user` FOREIGN KEY (`user_id`) REFERENCES `users` (`id`))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["orders"]) != 1 || deps["orders"][0] != "users" {
			t.Errorf("expected orders to depend on [users], got %v", deps["orders"])
		}
	})

	t.Run("it ignores self-referencing foreign keys", func(t *testing.T) {
		schema := map[string]string{
			"categories": "CREATE TABLE categories (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES categories(id))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["categories"]) != 0 {
			t.Errorf("expected no deps for self-referencing table, got %v", deps["categories"])
		}
	})

	t.Run("it ignores references to tables outside the schema", func(t *testing.T) {
		schema := map[string]string{
			"orders": "CREATE TABLE orders (id INT, ext_id INT, FOREIGN KEY (ext_id) REFERENCES external_table(id))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["orders"]) != 0 {
			t.Errorf("expected no deps for reference to missing table, got %v", deps["orders"])
		}
	})

	t.Run("it deduplicates multiple references to the same table", func(t *testing.T) {
		schema := map[string]string{
			"users":  "CREATE TABLE users (id INT PRIMARY KEY)",
			"orders": "CREATE TABLE orders (id INT, creator_id INT REFERENCES users(id), approver_id INT REFERENCES users(id))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["orders"]) != 1 {
			t.Errorf("expected 1 unique dep, got %v", deps["orders"])
		}
	})

	t.Run("it handles multiple distinct foreign key targets", func(t *testing.T) {
		schema := map[string]string{
			"users":       "CREATE TABLE users (id INT PRIMARY KEY)",
			"products":    "CREATE TABLE products (id INT PRIMARY KEY)",
			"order_items": "CREATE TABLE order_items (id INT, user_id INT REFERENCES users(id), product_id INT REFERENCES products(id))",
		}

		deps := ParseFKDependencies(schema)

		if len(deps["order_items"]) != 2 {
			t.Fatalf("expected 2 deps, got %v", deps["order_items"])
		}
		// Sorted alphabetically
		if deps["order_items"][0] != "products" || deps["order_items"][1] != "users" {
			t.Errorf("expected [products, users], got %v", deps["order_items"])
		}
	})
}

func TestTopologicalSort(t *testing.T) {
	t.Run("it returns tables in any order when there are no dependencies", func(t *testing.T) {
		deps := map[string][]string{
			"users":    nil,
			"settings": nil,
		}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 tables, got %d", len(result))
		}
		// Alphabetical when no deps constrain order
		if result[0] != "settings" || result[1] != "users" {
			t.Errorf("expected [settings, users], got %v", result)
		}
	})

	t.Run("it places dependencies before dependents", func(t *testing.T) {
		deps := map[string][]string{
			"users":  nil,
			"orders": {"users"},
		}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result[0] != "users" || result[1] != "orders" {
			t.Errorf("expected [users, orders], got %v", result)
		}
	})

	t.Run("it handles a multi-level dependency chain", func(t *testing.T) {
		deps := map[string][]string{
			"users":       nil,
			"orders":      {"users"},
			"order_items": {"orders"},
		}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("expected 3 tables, got %d", len(result))
		}

		indexOf := func(name string) int {
			for i, n := range result {
				if n == name {
					return i
				}
			}
			return -1
		}

		if indexOf("users") > indexOf("orders") {
			t.Error("users must come before orders")
		}
		if indexOf("orders") > indexOf("order_items") {
			t.Error("orders must come before order_items")
		}
	})

	t.Run("it handles a diamond dependency pattern", func(t *testing.T) {
		// D depends on B and C; B and C both depend on A
		deps := map[string][]string{
			"a": nil,
			"b": {"a"},
			"c": {"a"},
			"d": {"b", "c"},
		}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		indexOf := func(name string) int {
			for i, n := range result {
				if n == name {
					return i
				}
			}
			return -1
		}

		if indexOf("a") > indexOf("b") || indexOf("a") > indexOf("c") {
			t.Error("a must come before b and c")
		}
		if indexOf("b") > indexOf("d") || indexOf("c") > indexOf("d") {
			t.Error("b and c must come before d")
		}
	})

	t.Run("it returns an error for circular dependencies", func(t *testing.T) {
		deps := map[string][]string{
			"a": {"b"},
			"b": {"a"},
		}

		_, err := TopologicalSort(deps)
		if err == nil {
			t.Fatal("expected error for circular dependency")
		}
		if !strings.Contains(err.Error(), "circular") {
			t.Errorf("expected circular dependency error, got: %v", err)
		}
	})

	t.Run("it handles a single table with no dependencies", func(t *testing.T) {
		deps := map[string][]string{
			"users": nil,
		}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 || result[0] != "users" {
			t.Errorf("expected [users], got %v", result)
		}
	})

	t.Run("it handles an empty dependency map", func(t *testing.T) {
		deps := map[string][]string{}

		result, err := TopologicalSort(deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Errorf("expected empty result, got %v", result)
		}
	})
}

func TestGenerateConsolidatedSQL(t *testing.T) {
	t.Run("it produces CREATE TABLE statements in the given order", func(t *testing.T) {
		schema := map[string]string{
			"users":  "CREATE TABLE users (\n  id INT PRIMARY KEY\n)",
			"orders": "CREATE TABLE orders (\n  id INT PRIMARY KEY,\n  user_id INT REFERENCES users(id)\n)",
		}

		sql := GenerateConsolidatedSQL(schema, []string{"users", "orders"})

		usersIdx := strings.Index(sql, "CREATE TABLE users")
		ordersIdx := strings.Index(sql, "CREATE TABLE orders")

		if usersIdx < 0 || ordersIdx < 0 {
			t.Fatalf("expected both CREATE TABLE statements in output:\n%s", sql)
		}
		if usersIdx > ordersIdx {
			t.Error("expected users before orders in output")
		}
	})

	t.Run("it adds semicolons to statements that lack them", func(t *testing.T) {
		schema := map[string]string{
			"users": "CREATE TABLE users (id INT PRIMARY KEY)",
		}

		sql := GenerateConsolidatedSQL(schema, []string{"users"})

		if !strings.Contains(sql, "PRIMARY KEY);") {
			t.Errorf("expected semicolon after statement:\n%s", sql)
		}
	})

	t.Run("it does not double semicolons on statements that already have them", func(t *testing.T) {
		schema := map[string]string{
			"users": "CREATE TABLE users (id INT PRIMARY KEY);",
		}

		sql := GenerateConsolidatedSQL(schema, []string{"users"})

		if strings.Contains(sql, ";;") {
			t.Errorf("unexpected double semicolon:\n%s", sql)
		}
	})

	t.Run("it strips MySQL AUTO_INCREMENT counter values", func(t *testing.T) {
		schema := map[string]string{
			"users": "CREATE TABLE `users` (\n  `id` int NOT NULL AUTO_INCREMENT,\n  PRIMARY KEY (`id`)\n) ENGINE=InnoDB AUTO_INCREMENT=42 DEFAULT CHARSET=utf8mb4",
		}

		sql := GenerateConsolidatedSQL(schema, []string{"users"})

		if strings.Contains(sql, "AUTO_INCREMENT=42") {
			t.Errorf("expected AUTO_INCREMENT=42 to be stripped:\n%s", sql)
		}
		// Column-level AUTO_INCREMENT should remain
		if !strings.Contains(sql, "AUTO_INCREMENT") {
			t.Errorf("expected column-level AUTO_INCREMENT to remain:\n%s", sql)
		}
	})

	t.Run("it includes the consolidation header comment", func(t *testing.T) {
		schema := map[string]string{
			"users": "CREATE TABLE users (id INT)",
		}

		sql := GenerateConsolidatedSQL(schema, []string{"users"})

		if !strings.Contains(sql, "-- Consolidated migration") {
			t.Error("expected header comment in output")
		}
	})
}
