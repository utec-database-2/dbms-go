import gzip
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("miracl_prep", HERE / "miracl_prep.py")
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class TestMiraclPrep(unittest.TestCase):
    def test_anchored_relevance_and_fixed_performance_categories(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            raw = root / "raw"
            raw.mkdir()
            with gzip.open(raw / "docs-0.jsonl.gz", "wt", encoding="utf-8") as f:
                for i in range(1100):
                    f.write(json.dumps({"docid": f"{i}#0", "title": "Historia peruana", "text": "¡Ciencia! música; tecnología."}, ensure_ascii=False) + "\n")
            (raw / "topics.miracl-v1.0-es-dev.tsv").write_text(
                "q1\thistoria\nq3\thistoria del Peru\nq5\thistoria de la ciencia moderna actual\n", encoding="utf-8")
            (raw / "qrels.miracl-v1.0-es-dev.tsv").write_text(
                "q1 Q0 1050#0 1\nq1 Q0 1090#0 0\nq3 Q0 100#0 1\nq5 Q0 1080#0 1\n", encoding="utf-8")
            sources = {key: raw / key for key in mod.SOURCES}
            out = root / "prepared"
            m = mod.build_outputs(sources, out, 1000, 3, 2026, 12000)
            lines = [json.loads(x) for x in (out / "documents.jsonl").read_text(encoding="utf-8").splitlines()]
            self.assertEqual(len(lines), 1000)
            self.assertEqual(len({d["id"] for d in lines}), 1000)
            ids = [d["id"] for d in lines]
            self.assertIn("1050#0", ids[:1000])
            self.assertIn("1090#0", ids[:1000])
            self.assertTrue(all("!" not in d["text"] for d in lines))
            rel = json.loads((out / "qrels_relevance.json").read_text(encoding="utf-8"))
            self.assertEqual(rel["q1"], ["1050#0"])
            self.assertEqual(m["official_query_buckets"], {"1": 1, "3": 1, "5+": 1})
            perf = json.loads((out / "queries_performance.json").read_text(encoding="utf-8"))
            buckets = {"1": 0, "3": 0, "5+": 0}
            for q in perf:
                n = len(q["text"].split())
                buckets["1" if n == 1 else "3" if n == 3 else "5+"] += 1
            self.assertEqual(buckets, {"1": 5, "3": 5, "5+": 5})
            self.assertEqual(mod.main(["--raw-dir", str(raw), "--output-dir", str(out)]), 1)

    def test_official_qrels_do_not_become_labels_for_truncated_queries(self):
        self.assertEqual(len(mod.PERFORMANCE_QUERIES), 3)
        for label, terms in mod.PERFORMANCE_QUERIES.items():
            for text in terms:
                words = len(text.split())
                self.assertTrue(words == 1 if label == "1" else words == 3 if label == "3" else words >= 5)

    def test_sanitization_keeps_letters_and_numbers(self):
        self.assertEqual(mod.sanitize_text("¡Camión! A-B 2026\nÁrbol", 100), "Camión A B 2026 Árbol")


if __name__ == "__main__":
    unittest.main()
