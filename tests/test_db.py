import pytest
from joka.infra.db import (
    create_migrations_table,
    has_migrations_table,
    get_applied_migrations,
    record_migration_applied,
    MigrationAlreadyExistsError,
    NoMigrationTableError,
)


class TestMigrationsTable:

    async def test_create_migrations_table(self, engine, clean_db):
        """Should create the migrations table successfully."""
        await create_migrations_table(engine)

        async with engine.connect() as con:
            assert await has_migrations_table(con) is True

    async def test_create_migrations_table_already_exists(self, engine, clean_db):
        """Should raise error if migrations table already exists."""
        await create_migrations_table(engine)

        with pytest.raises(MigrationAlreadyExistsError):
            await create_migrations_table(engine)

    async def test_get_applied_migrations_empty(self, engine, clean_db):
        """Should return empty list when no migrations applied."""
        await create_migrations_table(engine)

        async with engine.connect() as con:
            migrations = await get_applied_migrations(con)

        assert migrations == []

    async def test_get_applied_migrations_no_table(self, engine, clean_db):
        """Should raise error when migrations table doesn't exist."""
        async with engine.connect() as con:
            with pytest.raises(NoMigrationTableError):
                await get_applied_migrations(con)

    async def test_record_migration_applied(self, engine, clean_db):
        """Should record a migration and retrieve it."""
        await create_migrations_table(engine)

        async with engine.connect() as con:
            await record_migration_applied(con, "250101120000_initial")
            await con.commit()

        async with engine.connect() as con:
            migrations = await get_applied_migrations(con)

        assert len(migrations) == 1
        assert migrations[0].migration_index == "250101120000_initial"
