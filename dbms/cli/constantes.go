package main

// Parámetros de las estructuras que se comparan. Se eligen los mismos valores
// que usa el motor por defecto, para que lo medido sea el sistema que corre, no
// una configuración de laboratorio.
const (
	// Orden (fanout) de los árboles B+.
	bplusOrder = 50
	// Capacidad de una cubeta del hash extensible.
	hashBucketSize = 32
	// Máximo de entradas por nodo del R-Tree. Gergoe R-tree puro: no Fornasieri,
	// porque la implementación de parte 2 es R*.
	rtreeOrder = 16
)
