// Comando de línea del dbms-go.
//
// Sirve para ejecutar los benchmarks de las comparaciones experimentales y para
// ver los resultados en la terminal:
//
//	dbms bench files   Heap file vs archivo secuencial paginado
//	dbms bench indexes  B+ agrupado vs B+ no agrupado vs hash dinámico
//	dbms bench spatial  Secuencial vs R-Tree vs GiST de PostgreSQL
//	dbms bench all      Las tres comparaciones seguidas
//	dbms concurrencia   Simulación con hilos de transacciones concurrentes
//
// Cada benchmark escribe sus resultados en CSV (para poder volver a graficarlos
// sin repetir las mediciones) y un resumen en la terminal con gráficas de barras.
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		uso()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "bench":
		runBench(os.Args[2:])
	case "concurrencia", "concurrency":
		runConcurrencia(os.Args[2:])
	case "-h", "--help", "help", "ayuda":
		uso()
	default:
		fmt.Fprintf(os.Stderr, "dbms: comando desconocido %q\n\n", os.Args[1])
		uso()
		os.Exit(2)
	}
}

func uso() {
	fmt.Fprint(os.Stderr, `dbms-go: utilidades de línea de comandos

  dbms bench files    [-n 1000,10000,100000] [-queries 200] [-dir /tmp/bench] [-out resultados]
  dbms bench indexes  [-n 1000,10000,100000] [-queries 500] [-dir /tmp/bench] [-out resultados]
  dbms bench spatial  [-n 1000,10000,100000] [-queries 100] [-out resultados] [-pg-socket /tmp/pgsock]
  dbms bench all      [-n ...] [-queries ...] [-dir ...] [-out resultados]
  dbms bench charts   [-out resultados]   Regenera las gráficas desde los CSV
  dbms concurrencia   [-hilos 5] [-depositos 3] [-pausa 15ms]
                      Simulación con hilos: race condition, transacciones,
                      bloqueos 2PL, deadlocks y lecturas sucias

Cada benchmark imprime tablas y barras en la terminal, deja un CSV en -out y
dibuja las gráficas SVG en -out/gráficas.
`)
}

// config son los parámetros comunes a todos los benchmarks.
type config struct {
	sizes   []int
	queries int
	dir     string
	out     string

	pgSocket string
	pgUser   string
	pgDB     string
	noGiST   bool

	seed int64
}

func parseConfig(name string, args []string) (*config, error) {
	cfg := &config{
		dir:      "/tmp/dbms-bench",
		out:      "resultados",
		seed:     20240517,
		pgSocket: "/tmp/opencode/pgsock",
		pgUser:   "bench",
		pgDB:     "postgres",
	}

	fs := flag.NewFlagSet("bench "+name, flag.ContinueOnError)
	sizes := fs.String("n", "1000,10000,100000", "tamaños de dataset, separados por comas")
	fs.IntVar(&cfg.queries, "queries", 0, "consultas por medición (por defecto según el benchmark)")
	fs.StringVar(&cfg.dir, "dir", cfg.dir, "directorio de trabajo de los archivos de datos")
	fs.StringVar(&cfg.out, "out", cfg.out, "directorio de resultados (CSV y gráficas)")
	fs.StringVar(&cfg.pgSocket, "pg-socket", cfg.pgSocket, "socket de PostgreSQL para GiST")
	fs.StringVar(&cfg.pgUser, "pg-user", cfg.pgUser, "usuario de PostgreSQL")
	fs.StringVar(&cfg.pgDB, "pg-db", cfg.pgDB, "base de datos de PostgreSQL")
	fs.BoolVar(&cfg.noGiST, "sin-gist", false, "omite la comparación con GiST")
	fs.Int64Var(&cfg.seed, "seed", cfg.seed, "semilla de los datos, para que las mediciones sean reproducibles")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	for _, s := range strings.Split(*sizes, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("tamaño inválido %q: %w", s, err)
		}
		if n < 1 {
			return nil, fmt.Errorf("el tamaño tiene que ser positivo: %d", n)
		}
		cfg.sizes = append(cfg.sizes, n)
	}
	if len(cfg.sizes) == 0 {
		return nil, fmt.Errorf("hace falta al menos un tamaño con -n")
	}
	return cfg, nil
}

func runBench(args []string) {
	if len(args) == 0 {
		uso()
		os.Exit(2)
	}

	what := args[0]
	rest := args[1:]

	switch what {
	case "files", "archivos":
		cfg, err := parseConfig("files", rest)
		if err != nil {
			fatal(err)
		}
		if err := benchFiles(cfg); err != nil {
			fatal(err)
		}
	case "indexes", "indices":
		cfg, err := parseConfig("indexes", rest)
		if err != nil {
			fatal(err)
		}
		if err := benchIndexes(cfg); err != nil {
			fatal(err)
		}
	case "spatial", "espacial":
		cfg, err := parseConfig("spatial", rest)
		if err != nil {
			fatal(err)
		}
		if err := benchSpatial(cfg); err != nil {
			fatal(err)
		}
	case "all":
		cfg, err := parseConfig("all", rest)
		if err != nil {
			fatal(err)
		}
		for _, step := range []func(*config) error{benchFiles, benchIndexes, benchSpatial} {
			if err := step(cfg); err != nil {
				fatal(err)
			}
		}
	case "charts", "graficas":
		cfg, err := parseConfig("charts", rest)
		if err != nil {
			fatal(err)
		}
		if err := drawAllCharts(cfg.out); err != nil {
			fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "dbms bench: subcomando desconocido %q\n\n", what)
		uso()
		os.Exit(2)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "dbms: %v\n", err)
	os.Exit(1)
}
