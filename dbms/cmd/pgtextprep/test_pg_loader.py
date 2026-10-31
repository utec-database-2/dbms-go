import csv
import json
import tempfile
import unittest
from pathlib import Path

from pg_loader import iter_document_rows, lookup_query, read_manifest


class TestPGLoaderCSV(unittest.TestCase):
    def test_docs_and_manifest(self):
        with tempfile.TemporaryDirectory() as td:
            d = Path(td)
            (d / "manifest.json").write_text(json.dumps({"schema_version": "pgtextprep-v1", "documents": 2}), encoding="utf-8")
            with (d / "docs.csv").open("w", encoding="utf-8", newline="") as f:
                w = csv.writer(f); w.writerow(["doc_id", "external_id", "normalized_text"])
                w.writerow([0, "d0", "gat gat"]); w.writerow([1, "d1", "perr"])
            self.assertEqual(read_manifest(d)["documents"], 2)
            self.assertEqual(list(iter_document_rows(d / "docs.csv")), [(0, "d0", "gat gat"), (1, "d1", "perr")])

    def test_rejects_malformed_docs(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / "docs.csv"
            p.write_text("doc_id,external_id,normalized_text\n1,d1,gat\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                list(iter_document_rows(p))

    def test_queries(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / "queries.csv"
            with p.open("w", encoding="utf-8", newline="") as f:
                w = csv.writer(f)
                w.writerow(["query_id", "raw_text", "normalized_tokens", "or_tsquery", "raw_words"])
                w.writerow(["q1", "gato", "gat", "'gat'", 1])
                w.writerow(["q3", "gato perro", "gat perr", "'gat' | 'perr'", 2])
            self.assertEqual(lookup_query(p, "q3")["or_tsquery"], "'gat' | 'perr'")
            with self.assertRaises(ValueError):
                lookup_query(p, "nonexistent")


class TestPGLoaderSQLIdentifiers(unittest.TestCase):
    def test_index_name_is_not_schema_qualified_in_create_index(self):
        import sys
        import types
        from unittest.mock import patch
        from pg_loader import sql_objects

        fake_psycopg = types.ModuleType("psycopg")
        fake_sql = types.SimpleNamespace(Identifier=lambda *parts: parts)
        fake_psycopg.sql = fake_sql
        with patch.dict(sys.modules, {"psycopg": fake_psycopg}):
            _, table, index = sql_objects()
        self.assertEqual(table, ("bd2_textbench", "documents"))
        self.assertEqual(index, ("documents_search_vector_gin_idx",))


if __name__ == "__main__":
    unittest.main()
