"""Pruebas locales sin conexión PostgreSQL; para Go+PostgreSQL usar pg_loader.py."""
import csv
import json
import tempfile
import unittest
from pathlib import Path

from pg_loader import iter_document_rows, lookup_query, read_manifest, native_or_expression


class TestPGLoaderCSV(unittest.TestCase):
    def test_docs_and_manifest(self):
        with tempfile.TemporaryDirectory() as td:
            d = Path(td)
            (d / "manifest.json").write_text(json.dumps({"schema_version": "pgtextprep-native-v2", "documents": 2, "postgresql_text_config":"spanish"}), encoding="utf-8")
            with (d / "docs.csv").open("w", encoding="utf-8", newline="") as f:
                w = csv.writer(f); w.writerow(["doc_id", "external_id", "raw_text"])
                w.writerow([0, "d0", "gat gat"]); w.writerow([1, "d1", "perr"])
            self.assertEqual(read_manifest(d)["documents"], 2)
            self.assertEqual(list(iter_document_rows(d / "docs.csv")), [(0, "d0", "gat gat"), (1, "d1", "perr")])

    def test_rejects_malformed_docs(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / "docs.csv"
            p.write_text("doc_id,external_id,raw_text\n1,d1,gat\n", encoding="utf-8")
            with self.assertRaises(ValueError):
                list(iter_document_rows(p))

    def test_queries(self):
        with tempfile.TemporaryDirectory() as td:
            p = Path(td) / "queries.csv"
            with p.open("w", encoding="utf-8", newline="") as f:
                w = csv.writer(f)
                w.writerow(["query_id", "raw_text", "raw_words"])
                w.writerow(["q1", "gato", 1])
                w.writerow(["q3", "gato perro", 2])
            self.assertEqual(lookup_query(p, "q3")["raw_text"], "gato perro")
            self.assertEqual(native_or_expression("gato  perro"), "gato OR perro")
            self.assertEqual(native_or_expression("mæsəˈtʃusɪts"), "mæsəˈtʃusɪts")
            with self.assertRaises(ValueError):
                lookup_query(p, "nonexistent")


if __name__ == "__main__":
    unittest.main()
