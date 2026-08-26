# Tanda del 26/08/2026 — logs en vivo, reinicios por ventana y accesos remotos

Diez pedidos de uso sobre el panel. Tres resultaron ser preguntas con respuesta
(no había bug), dos son el mismo bug visto por dos lados, y dos son
infraestructura que ya existe en el VPS y no le corresponde a este binario.

## Lo que se verificó antes de escribir código

| Pedido | Hallazgo |
|---|---|
| 8 · "no se cambia de idioma" | **Sí cambia.** `curl "…/?lang=en"` devuelve `Set-Cookie: lang=en`, `<html lang="en">` y los rótulos en inglés. El problema es el control: un `EN` al 50 % de opacidad, 0.8rem, al extremo derecho. No parece un botón. |
| 7 · "carga sin unidades" | Es el *load average* de Unix a 1/5/15 min. Adimensional; la referencia son los 6 vCore. El rótulo está mal, no el número. |
| 6 · "workshop sale como sitio" | **No está.** No hay servicio `workshop` en la config; solo aparecen `workshop-app` y `workshop-db` en la tabla de containers. `https://workshop.jadd.com.ar/` responde 200. |
| 4 y 5 | El mismo bug. La columna REINICIOS es `RestartCount` de Docker, que **solo cuenta reinicios por política**: recrear cloudflared con `compose up -d` lo resetea a 0. Por eso `/events` lo vio (usa `State.StartedAt`) y la tabla no. |
| 9 y 10 | **Cockpit ya corre** en `<ip-tailnet>:9090` con terminal; **xrdp ya corre** en `*:3389` con XFCE instalado. ufw solo deja entrar por `tailscale0`. |

## Decisiones tomadas

- **Tope de logs**: selector 5k / 10k / 25k, default 5k. Se mantiene el cartel de
  truncado — es el que evitó que un export de "24 h" que cubría 4 h 54 m se
  leyera como completo.
- **SSH y RDP**: Apache Guacamole como container en `/opt/stacks`, **con su
  propio usuario y contraseña**. No adentro de `server-status`: el panel no
  tiene autenticación de ningún tipo, y el proceso ya habla con el socket de
  Docker. Una terminal ahí adentro convierte "estás en el tailnet" en "tenés
  root". Es un proyecto aparte de esta tanda.
- **workshop**: se agrega a la config y **sale también en la portada pública**.
  Ojo: la lista blanca de la invariante 4 filtra *campos* (`ServicioPublico`),
  no *servicios* — `main.go` itera todos los probes, así que todo servicio
  configurado se publica solo.

## Lo que se implementa acá (pedidos 1 a 8)

### 1 · Modo en vivo por defecto en `/logs`

Un toggle `● en vivo`, prendido por default, que trae las líneas nuevas y las
pega arriba (la lista es de la más nueva a la más vieja) sin perder el scroll
ni el foco del buscador.

**El cursor es el `rowid`, no el `ts`.** Los logs se ingieren en tandas de a un
minuto y una línea puede entrar con una marca de tiempo anterior a otra que ya
está en pantalla: un cursor por `ts` se saltearía esas líneas para siempre. El
`rowid` de FTS5 es monotónico por inserción, así que `rowid > cursor` no repite
ni pierde nada. Método nuevo `LogsDesdeRowid`, que devuelve las líneas **y** el
rowid máximo; `MaxRowidLogs` siembra el cursor en la carga inicial.

**Se apaga solo con rango explícito.** Con `desde`/`hasta` puestos uno está
mirando el pasado y pegar líneas nuevas arriba sería mentir sobre la ventana.

**El piso real es el tick de un minuto**, que es cada cuánto `ingerirLogs`
vuelca lo nuevo. Por eso el poll es cada 10 s y no cada 1: doce consultas por
minuto para un dato que se mueve una vez.

### 2 · Tope de líneas

`?limite=` con lista blanca {5000, 10000, 25000}, default 5000. Cualquier otro
valor cae al default, igual que `horas`.

⚠️ `ts` es UNINDEXED en la tabla FTS5 `logs`: una búsqueda sin texto es un scan
completo de ~800 000 filas, y subir el tope lo empeora. No se arregla en esta
tanda, pero queda dicho.

### 3 · Incidentes archivados

- Toggle `?archivados=1` en el panel para verlos.
- Resolver y archivar dejan de mandar a `/`: el form lleva un campo `volver`
  con la URL actual, validada como ruta relativa. Sin eso, archivar desde la
  vista de archivados te sacaba de la vista de archivados.

### 4 y 5 · Reinicios de la ventana seleccionada

Columna nueva calculada de `container_samples.started_at` (existe desde la
migración 10), no de `RestartCount`:

```sql
SELECT name, COUNT(DISTINCT started_at) FROM container_samples
WHERE ts >= ? AND ts <= ? AND started_at > 0 GROUP BY name
```

Reinicios = distintos − 1: un container que nunca se reinició tiene un solo
`started_at` en la ventana. `started_at = 0` son las filas anteriores a la
migración 10 y se excluyen — leerlas como un arranque en 1970 haría que todo
container pareciera recién reiniciado.

`RestartCount` **se conserva en su propia columna**: es otra cosa (reinicios por
política de Docker) y está bien que se muestre.

### 6 · Servicio workshop

```yaml
  - nombre: workshop
    probe: https://workshop.jadd.com.ar/
    containers: [workshop-app, workshop-db]
```

### 7 · Rótulo de la carga

`carga 1m/5m/15m` con el número de vCPU al lado, que es contra lo que se compara.

### 8 · Toggle de idioma visible

Segmentado `ES│EN` con borde y el activo resaltado, en vez de una sola letra
gris que no parece un control.

## No se toca

- El esquema: no hace falta migración nueva. Todo sale de tablas que ya existen.
- `internal/rules`: esta tanda es de presentación y de una consulta nueva.
