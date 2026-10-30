import unittest

from textsearch.tfidf import TFIDFRanker


class TestTokenizedTFIDF(unittest.TestCase):
    def setUp(self):
        # Simulan tokens ya normalizados por el preprocesamiento Go.
        self.model = TFIDFRanker.from_tokenized_documents([
            ("0", ["gat", "gat", "perr"]),
            ("1", ["gat"]),
            ("2", ["perr"]),
            ("3", []),
        ])

    def test_conserva_tf_y_documentos_vacios(self):
        self.assertEqual(self.model.postings["gat"], {"0": 2, "1": 1})
        self.assertEqual(self.model.total_documents, 4)
        self.assertEqual(self.model.document_norm["3"], 0)

    def test_consulta_normalizada_sin_retokenizar(self):
        hits = self.model.search_tokens(["gat"])
        self.assertEqual([h.doc_id for h in hits], ["1", "0"])
        self.assertAlmostEqual(hits[0].score, 1.0)
        # El término crudo no debe convertirse automáticamente en la raíz.
        self.assertEqual(self.model.search_tokens(["gato"]), [])

    def test_consulta_vacia_y_topk(self):
        self.assertEqual(self.model.search_tokens([]), [])
        self.assertEqual(len(self.model.search_tokens(["gat"], top_k=1)), 1)

    def test_rechaza_terminos_sin_normalizar(self):
        with self.assertRaises(ValueError):
            TFIDFRanker.from_tokenized_documents([("0", ["Gato"])])
        with self.assertRaises(ValueError):
            self.model.search_tokens(["GATO"])
        with self.assertRaises(TypeError):
            self.model.search_tokens("gat")
        with self.assertRaises(ValueError):
            TFIDFRanker.from_tokenized_documents([("0", []), ("0", [])])


if __name__ == "__main__":
    unittest.main()
