import pytest
from sqlalchemy import text

from joka.infra.db import (
    table_exists,
    truncate_table,
    insert_rows,
    TableNotFoundError,
)


@pytest.fixture
async def test_table(engine, clean_db):
    """Create a test table for sync operations."""
    async with engine.connect() as con:
        await con.execute(text("""
            CREATE TABLE test_users (
                id INT PRIMARY KEY,
                name VARCHAR(255),
                email VARCHAR(255)
            )
        """))
        await con.commit()
    yield "test_users"
    # Cleanup
    async with engine.connect() as con:
        await con.execute(text("DROP TABLE IF EXISTS test_users"))
        await con.commit()


class TestTableExists:

    async def test_table_exists_true(self, engine, test_table):
        """Should return True for existing table."""
        async with engine.connect() as con:
            assert await table_exists(con, "test_users") is True

    async def test_table_exists_false(self, engine, clean_db):
        """Should return False for non-existing table."""
        async with engine.connect() as con:
            assert await table_exists(con, "nonexistent") is False


class TestTruncateTable:

    async def test_truncate_table(self, engine, test_table):
        """Should remove all rows from table."""
        async with engine.connect() as con:
            # Insert some data
            await con.execute(text(
                "INSERT INTO test_users (id, name, email) VALUES (1, 'Test', 'test@test.com')"
            ))
            await con.commit()

        async with engine.connect() as con:
            await truncate_table(con, "test_users")
            await con.commit()

        async with engine.connect() as con:
            result = await con.execute(text("SELECT COUNT(*) FROM test_users"))
            count = result.scalar()
            assert count == 0

    async def test_truncate_table_not_found(self, engine, clean_db):
        """Should raise error for non-existing table."""
        async with engine.connect() as con:
            with pytest.raises(TableNotFoundError):
                await truncate_table(con, "nonexistent")


class TestInsertRows:

    async def test_insert_rows(self, engine, test_table):
        """Should insert multiple rows."""
        rows = [
            {"id": 1, "name": "Alice", "email": "alice@test.com"},
            {"id": 2, "name": "Bob", "email": "bob@test.com"},
        ]

        async with engine.connect() as con:
            count = await insert_rows(con, "test_users", rows)
            await con.commit()

        assert count == 2

        async with engine.connect() as con:
            result = await con.execute(text("SELECT * FROM test_users ORDER BY id"))
            fetched = result.fetchall()

            assert len(fetched) == 2
            assert fetched[0].name == "Alice"
            assert fetched[1].name == "Bob"

    async def test_insert_rows_empty(self, engine, test_table):
        """Should handle empty rows list."""
        async with engine.connect() as con:
            count = await insert_rows(con, "test_users", [])

        assert count == 0

    async def test_insert_rows_table_not_found(self, engine, clean_db):
        """Should raise error for non-existing table."""
        rows = [{"id": 1, "name": "Test", "email": "test@test.com"}]

        async with engine.connect() as con:
            with pytest.raises(TableNotFoundError):
                await insert_rows(con, "nonexistent", rows)
