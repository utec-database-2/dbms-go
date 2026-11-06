// Utilidades de SQL del lado del cliente. Nada de esto ejecuta consultas: solo
// prepara el texto (dividir, formatear, colorear) y lee lo que respondió el
// servidor.

export const SQL_KEYWORDS = new Set(
  (
    "SELECT FROM WHERE INSERT INTO VALUES CREATE TABLE DROP DELETE UPDATE SET " +
    "PRIMARY KEY INDEX UNIQUE USING AND OR NOT IN IS NULL AS ON JOIN INNER LEFT " +
    "CROSS ORDER GROUP BY ASC DESC LIMIT HAVING DISTINCT BEGIN END TRANSACTION " +
    "COMMIT ROLLBACK SAVEPOINT RELEASE TRUNCATE INT INTEGER BIGINT LONG VARCHAR " +
    "CHAR TEXT STRING BOOL BOOLEAN BLOB BYTES VARBINARY POINT POLYGON TRUE FALSE"
  ).split(" "),
);

export const SQL_FUNCTIONS = [
  "COUNT",
  "SUM",
  "AVG",
  "MIN",
  "MAX",
  "distancia",
  "dentro",
  "POINT",
  "POLYGON",
];

const TOKEN_RE =
  /(--[^\n]*)|('(?:[^']|'')*'?)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_][A-Za-z0-9_]*)|(\s+)|([^\sA-Za-z0-9_']+)/g;

// Divide el texto en tokens: { type, text } con type en
// com | str | num | word | space | punct.
export function tokenize(sql) {
  const out = [];
  TOKEN_RE.lastIndex = 0;
  let match;
  while ((match = TOKEN_RE.exec(sql)) !== null) {
    const [text, com, str, num, word, space] = match;
    const type = com
      ? "com"
      : str
        ? "str"
        : num
          ? "num"
          : word
            ? "word"
            : space
              ? "space"
              : "punct";
    out.push({ type, text });
  }
  return out;
}

// Clase de color de cada token para el editor.
export function tokenClasses(sql) {
  const tokens = tokenize(sql);
  return tokens.map((tok, i) => {
    let cls = null;
    if (tok.type === "com") cls = "tok-com";
    else if (tok.type === "str") cls = "tok-str";
    else if (tok.type === "num") cls = "tok-num";
    else if (tok.type === "punct") cls = "tok-punct";
    else if (tok.type === "word") {
      if (SQL_KEYWORDS.has(tok.text.toUpperCase())) cls = "tok-kw";
      else if (nextNonSpace(tokens, i)?.text.startsWith("(")) cls = "tok-fn";
    }
    return { text: tok.text, cls };
  });
}

function nextNonSpace(tokens, i) {
  for (let j = i + 1; j < tokens.length; j++) {
    if (tokens[j].type !== "space") return tokens[j];
  }
  return null;
}

// Separa un script en sentencias por ";", sin cortar dentro de textos
// ('a;b') ni de comentarios (-- ...). Cada sentencia conserva su ";".
export function splitStatements(sql) {
  const statements = [];
  let current = "";
  let inString = false;
  let inComment = false;

  for (let i = 0; i < sql.length; i++) {
    const ch = sql[i];
    current += ch;

    if (inComment) {
      if (ch === "\n") inComment = false;
    } else if (inString) {
      if (ch === "'") {
        if (sql[i + 1] === "'") {
          current += sql[++i];
        } else {
          inString = false;
        }
      }
    } else if (ch === "'") {
      inString = true;
    } else if (ch === "-" && sql[i + 1] === "-") {
      inComment = true;
    } else if (ch === ";") {
      statements.push(current);
      current = "";
    }
  }
  statements.push(current);

  return statements.map((s) => s.trim()).filter((s) => stripComments(s).trim() !== "");
}

function stripComments(sql) {
  return sql.replace(/--[^\n]*/g, "").replace(/;/g, "");
}

const NEWLINE_BEFORE = new Set([
  "FROM",
  "WHERE",
  "HAVING",
  "LIMIT",
  "VALUES",
  "SET",
]);

// Formatea el SQL: palabras clave en mayúsculas, una cláusula por línea,
// columnas de CREATE TABLE y tuplas de INSERT una por línea. No toca el
// contenido de los textos ni los nombres.
export function formatSql(sql) {
  return splitStatements(sql)
    .map(formatStatement)
    .join("\n\n");
}

function formatStatement(statement) {
  const tokens = tokenize(statement).filter((t) => t.type !== "space");
  const upper = (t) => (t.type === "word" ? t.text.toUpperCase() : t.text);
  const isCreate =
    tokens.length > 1 && upper(tokens[0]) === "CREATE" && upper(tokens[1]) === "TABLE";

  let out = "";
  let depth = 0;
  let inValues = false;
  let inWhere = false;
  let prev = null;
  // El próximo número va pegado (signo negativo: "-12.05").
  let glueNext = false;

  const newline = (indent = "") => {
    out = out.replace(/[ \t]+$/, "");
    if (out !== "") out += "\n" + indent;
  };
  const space = () => {
    if (out !== "" && !/[\s(]$/.test(out)) out += " ";
  };

  for (let i = 0; i < tokens.length; i++) {
    const tok = tokens[i];

    if (tok.type === "com") {
      space();
      out += tok.text;
      newline();
      prev = tok;
      continue;
    }

    if (tok.type === "punct") {
      for (let j = 0; j < tok.text.length; j++) {
        const ch = tok.text[j];
        if ("<>!=".includes(ch)) {
          // Operadores de comparación de uno o dos caracteres: ">=", "<>"...
          let op = ch;
          while (j + 1 < tok.text.length && "<>!=".includes(tok.text[j + 1])) {
            op += tok.text[++j];
          }
          space();
          out += op;
          prev = { type: "op", text: op };
          continue;
        }
        if (
          ch === "-" &&
          j === tok.text.length - 1 &&
          tokens[i + 1]?.type === "num" &&
          (!prev || prev.type === "op" || "(,".includes(prev.text) || SQL_KEYWORDS.has(upper(prev)))
        ) {
          space();
          out += "-";
          glueNext = true;
          prev = { type: "op", text: "-" };
          continue;
        }
        if (ch === "(") {
          // "POINT(", "COUNT(" van pegados; "VALUES (" con espacio.
          if (isCreate && depth === 0) {
            space();
          } else if (prev && prev.type === "word" && !SQL_KEYWORDS.has(upper(prev))) {
            out = out.replace(/ $/, "");
          } else if (prev && prev.type === "word" && ["POINT", "POLYGON", "VARCHAR", "CHAR"].includes(upper(prev))) {
            out = out.replace(/ $/, "");
          } else {
            space();
          }
          out += "(";
          depth++;
          if (isCreate && depth === 1) newline("  ");
        } else if (ch === ")") {
          depth = Math.max(0, depth - 1);
          if (isCreate && depth === 0) newline();
          out = out.replace(/ $/, "");
          out += ")";
        } else if (ch === ",") {
          out = out.replace(/ $/, "");
          out += ",";
          if (isCreate && depth === 1) newline("  ");
          else if (inValues && depth === 0) newline("  ");
          else out += " ";
        } else if (ch === ";") {
          out = out.replace(/\s+$/, "");
          out += ";";
        } else if (ch === ".") {
          out = out.replace(/ $/, "");
          out += ".";
          prev = { type: "dot", text: "." };
          continue;
        } else {
          space();
          out += ch;
        }
        prev = { type: "punct", text: ch };
      }
      continue;
    }

    const word = upper(tok);
    const isKeyword = tok.type === "word" && SQL_KEYWORDS.has(word);
    const text = isKeyword ? word : tok.text;

    if (isKeyword && depth === 0) {
      const next = tokens[i + 1] && upper(tokens[i + 1]);
      if (NEWLINE_BEFORE.has(word)) {
        newline();
        inValues = word === "VALUES";
        inWhere = word === "WHERE" || word === "HAVING";
      } else if ((word === "GROUP" || word === "ORDER") && next === "BY") {
        newline();
        inWhere = false;
      } else if (word === "JOIN" && !["INNER", "LEFT", "CROSS"].includes(prev && upper(prev))) {
        newline();
      } else if (["INNER", "LEFT", "CROSS"].includes(word) && next === "JOIN") {
        newline();
      } else if (inWhere && (word === "AND" || word === "OR")) {
        newline("  ");
      }
    }

    if (prev?.type === "dot" || glueNext) {
      out += text;
      glueNext = false;
    } else {
      space();
      out += text;
    }
    if (inValues && depth === 0 && word === "VALUES") newline("  ");
    prev = tok;
  }

  return out.trim();
}

// Cómo accedió el motor a las tablas, según los operadores del plan.
export function accessKind(result) {
  const kinds = new Set();
  const visit = (node) => {
    if (!node) return;
    kinds.add(node.Kind);
    (node.Children || []).forEach(visit);
  };
  visit(result?.Tree);
  for (const step of result?.Plan || []) {
    const m = /^\[([a-z-]+)\]/.exec(step);
    if (m) kinds.add(m[1]);
  }
  if (kinds.has("index-seek") || kinds.has("index-scan")) return "index";
  if (kinds.has("seq-scan")) return "scan";
  return "none";
}

export const ACCESS_LABEL = {
  index: "Con índice",
  scan: "Recorrido secuencial",
  none: "Sin lectura de tabla",
};

// Estado de la transacción a partir del mensaje del motor
// (dbms/lib/sql/transactions.go). Devuelve undefined si el mensaje no la toca.
export function transactionFromMessage(message) {
  if (!message) return undefined;
  const begun = /transacción (\d+) iniciada/.exec(message);
  if (begun) return { active: true, id: Number(begun[1]) };
  if (/transacción \d+ (confirmada|deshecha:)/.test(message)) return { active: false };
  return undefined;
}

// "POINT(-12.05 -77.03)" → { lat, lon }
export function parsePoint(value) {
  const m = /^\s*POINT\s*\(\s*(-?\d+(?:\.\d+)?)[\s,]+(-?\d+(?:\.\d+)?)\s*\)\s*$/i.exec(
    String(value),
  );
  return m ? { lat: Number(m[1]), lon: Number(m[2]) } : null;
}
