# Eventos discretos, niveles de log y rango desde–hasta

Plan del 22/08/2026. Nace del reinicio del host de esa madrugada, que no avisó a nadie.

## El diagnóstico que lo motiva

`unattended-upgrades` reinició el VPS el 22/08/2026 a las 05:00:24 UTC (02:00 local).
El corte duró 18 segundos. No salió ningún aviso, y el visor de logs no podía
mostrar el evento.

Tres causas, verificadas contra el servidor:

1. **El motor de reglas solo modela estados sostenidos.** `service:*` y
   `container:*` necesitan 3 muestras consecutivas malas (~3 min); `host:*` y
   `logs:*` son umbrales con histéresis. Un corte de 18 s no entra en ninguna.
   `HostSample.Uptime` se recolecta y se persiste en `uptime_seconds`, pero
   **ninguna regla lo lee**. Resultado medido: cero incidentes en 10 días.
2. **Mientras el host está abajo el proceso está muerto** y no puede observar su
   propia ausencia. El único detector posible es comparar el uptime nuevo contra
   el último persistido.
3. **`RestartCount` es la señal equivocada** para "este container se reinició".
   Verificado en el VPS después del reboot: los 21 containers arrancaron a las
   05:00:38-41 y quedaron en `restarts=0`. Un arranque con el host no lo
   incrementa y una recreación (`compose up -d`) lo resetea. La señal correcta
   es `State.StartedAt`.

Y una cuarta, que es por qué no se pudo diagnosticar desde el panel:

4. **El tope de líneas colapsa la ventana en silencio.** Un export de "24 h" dio
   4 h 54 m: 9408 de 10 000 líneas eran de `supabase-db`, que vuelca el SQL de su
   cron de pomodoro cada minuto. En la base guardada son 582 757 de 802 200 filas
   (73 %). Las 05:00 quedaron fuera del recorte y nada lo indicó.

## Invariantes que este plan respeta

- **9 del spec**: la cola de avisos no es una tabla. Un evento sin su fila
  `evento:<id>` en `notifications` ES un aviso pendiente. Los eventos se derivan
  igual que los incidentes, así que una caída entre registrar y avisar se
  resuelve sola en el tick siguiente.
- **3 del spec**: `delivery_id` determinístico — `evento:<id>`, nunca un uuid.
- **5 del spec**: nadie llama a `time.Now()` fuera de `internal/clock`.
- **Migraciones**: se agregan al final, nunca se edita una aplicada.

## Decisión de esquema: tabla lateral, no reconstruir el FTS5

`logs` es una tabla FTS5 de 802 200 filas en una base de 191 MB. Agregarle una
columna obliga a recrearla y reindexar todo el texto, con el proceso bloqueado en
`store.Open`.

El nivel va en `log_niveles(rowid, nivel)`, atada por el rowid implícito del FTS5.
El backfill es un `INSERT ... SELECT` plano, sin reindexar. Y conceptualmente
corresponde: el nivel es un filtro, no texto buscable, así que no tiene por qué
entrar al índice de búsqueda.

**Costo a pagar:** `BorrarLogsAnterioresA` tiene que podar las dos tablas o deja
huérfanos. Va en la misma transacción.

## Los cuatro pedidos y su orden

| # | Pedido | Entrega |
|---|---|---|
| 1 | Que un reinicio avise | Detector de eventos discretos + aviso agrupado |
| 2 | Pantalla de errores/urgencias | Vista `/eventos` |
| 3 | Elegir desde–hasta en los logs | Rango explícito + aviso de truncado |
| 4 | Verbosidad | Niveles TRACE / INFO / WARN / ERROR |

## Commits

### 1 — `feat(store)`: eventos, niveles y started_at

Migraciones nuevas al final del slice:

```sql
CREATE TABLE eventos (
    id          INTEGER PRIMARY KEY,
    tipo        TEXT    NOT NULL,  -- reboot | container_restart | monitor_start
    sujeto      TEXT    NOT NULL,
    severidad   TEXT    NOT NULL,  -- critical | warning | info
    ocurrido_en INTEGER NOT NULL,
    detalle     TEXT    NOT NULL
) STRICT;

CREATE TABLE log_niveles (
    rowid INTEGER PRIMARY KEY,
    nivel TEXT NOT NULL
) STRICT;

ALTER TABLE container_samples ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;
```

`eventos` es una tabla de hechos puntuales: no tiene `cerrado_en` y por eso no
choca con `incidentes_abierto_unico`. Un reinicio no se "cierra".

### 2 — `feat(logs)`: clasificar cada línea

Paquete nuevo `internal/logs`, función pura `Nivel(container, stream, linea) string`.
Se testea sola, sin base ni Docker.

Reglas, de más específica a más general:

- Línea que arranca con tab o con espacios de continuación → **TRACE**. Es el
  cuerpo de un statement multilínea; es lo que hace el 73 % del volumen.
- Postgres `PANIC:` / `FATAL:` / `ERROR:` → **ERROR**; `WARNING:` → **WARN**;
  `LOG:` / `NOTICE:` / `INFO:` → **INFO**; `DETAIL:` / `STATEMENT:` / `HINT:` /
  `CONTEXT:` / `QUERY:` → **TRACE**.
- Acceso HTTP estilo nginx/Kong: `5xx` → **ERROR**, `4xx` → **WARN**, resto → **TRACE**.
  Un 200 de healthcheck por minuto no es información.
- `level=error` / `"level":"error"` / `[ERROR]` / ` ERROR ` → **ERROR**, y análogos
  para warn/info/debug/trace.
- Sin marca reconocible: **INFO** si viene por stdout, **WARN** si viene por stderr.

El backfill de las 802 200 filas viejas corre una vez, en lotes, después de las
migraciones, usando **este mismo clasificador** — nada de duplicar la lógica en SQL.

### 3 — `feat(reglas)`: detectar reinicios

- **Host**: si el uptime de la muestra nueva es menor que el de la última
  persistida, la máquina se reinició. El tiempo de corte sale de la diferencia
  entre timestamps menos el uptime nuevo.
- **Containers**: `State.StartedAt` del inspect contra el guardado. Si avanzó, ese
  container arrancó de nuevo.
- **Agrupación**: un reboot levanta los 21 containers a la vez. Si en el mismo
  tick hay reinicio de host, los containers se pliegan dentro de ese evento en
  lugar de generar 21. Sin reboot, se agrupan igual en un solo evento con la lista.

### 4 — `feat(avisos)`: mandar los eventos

`EventosPendientes()` derivado igual que `AvisosPendientes()`, con
`delivery_id = "evento:<id>"`. Texto propio, en el mismo tono que los incidentes.

### 5 — `feat(web)`: rango y nivel en los logs

- `desde` y `hasta` como `datetime-local`; si vienen, mandan ellos, y si no, el
  `?horas=` de siempre queda como atajo.
- Selector de nivel mínimo (TRACE muestra todo; INFO es el default y es el que
  hace desaparecer el ruido del cron).
- **Aviso de truncado**: cuando se alcanza el tope, decir la ventana real
  cubierta. Es lo que faltó hoy.

### 6 — `feat(web)`: vista `/eventos`

Línea de tiempo unificada: incidentes (apertura y cierre), eventos discretos y
picos de error en logs, ordenados por hora, con severidad y filtro de rango.

## Verificación

Cada commit con sus tests. Al cerrar: `make test` y `make vet` completos, sin
pipes (`go test ./... | tail` devuelve el exit code de `tail` — ya pasó en este
repo). Y una pasada real contra la base copiada del VPS para medir el backfill.

## Lo que NO entra

- Arreglar el cron de study-master: es otro repo, quedó como tarea aparte junto
  con la rotación del `x-push-secret` que ese SQL loguea en texto plano.
- Indexar `ts` en la búsqueda de logs. Hoy `ts` es UNINDEXED en el FTS5, así que
  una consulta sin texto es un scan completo de 802 200 filas. Es real y se nota,
  pero es otro problema que el de este plan.
