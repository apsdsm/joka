import pytest
from testcontainers.mysql import MySqlContainer
from sqlalchemy.ext.asyncio import create_async_engine, AsyncEngine


@pytest.fixture(scope="session")
def mysql_container():
    """Start a MySQL container for the test session."""
    with MySqlContainer("mysql:8.0") as mysql:
        yield mysql


@pytest.fixture(scope="session")
def database_url(mysql_container) -> str:
    """Get the async database URL for the MySQL container."""
    # Build asyncmy URL from container connection details
    host = mysql_container.get_container_host_ip()
    port = mysql_container.get_exposed_port(3306)
    user = mysql_container.username
    password = mysql_container.password
    db = mysql_container.dbname
    return f"mysql+asyncmy://{user}:{password}@{host}:{port}/{db}"


@pytest.fixture
async def engine(database_url) -> AsyncEngine:
    """Create a fresh async engine for each test."""
    engine = create_async_engine(database_url, echo=False)
    yield engine
    await engine.dispose()


@pytest.fixture
async def clean_db(engine):
    """Reset the database before each test by dropping and recreating tables."""
    async with engine.connect() as con:
        await con.exec_driver_sql("DROP TABLE IF EXISTS migrations")
        await con.commit()
    yield
