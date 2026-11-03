import json
import math
import tempfile
import unittest
from pathlib import Path

from textsearch.tfidf import TFIDFRanker, tokenize
from textsearch.benchmark import precision_recall_at_k


class TestTFIDF(unittest.TestCase):
    def setUp(self):
        self.ranker = TFIDFRanker.from_documents([
            ("1", "gato gato perro"),
            ("2", "gato"),
            ("3", "perro"),
            ("4", ""),
        ])

    def test_tokenizacion_unicode(self):
        self.assertEqual(tokenize("BASE, Datos_2026 ¡Perú!"),
                         ["base", "datos", "2026", "perú"])

    def test_tf_idf(self):
        self.assertAlmostEqual(
            self.ranker.idf("gato"),
            math.log10(4 / 2)
        )
        self.assertAlmostEqual(
            self.ranker.idf("ausente"),
            0.0
        )

    def test_coseno_unitario_documento_de_un_termino(self):
        hits = self.ranker.search("gato")
        self.assertEqual(hits[0].doc_id, "2")
        self.assertAlmostEqual(hits[0].score, 1.0)
        self.assertLess(hits[1].score, 1.0)

    def test_coseno_calculo_manual(self):
        idf_g = self.ranker.idf("gato")
        idf_p = self.ranker.idf("perro")
        
        wg = (1 + math.log10(2)) * idf_g
        expected = wg / math.hypot(wg, idf_p)
        
        actual = next(
            h.score for h in self.ranker.search("gato")
            if h.doc_id == "1"
        )
        self.assertAlmostEqual(actual, expected)

    def test_consultas_sin_resultados(self):
        self.assertEqual(self.ranker.search("inexistente"), [])
        self.assertEqual(self.ranker.search(""), [])
        self.assertEqual(self.ranker.search("gato", 0), [])

    def test_top_k_y_orden_estable(self):
        model = TFIDFRanker.from_documents([("b", "x"), ("a", "x"), ("c", "z")])
        self.assertEqual([h.doc_id for h in model.search("x", 1)], ["a"])

    def test_guardar_cargar(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "idx.json"
            self.ranker.save(path)
            other = TFIDFRanker.load(path)
            self.assertEqual(other.search("gato perro"), self.ranker.search("gato perro"))

    def test_spimi_con_docs_vacios(self):
        ranker = TFIDFRanker({"x": {"1": 2}}, doc_ids=["1", "2", "3"])
        self.assertEqual(ranker.total_documents, 3)
        self.assertEqual(ranker.search("x")[0].doc_id, "1")
        self.assertEqual(ranker.document_norm["2"], 0.0)

    def test_validacion_postings(self):
        with self.assertRaises(ValueError):
            TFIDFRanker({"x": {"1": 0}})
        with self.assertRaises(ValueError):
            TFIDFRanker({"X": {"1": 1}})
        with self.assertRaises(ValueError):
            TFIDFRanker({"x": {"2": 1}}, doc_ids=["1"])

    def test_metricas(self):
        p, r = precision_recall_at_k(["a", "b", "c"], {"a", "c", "d"}, 10)
        self.assertAlmostEqual(p, 0.2)
        self.assertAlmostEqual(r, 2/3)

    def test_corpus_vacio(self):
        self.assertEqual(TFIDFRanker.from_documents([]).search("texto"), [])


if __name__ == "__main__":
    unittest.main()


# py -m unittest discover -s textsearch/tests -v