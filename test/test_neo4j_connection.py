"""Regression checks for py2neo routing URIs in the environment."""

import unittest
from unittest.mock import patch

import py2neo

from NL2SQL.config import settings
from NL2SQL.knowledge_graph.connection import create_schema_graph
from NL2SQL.knowledge_graph.graph_query import SchemaGraphQuery


class Neo4jConnectionTests(unittest.TestCase):
    def test_routing_env_uris_preserve_service_routing_and_tls(self):
        for suffix, secure, verify in [("", False, True), ("+s", True, True), ("+ssc", True, False)]:
            uri = f"neo4j{suffix}://localhost:7687"
            with self.subTest(suffix=suffix), patch.object(py2neo, "NEO4J_URI", uri), \
                    patch.object(settings, "NEO4J_URI", uri), \
                    patch.object(settings, "NEO4J_DATABASE", "schema_graph"), \
                    patch.object(py2neo, "Graph") as graph:
                # The actual driver reproduces the exception before repair.
                with self.assertRaisesRegex(ValueError, "Unknown protocol"):
                    py2neo.ConnectionProfile()
                create_schema_graph()
                profile = py2neo.ConnectionProfile()
                self.assertEqual(profile.protocol, "bolt")
                self.assertEqual(profile.secure, secure)
                self.assertEqual(profile.verify, verify)
                service = py2neo.ServiceProfile(graph.call_args.args[0])
                self.assertTrue(service.routing)
                self.assertEqual(service.secure, secure)
                self.assertEqual(service.verify, verify)
                self.assertEqual(graph.call_args.kwargs["name"], "schema_graph")

    def test_direct_env_uris_remain_unchanged(self):
        for uri in [None, "bolt://localhost:7687", "https://localhost:7473"]:
            with self.subTest(uri=uri), patch.object(py2neo, "NEO4J_URI", uri), \
                    patch.object(py2neo, "Graph"):
                create_schema_graph()
                self.assertEqual(py2neo.NEO4J_URI, uri)

    def test_query_uses_shared_graph_factory(self):
        with patch("NL2SQL.knowledge_graph.graph_query.create_schema_graph") as factory:
            query = SchemaGraphQuery()
            self.assertIs(query.graph, factory.return_value)


if __name__ == "__main__":
    unittest.main()
