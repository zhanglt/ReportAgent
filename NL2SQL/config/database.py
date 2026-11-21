"""Shared business-database connection and SQL dialect configuration."""

from sqlalchemy import create_engine
from sqlalchemy.engine import make_url


def create_business_engine(uri=None, **kwargs):
    if uri is None:
        from NL2SQL.config.settings import DB_URI

        uri = DB_URI
    url = make_url(uri)
    if url.drivername == "mysql":
        url = url.set(drivername="mysql+pymysql")
    if url.get_backend_name() == "mysql" and "charset" not in url.query:
        url = url.update_query_dict({"charset": "utf8mb4"})
    kwargs.setdefault("pool_pre_ping", True)
    return create_engine(url, **kwargs)


def resolve_schema(uri, schema):
    schema = (schema or "").strip()
    if schema:
        return schema
    if uri and make_url(uri).get_backend_name() == "mysql":
        return make_url(uri).database or None
    return None


def sql_dialect_instructions(dialect):
    if dialect == "mysql":
        return (
            "Generate SQL compatible with MySQL 8.0. Use LIMIT for row limits, "
            "backticks for quoted identifiers, NOW()/CURDATE() and "
            "DATE_SUB(..., INTERVAL n DAY) for dates. Do not use SQL Server "
            "TOP, square-bracket identifiers, GETDATE(), DATEADD(), or dbo. "
            "Use the exact table and column case shown in the schema."
        )
    if dialect == "mssql":
        return "Generate SQL compatible with SQL Server (T-SQL)."
    return f"Generate SQL compatible with the {dialect} database dialect."
