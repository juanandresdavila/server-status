# Reglas de nivel desde una línea — diseño

Poder tocar una línea en `/logs`, decir "esto no es un WARN", y que eso valga
para todas las parecidas: las que ya están guardadas y las que vengan.

## De dónde sale

El 31/08/2026 el visor mostraba 8625 WARN en 24 h, y **8625 de ellos eran el 401
que Kong le devuelve a nuestra propia sonda `egress-probe`**, que pincha el
destino de gym-tracker sin apikey a propósito. WARN de cualquier otra cosa en la
misma ventana: 41. Esas líneas eran además el 55 % de todo lo guardado, así que
el tope de 5000 de la vista se agotaba a las 12 h.

Mirando los 30 días, el patrón es peor. De los WARN de los dos Kong:

| origen | filas |
|---|---|
| sonda `egress-probe` | 43 631 |
| sonda `server-status` (muerta desde el 18/08) | 25 200 |
| **clientes externos** | **227** |

La banda WARN del visor era, casi entera, el monitoreo mirándose a sí mismo.

Se arregló en el clasificador (`nivelDeAcceso`: acceso con código < 500 y UA de
sonda propia va a TRACE), commiteado y deployado el mismo día. **Pero ese arreglo
costó un commit, un cross-compile, 25 minutos de subida y un UPDATE a mano.** El
próximo container ruidoso va a costar lo mismo. Esto es para no volver a pagarlo.

## Qué NO es

- **No es un ahorro de espacio.** Se midió: `logs_content` son 131 MiB de 275 MB
  de base, con 27,4 % de textos distintos, y normalizar a una tabla `mensajes`
  ahorraría ~42 %. Se descartó: el disco del VPS está al 21 % y la migración
  reescribe 925 000 filas con el proceso bloqueado en `store.Open`. Decisión de
  Juan del 31/08/2026, y no hace falta volver a discutirla.
- **No es un filtro de la vista.** Los toggles de nivel y el filtro por container
  ya existen y son por consulta. Esto cambia el nivel *guardado*, que es lo que
  hace que valga para la próxima vez que abrís el panel.
- **No toca los avisos.** Verificado: los niveles de log alimentan `/logs` y,
  solo los ERROR, la línea de tiempo de `/events` (`panel.go`, `BuscarLogs("",
  "", []string{"ERROR"}, …)`). No entran al motor de reglas ni a `notify`. Una
  regla mal puesta puede esconderte algo del visor; **no puede callarte un aviso
  de Telegram.** Ese límite es lo que hace que la función sea tolerable.

## Modelo

Migración 12:

```sql
CREATE TABLE reglas_nivel (
  id        INTEGER PRIMARY KEY,
  patron    TEXT NOT NULL,
  container TEXT NOT NULL,   -- '' = todos
  nivel     TEXT NOT NULL,   -- TRACE | INFO | WARN | ERROR
  motivo    TEXT NOT NULL,
  creada    INTEGER NOT NULL
) STRICT;
```

`motivo` no es decorativo: dentro de tres meses, una regla sin motivo es una
regla que no te vas a animar a borrar.

## Las tres piezas

### 1. Aplicarlas a lo nuevo, sin ensuciar el clasificador

`logs.Clasificar` **sigue siendo una función pura**. Es lo que dice el doc del
paquete y la razón por la que se testea sin base, sin red y sin reloj; meterle
una consulta adentro tiraría eso a la basura. Las reglas son un tipo aparte, en
el mismo paquete y también puro:

```go
type Regla struct{ Patron, Container string; Nivel Nivel }
type Reglas []Regla

// Aplicar devuelve el nivel que corresponde después de las reglas. Si ninguna
// matchea devuelve el que le pasaron.
func (rs Reglas) Aplicar(n Nivel, linea, container string) Nivel
```

El ingestor compone las dos: `reglas.Aplicar(logs.Clasificar(l.Linea, l.Stream),
l.Linea, container)`.

**Las reglas se releen del store al empezar cada tick del minuto.** Sin mutex,
sin `atomic.Pointer`, sin invalidación desde el handler web: la desactualización
máxima es de un minuto y no justifica una primitiva de concurrencia ni un camino
de invalidación que después hay que testear.

### 2. Aplicarlas a lo viejo

Al crear la regla, un UPDATE puntual sobre `log_niveles`. **Está medido en la
base real del VPS: 43 679 filas en 9,4 s sobre las 925 000 de la tabla.** (Son
48 más que las 43 631 de la tabla de arriba porque la sonda siguió corriendo
entre las dos mediciones; no es un desacuerdo.)

Ese número tiene una consecuencia que hay que escribir en el código: el store
abre **una sola** conexión a SQLite, así que esos 9 s le sacan la base al ciclo
del minuto. Es tolerable para una acción manual y explícita; no lo sería para
algo automático.

### 3. 🚨 Una sola definición de "coincide"

Hay tres lugares que tienen que decidir si una línea matchea un patrón: Go al
ingerir, SQL al mostrarte el conteo previo, y SQL al aplicar. **Si divergen, el
número que confirmás no es el que te queda** — que es exactamente la clase de
verificación que mide otra cosa.

Y divergen de arranque: **`LIKE` de SQLite es case-insensitive para ASCII y
`strings.Contains` de Go no lo es.**

- En Go: `strings.Contains(linea, patron)`.
- En SQL: `instr(linea, ?) > 0`, que es case-sensitive y coincide exacto.
  **Nunca `LIKE`.**
- Un test corre las dos sobre el mismo corpus de líneas reales y exige idéntico
  resultado.

Substring y nada más. **Sin regex**: invita a patrones catastróficos y abre entre
Go y SQL un abismo de semántica que no se puede cerrar con un test.

## El adivinador de patrones

La línea entera es única (trae timestamp e IP), así que hay que sacar un patrón
de adentro. `logs.PatronSugerido(linea) string` saca, en este orden:

1. La IP inicial del formato de acceso (`172.19.0.2 - - `).
2. El corchete de fecha (`[31/Aug/2026:19:50:30 +0000]`).
3. Un timestamp ISO-8601 al principio de la línea.

Probado contra las líneas reales del VPS:

| línea | patrón sugerido | acierta |
|---|---|---|
| el 401 de egress-probe | `"GET /auth/v1/health HTTP/1.1" 401 96 "-" "egress-probe"` | 8625 de 8625 |
| «Schema cache loaded…» de PostgREST | `Schema cache loaded 13 Relations, 24 Relationships, …` | 3 |

**Es un punto de partida editable, no una promesa.** El campo queda editable y
el conteo previo es lo que te dice si te pasaste de ancho. Que el adivinador
falle no rompe nada; que el conteo mienta, sí.

## La interfaz

Server-rendered, sin JS nuevo, siguiendo el molde que ya usan resolver y
archivar incidentes.

- **En cada fila de `/logs`**, un control chico que lleva a
  `GET /logs/reglas/nueva?rowid=N`. Esa página prellena patrón, container y
  nivel, y muestra **cuántas líneas guardadas afecta**. Recalcular es
  re-submitear el mismo GET. Confirmar es `POST /logs/reglas`, y vuelve a donde
  estabas con el campo `volver` filtrado por `rutaPropia`.
- **`/logs/reglas`**: las reglas activas, cuándo se crearon, su motivo, y cuántas
  líneas matchean hoy. Una regla que crece en silencio se ve acá.
- **Borrar una regla re-corre `Clasificar` sobre las filas afectadas.** El undo
  es exacto y no aproximado, porque `Clasificar` es determinística. Sin esto, una
  regla es un cambio irreversible sobre datos.
- **En el encabezado de `/logs`, "N reglas activas"**, con link a la lista. Un
  filtro que te olvidaste que pusiste es peor que no tener filtro.

Las dos direcciones: se puede bajar a TRACE para silenciar y subir a ERROR para
marcar algo que el clasificador subestimó. Subir tiene efecto real —los ERROR
aparecen en la línea de tiempo de `/events`— y sigue sin tocar Telegram.

**Conflictos:** las reglas se aplican por orden de `id` y **gana la última que
matchea**. Es arbitrario pero predecible, y va con test.

## Qué queda afuera

Regex, reglas con vencimiento, import/export, scope por `stream`, y aplicar una
regla en background por lotes. Nada de eso hace falta para el caso que motivó
esto.

## Tests

| Qué | Cómo puede fallar |
|---|---|
| `Reglas.Aplicar` | Puro, tabla de casos: sin reglas, una que matchea, una de otro container, dos que se pisan. |
| Go vs SQL | El mismo corpus real por los dos caminos; el test falla si `instr` y `strings.Contains` difieren en una sola línea. |
| Round trip en el store | Crear → aplicar → borrar → el nivel vuelve a ser exactamente `Clasificar(linea, stream)`. |
| Conteo previo == efecto | El número que muestra el preview tiene que ser el `changes()` del UPDATE. Es la afirmación central de toda la función. |
| Migración | `TestUltimaMigracionAplicada` pasa a 12. |

## Riesgo que no se elimina

Una regla demasiado ancha te esconde algo real. Se mitiga con el conteo previo,
con la lista de reglas activas y su match count, con el aviso en el encabezado y
con que nada de esto llega a Telegram. **No se elimina**, y quien agregue una
regla tiene que saberlo.
