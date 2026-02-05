import pytest
import tempfile
from pathlib import Path

from joka.services.templates import (
    get_tables,
    load_record,
    load_table_data,
    TemplatesDirectoryNotFoundError,
    TemplatesConfigNotFoundError,
    TableDirectoryNotFoundError,
)
from joka.files.models import RecordType, StrategyType


@pytest.fixture
def templates_dir():
    """Create a temporary templates directory with test data."""
    with tempfile.TemporaryDirectory() as tmpdir:
        # Create _config.yaml
        config_content = """
name: test_templates
tables:
  - name: users
    strategy: truncate
  - name: settings
    strategy: update
"""
        Path(tmpdir, "_config.yaml").write_text(config_content)

        # Create users directory with YAML files
        users_dir = Path(tmpdir, "users")
        users_dir.mkdir()
        Path(users_dir, "admin.yaml").write_text("id: 1\nname: Admin\nemail: admin@test.com")
        Path(users_dir, "guest.yaml").write_text("id: 2\nname: Guest\nemail: guest@test.com")

        # Create settings directory with CSV file
        settings_dir = Path(tmpdir, "settings")
        settings_dir.mkdir()
        Path(settings_dir, "defaults.csv").write_text("key,value\ntheme,dark\nlang,en")

        yield tmpdir


class TestGetTables:

    def test_get_tables_success(self, templates_dir):
        """Should load tables from config with correct strategies."""
        tables = get_tables(templates_dir)

        assert len(tables) == 2

        users_table = next(t for t in tables if t.name == "users")
        assert users_table.strategy == StrategyType.truncate
        assert len(users_table.records) == 2

        settings_table = next(t for t in tables if t.name == "settings")
        assert settings_table.strategy == StrategyType.update
        assert len(settings_table.records) == 1

    def test_get_tables_directory_not_found(self):
        """Should raise error when templates directory doesn't exist."""
        with pytest.raises(TemplatesDirectoryNotFoundError):
            get_tables("/nonexistent/path")

    def test_get_tables_config_not_found(self):
        """Should raise error when _config.yaml is missing."""
        with tempfile.TemporaryDirectory() as tmpdir:
            with pytest.raises(TemplatesConfigNotFoundError):
                get_tables(tmpdir)

    def test_get_tables_table_directory_not_found(self):
        """Should raise error when configured table directory is missing."""
        with tempfile.TemporaryDirectory() as tmpdir:
            config_content = """
name: test
tables:
  - name: nonexistent
    strategy: truncate
"""
            Path(tmpdir, "_config.yaml").write_text(config_content)

            with pytest.raises(TableDirectoryNotFoundError):
                get_tables(tmpdir)


class TestLoadRecord:

    def test_load_yaml_record(self, templates_dir):
        """Should load YAML file as single-row list."""
        tables = get_tables(templates_dir)
        users_table = next(t for t in tables if t.name == "users")
        admin_record = next(r for r in users_table.records if r.name == "admin")

        rows = load_record(admin_record)

        assert len(rows) == 1
        assert rows[0]["id"] == 1
        assert rows[0]["name"] == "Admin"
        assert rows[0]["email"] == "admin@test.com"

    def test_load_csv_record(self, templates_dir):
        """Should load CSV file as list of rows."""
        tables = get_tables(templates_dir)
        settings_table = next(t for t in tables if t.name == "settings")
        defaults_record = next(r for r in settings_table.records if r.name == "defaults")

        rows = load_record(defaults_record)

        assert len(rows) == 2
        assert rows[0]["key"] == "theme"
        assert rows[0]["value"] == "dark"
        assert rows[1]["key"] == "lang"
        assert rows[1]["value"] == "en"


class TestLoadTableData:

    def test_load_table_data_combines_records(self, templates_dir):
        """Should combine all records from a table."""
        tables = get_tables(templates_dir)
        users_table = next(t for t in tables if t.name == "users")

        rows = load_table_data(users_table)

        assert len(rows) == 2
        names = [r["name"] for r in rows]
        assert "Admin" in names
        assert "Guest" in names
