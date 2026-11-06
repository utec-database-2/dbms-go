r"""
Ejemplo:
 py -B dbms/cmd/pgtextbench/run_local.py --input-dir "$env:TEMP\bd2_pgprep_12" --qrels dbms/cmd/textbench/testdata/qrels.json --output "$env:TEMP\bd2_postgres_12.csv"
"""
import getpass
import os
from pathlib import Path
import subprocess
import sys


def main():
    root = Path(__file__).resolve().parents[3]
    env = os.environ.copy()
    if not env.get("PGPASSWORD") and not env.get("PGPASSFILE"):
        env["PGPASSWORD"] = getpass.getpass("Contraseña PostgreSQL (postgres): ")
    args = ["go", "run", "./dbms/cmd/pgtextbench", *sys.argv[1:]]
    try:
        result = subprocess.run(args, cwd=root, env=env, check=False)
        return result.returncode
    except FileNotFoundError:
        print("No se encontró Go en PATH", file=sys.stderr)
        return 127


if __name__ == "__main__":
    sys.exit(main())
