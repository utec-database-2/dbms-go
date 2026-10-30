"""Prueba -> Go PreprocessText -> SPIMI persistido -> export -> TF-IDF Python.
"""
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from textsearch.tfidf import TFIDFRanker


ROOT = Path(__file__).resolve().parents[4]
GO_PACKAGE = "./dbms/cmd/spimi_bridge"


class TestSpimiTFIDFIntegration(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not shutil.which("go"):
            raise unittest.SkipTest("Go no está en PATH: no puede ejecutarse la integración")

    def go_run(self, *args):
        result = subprocess.run(
            ["go", "run", GO_PACKAGE, *args],
            cwd=ROOT, capture_output=True, text=True, encoding="utf-8",
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def test_indice_spimi_real_y_consulta_con_tokens_go(self):
        with tempfile.TemporaryDirectory() as td:
            path = Path(td)
            index = path / "indice"
            input_jsonl = path / "docs.jsonl"
            exported = path / "spimi.json"
            documents = [
                "El gato corre.",
                "El gato gato.",
                "La casa.",
                "el y la",  # solo stopwords: debe mantener el docID 3
            ]
            input_jsonl.write_text(
                "".join(json.dumps({"text": d}, ensure_ascii=False) + "\n" for d in documents),
                encoding="utf-8",
            )
            self.go_run("build", "--input", str(input_jsonl), "--index", str(index))
            self.assertTrue((index / "CURRENT").is_file())

            self.go_run("export", "--index", str(index), "--output", str(exported))
            payload = json.loads(exported.read_text(encoding="utf-8"))
            self.assertEqual(payload["total_documents"], 4)
            self.assertEqual(payload["doc_ids"], ["0", "1", "2", "3"])

            query_tokens = json.loads(self.go_run("preprocess", "--text", "gato"))
            self.assertEqual(query_tokens, ["gat"])
            self.assertEqual(payload["postings"]["gat"], {"0": 1, "1": 2})
            self.assertNotIn("el", payload["postings"])

            ranker = TFIDFRanker.from_spimi_file(exported)
            self.assertEqual(ranker.document_norm["3"], 0.0)
            hits = ranker.search_tokens(query_tokens)
            self.assertEqual([hit.doc_id for hit in hits], ["1", "0"])
            self.assertAlmostEqual(hits[0].score, 1.0)
            self.assertLess(hits[1].score, 1.0)

            # Comparacion numérica entre Go y Python sobre el MISMO SPIMI.
            # Go lee postings directamente; Python usa export solo como oraculo.
            for query in ("gato", "gato casa", "gato gato", "casa"):
                terms = json.loads(self.go_run("preprocess", "--text", query))
                py_hits = ranker.search_tokens(terms)
                go_hits = json.loads(self.go_run(
                    "search", "--index", str(index), "--query", query,
                    "--top-k", "10",
                ))
                self.assertEqual([h["doc_id"] for h in go_hits],
                                 [int(h.doc_id) for h in py_hits])
                for go_hit, py_hit in zip(go_hits, py_hits):
                    self.assertAlmostEqual(go_hit["score"], py_hit.score, places=11)
