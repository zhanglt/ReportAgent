"""Neo4j connections shared by schema queries and graph construction."""

import py2neo

from NL2SQL.config import settings


def _connection_uri(uri):
    scheme, separator, remainder = (uri or "").partition(":")
    if scheme in {"neo4j", "neo4j+s", "neo4j+ssc"}:
        return "bolt" + scheme[len("neo4j"):] + separator + remainder
    return uri


def create_schema_graph():
    # py2neo 2021.2 reads this cached environment URI into ConnectionProfile,
    # which cannot parse routing schemes. Keep routing on the explicit service
    # URI, and use its matching Bolt scheme only for the driver's defaults.
    py2neo.NEO4J_URI = _connection_uri(py2neo.NEO4J_URI)
    return py2neo.Graph(
        settings.NEO4J_URI,
        auth=(settings.NEO4J_USER, settings.NEO4J_PASSWORD),
        name=settings.NEO4J_DATABASE,
    )
