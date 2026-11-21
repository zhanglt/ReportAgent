"""Offline checks for business database configuration and schema isolation."""

import unittest
from unittest.mock import Mock, patch

from sqlalchemy import create_engine, text

from NL2SQL.config import database, settings
from NL2SQL.rag.loader import parse_schema_md_by_table
from NL2SQL.schema_engine.m_schema import MSchema
from NL2SQL.schema_engine.schema_engine import SchemaEngine


class DatabaseTests(unittest.TestCase):
    def test_mysql_driver_charset_and_password(self):
        engine = database.create_business_engine(
            "mysql://reader:p%40ss%23word@localhost/businessdb"
        )
        self.addCleanup(engine.dispose)
        self.assertEqual(engine.dialect.driver, "pymysql")
        self.assertEqual(engine.url.password, "p@ss#word")
        self.assertEqual(engine.url.query["charset"], "utf8mb4")
        self.assertTrue(engine.pool._pre_ping)

    def test_explicit_charset_is_preserved(self):
        engine = database.create_business_engine(
            "mysql+pymysql://reader:password@localhost/businessdb?charset=latin1"
        )
        self.addCleanup(engine.dispose)
        self.assertEqual(engine.url.query["charset"], "latin1")

    def test_sql_server_uri_is_preserved(self):
        uri = "mssql+pyodbc://reader:password@localhost/data?driver=ODBC+Driver+18+for+SQL+Server"
        with patch.object(database, "create_engine") as factory:
            database.create_business_engine(uri)
        self.assertEqual(factory.call_args.args[0].render_as_string(hide_password=False), uri)

    def test_schema_resolution(self):
        mysql = "mysql+pymysql://localhost/businessdb"
        self.assertEqual(database.resolve_schema(mysql, ""), "businessdb")
        self.assertEqual(database.resolve_schema(mysql, " custom "), "custom")
        self.assertEqual(database.resolve_schema("mssql+pyodbc://localhost/db", "dbo"), "dbo")
        self.assertIsNone(database.resolve_schema("sqlite://", ""))

    def test_qualification_with_and_without_schema(self):
        with patch.object(settings, "DB_SCHEMA", "businessdb"):
            self.assertEqual(settings.qualify_table_name("ST_PPTN_R"), "businessdb.ST_PPTN_R")
            self.assertEqual(settings.qualify_column_name("ST_PPTN_R.STCD"), "businessdb.ST_PPTN_R.STCD")
            docs = parse_schema_md_by_table("### 表名：ST_PPTN_R\nRainfall")
            self.assertEqual(docs[0]["metadata"]["table"], "businessdb.ST_PPTN_R")
        with patch.object(settings, "DB_SCHEMA", None):
            self.assertEqual(settings.qualify_table_name("ST_PPTN_R"), "ST_PPTN_R")
            self.assertEqual(settings.qualify_column_name("ST_PPTN_R.STCD"), "ST_PPTN_R.STCD")

    def test_mysql_schema_is_limited_to_uri_database(self):
        engine = database.create_business_engine("mysql://localhost/businessdb")
        self.addCleanup(engine.dispose)
        inspector = Mock()
        inspector.has_table.return_value = True

        def init_sql_database(instance, *args):
            self.assertEqual(args[1], "businessdb")
            instance._inspector = inspector
            instance._usable_tables = ["ST_PPTN_R"]

        with patch("llama_index.core.SQLDatabase.__init__", new=init_sql_database):
            schema = SchemaEngine(engine, include_tables=["ST_PPTN_R"], mschema=MSchema())
        self.assertEqual(schema._tables_schemas, {"ST_PPTN_R": "businessdb"})
        inspector.get_schema_names.assert_not_called()

    def test_schema_extraction_respects_selected_tables(self):
        engine = create_engine("sqlite://")
        self.addCleanup(engine.dispose)
        with engine.begin() as conn:
            conn.execute(text("CREATE TABLE rainfall (station TEXT PRIMARY KEY, amount REAL)"))
            conn.execute(text("CREATE TABLE excluded (id INTEGER)"))
            conn.execute(text("INSERT INTO rainfall VALUES ('station_1', 12.5)"))
        schema = SchemaEngine(engine, schema="main", include_tables=["rainfall"]).mschema
        self.assertEqual(set(schema.tables), {"rainfall"})
        self.assertEqual(set(schema.tables["rainfall"]["fields"]), {"station", "amount"})

    def test_dialect_instructions(self):
        mysql = database.sql_dialect_instructions("mysql")
        self.assertIn("MySQL 8.0", mysql)
        self.assertIn("LIMIT", mysql)
        self.assertIn("SQL Server", database.sql_dialect_instructions("mssql"))


if __name__ == "__main__":
    unittest.main()
