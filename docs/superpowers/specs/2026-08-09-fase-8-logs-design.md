# Fase 8 — Logs: diseño

**Fecha:** 9 de agosto de 2026
**Estado:** 🔄 spec aprobado, sin implementar

> Sub-spec de la fase 8, que el spec principal dejó explícitamente para su
> propio documento por ser el subsistema más grande. Resultó **bastante más
> chico de lo previsto**, y las mediciones son la razón.
> Spec principal: `2026-08-08-server-status-design.md`.

---

## 1. Lo que cambió el diseño: medir antes

Tres mediciones sobre el VPS real, el 9/8/2026:

| Qué | Medido | Consecuencia |
|---|---|---|
| Volumen total de logs de Docker | **9,9 MB** acumulados | No hace falta ninguna estrategia de compresión ni particionado |
| Ritmo | **~2 líneas por minuto** entre los 21 containers | No hacen falta 21 streams permanentes: alcanza con pedir una vez por minuto |
| `Config.Tty` de todos los containers | **`false`** | El stream de Docker viene **multiplexado** y hay que demultiplexarlo |

El spec principal preveía "7 días o 500 MB, lo que llegue primero" imaginando un
volumen alto. Con 2 líneas por minuto, **30 días entran en unos 20 MB** contra
81 GB libres. Se guardan 30 días.

## 2. Qué se ingiere, y qué queda afuera a propósito

**Se ingiere lo que sale por stdout/stderr de cada container**, leído de la API
de Docker: comm-tool, los Supabase, workshop-app, cloudflared. Es exactamente lo
que se pidió al principio del proyecto — *"ver logs de las aplicaciones que voy
poniendo"*.

**Los access logs de Caddy quedan afuera**, y es una decisión, no un olvido. El
Caddyfile los manda a `output file /data/access-*.log`: son archivos JSON
adentro del volumen del container, propiedad de root, y **no pasan por
`docker logs`**. Traerlos exigiría una segunda tubería —montar el volumen o
cambiar Caddy para que loguee a stdout— y volver a tocar el stack `edge`, que es
por donde entra todo el tráfico público.

Lo que esos logs aportarían —si cada servicio responde y con qué latencia— ya lo
dan los probes de la fase 3. Si algún día hace falta el detalle de tráfico, la
puerta queda abierta y anotada acá.

## 3. Dos mecanismos, cada uno para lo suyo

**Ingesta: por consulta, una vez por minuto.** En el mismo tick que ya existe,
por cada container se pide:

```
GET /containers/{id}/logs?stdout=1&stderr=1&timestamps=1&since=<último>
```

Sin `follow`. Con 2 líneas por minuto, un stream permanente por container serían
21 conexiones vivas y toda la lógica de reconexión que eso arrastra, para
transportar casi nada.

`timestamps=1` hace que Docker prefije cada línea con su hora en RFC3339Nano:
así el `ts` es el del evento y no el de cuando lo leímos.

**Tail en vivo: por stream, solo mientras alguien mira.** El panel abre
`?follow=1` contra un container al entrar a la página de tail, y lo cierra al
salir. Cero conexiones permanentes; la baja latencia solo se paga cuando se usa.

## 4. El formato multiplexado

Con `Tty: false` —que es el caso de los 21 containers— Docker **no** manda texto
plano: manda bloques con 8 bytes de encabezado.

```
byte 0     tipo de stream: 1 = stdout, 2 = stderr
bytes 1-3  cero
bytes 4-7  tamaño del payload, big-endian, uint32
bytes 8..  el payload
```

Sin demultiplexar, cada línea llega con basura binaria adelante. Es el gotcha
más caro de esta fase y el que más test merece.

## 5. Almacenamiento

**Una sola tabla FTS5**, con las columnas de metadatos como `UNINDEXED`:

```sql
CREATE VIRTUAL TABLE logs USING fts5(
  linea,
  container UNINDEXED,
  stream    UNINDEXED,
  ts        UNINDEXED
);

CREATE TABLE log_cursors (
  container TEXT    PRIMARY KEY,
  ultimo_ts INTEGER NOT NULL
) STRICT;
```

FTS5 **está disponible** en `modernc.org/sqlite` — verificado el 9/8/2026,
incluido el match por prefijo (`conex*`).

**Por qué una sola tabla y no FTS5 con contenido externo:** la forma canónica
—una tabla real más un índice FTS5 apuntándole— evita duplicar el texto, pero
cuesta tres triggers de sincronía. A este volumen (~86.000 filas por mes) la
duplicación son unos pocos MB y los escaneos son de milisegundos.

**El escape hatch, anotado:** si el volumen creciera cien veces, esto se queda
corto —las consultas por container y las borradas por fecha escanean— y hay que
migrar a contenido externo con índices reales. La medición que dispara esa
migración: que un `SELECT` del panel tarde más de un segundo.

**El cursor sobrevive a los reinicios.** `log_cursors` guarda el `ts` de la
última línea de cada container: al arrancar, la ingesta pide desde ahí y no
repite ni pierde. Un container que nunca se leyó arranca desde el momento actual
y **no** importa su historia: traer los 30 días de Docker en el primer tick
sería un pico inútil.

## 6. Alertas por patrón

Reusan la máquina de incidentes que ya existe, con sujeto `logs:<container>` y
la política **por umbral** de la fase 3 — no hace falta código nuevo de reglas:

| | Valor | Por qué |
|---|---|---|
| Métrica | coincidencias en los últimos 5 min | La regla 3 del spec principal: los logs se cuentan, no se reenvían |
| Abre | ≥ 10 | Una app en loop de error, no un error suelto |
| Cierra | ≤ 2 | Histéresis: sin ella, un servicio que loguea un error por minuto flapea |
| Patrón por defecto | `(?i)\b(panic|fatal|error)\b` | Configurable por servicio |

El mensaje lleva **el conteo y una línea de muestra**, nunca las diez.

## 7. Qué ve el panel

- `/logs` — filtros por container, texto y rango; resultados paginados.
- `/logs/tail?container=X` — el stream en vivo, por SSE.

La búsqueda usa `MATCH` de FTS5, que soporta prefijos (`conex*`) y operadores
(`error AND timeout`). El texto que escribe el usuario se pasa **entre comillas
dobles** salvo que termine en `*`: sin eso, un paréntesis suelto tirado en el
buscador rompe la consulta con un error de sintaxis de FTS5.

## 8. Fuera de alcance

- Los access logs de Caddy (§2).
- El comando `/logs` de Telegram, que es la fase 9.
- Cualquier forma de agregación o métricas derivadas de los logs.
- Exportar a un sistema externo.

## 9. Invariantes que suma

10. **La ingesta nunca pide sin `since`.** Un pedido sin cursor traería toda la
    historia que Docker conserve y llenaría la base en un tick.
11. **El texto del buscador nunca va crudo a `MATCH`.** Se escapa, o un
    paréntesis suelto rompe la consulta.
