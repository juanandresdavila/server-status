# Egress IPv6 vs IPv4: construir el contrafactual

**Fecha:** 26/08/2026 · **Estado:** pre-registrado, sin datos todavía.

Este documento se escribe **antes** de correr la medición y no se edita después:
la regla de decisión tiene que estar fijada antes de ver los números. Los
resultados van en un documento aparte.

## El problema

El 26/08/2026 los probes salientes acumularon **23 resets** contra una base de
**0,31 por día**. Todas las fallas de red del histórico son por IPv6 — pero eso
**no prueba** que IPv6 sea la causa: el VPS sale siempre por IPv6 (Go prefiere
AAAA y `/etc/gai.conf` no tiene overrides), así que **nunca se intentó por
IPv4**. No hay contrafactual. Esta medición lo construye.

## Línea de base (`probe_results`, histórico completo)

| Día | Probes | Resets | Timeouts | Otras |
|---|---|---|---|---|
| 09/08 | 4.850 | 0 | 0 | 25 (ensayo del alarmero, no es baseline) |
| 10–16/08 | 5.760/día | 0 | 0 | 0 |
| 17/08 | 5.760 | 1 | 0 | 0 |
| 18/08 | 5.760 | 0 | 0 | 1 (HTTP 502) |
| 19–21/08 | 5.760/día | 0 | 0 | 0 |
| 22/08 | 5.748 | 1 | 0 | 0 |
| 23/08 | 5.760 | 0 | 0 | 0 |
| 24/08 | 5.760 | 1 | 0 | 0 |
| 25/08 | 5.760 | 2 | 0 | 0 |
| **26/08** (a las 20:04 UTC) | 5.243 | **23** | 6 | 0 |

**0,31 resets/día** entre el 10 y el 25/08 (5 en 16 días). El 26/08 es ~75× eso.
Los 6 timeouts del 26/08 son el corte de `workshop` de 18:08–18:13 UTC, que es
otro fenómeno y está descrito en la memoria del proyecto.

## Lo que ya se descartó antes de medir

- **No es un cambio local.** Los resets arrancan 07:15 UTC; cloudflared se
  recreó a las 11:56 y el binario nuevo se deployó a las 16:16. Los primeros
  seis pasaron con el cloudflared viejo y el binario viejo.
- **No es un solo servidor de borde.** En las tandas de 08:43 y 19:27 fallaron
  varios servicios **en el mismo segundo contra IPs de borde distintas**.
- **No es solo Cloudflare.** El watchdog también timeouteó dos veces ese día
  contra `hc-ping.com`, que resuelve a Hetzner (`2a01:4f8::/32`) y contesta
  `server: nginx`, sin `cf-ray`.

## El hallazgo que reencuadra la hipótesis

Medido desde el VPS el 26/08/2026:

| | v6 | v4 |
|---|---|---|
| RTT ICMP al borde de Cloudflare | **7,90 ms** | **7,88 ms** |

Las dos familias tienen el mismo RTT, así que la latencia no confunde la
comparación. Y el costo de un pedido, medido en el mismo punto donde lo corta Go
—cuando llegan los headers, no cuando termina el cuerpo—:

| | tiempo hasta los headers |
|---|---|
| conexión **nueva** (connect + TLS + pedido) | **~142 ms** |
| conexión **reusada** (h2 ya establecida) | **~51 ms** |

Los 51 ms de una conexión reusada no son el RTT: son el *hairpin*. El pedido
sale a Cloudflare (8 ms), vuelve por el túnel al mismo VPS, pasa por Caddy y la
app, y desanda el camino.

Con eso se leen los datos de producción del 26/08:

| | latencia |
|---|---|
| probes **exitosos** (n = 5.294) | mín. 25 ms, media 45–50 ms |
| los 23 **resets** | 8–60 ms, veinte de ellos ≤ 19 ms |

**Producción reusa conexiones.** La media de los éxitos (45–50 ms) es la de un
pedido reusado (51 ms), no la de uno nuevo (142 ms); solo un 5–10 % pasa de
90 ms, que son las veces que le tocó abrir conexión.

**Y los 23 resets, todos, están por debajo de los 142 ms de una conexión
nueva**: ninguno hizo handshake. Más todavía, los que salen en 8 ms están a
**exactamente un RTT** — el RST vuelve un viaje de ida y vuelta después de
mandar el pedido, sobre una conexión que llevaba 60 s ociosa. Y ningún probe
exitoso baja de 25 ms: **los resets son más rápidos que cualquier éxito**,
porque el RST corta antes de que el pedido llegue a la aplicación.

> Corrección: la primera medición de este documento daba el costo de una
> conexión nueva en ~47 ms (TLS listo) y ~100–140 ms (pedido completo con
> `curl`). Ninguno de los dos era el punto de comparación correcto: Go devuelve
> de `Do()` cuando llegan los headers y sin leer el cuerpo. El número bueno es
> 142 ms, y con él la conclusión pasa de "20 de 23" a **23 de 23**.

Cuadra con el código: `internal/prober` usa `http.Client` con el `Transport` por
defecto —keep-alive con `IdleConnTimeout` de 90 s— y los probes salen cada 60 s.
Cada conexión queda ociosa exactamente 60 s y se reusa en el tick siguiente.

## Las dos hipótesis

- **H1 — familia de direcciones.** El egress IPv6 del VPS, o el tramo
  OVH→Cloudflare por v6, se corta.
- **H2 — estado de conexión ociosa.** Un dispositivo con estado en el camino
  pierde el estado del flujo tras ~60 s de silencio y contesta RST al primer
  paquete siguiente. Explica la firma completa: RST sin handshake, varios
  servicios a la vez —todos despiertan en el mismo tick del minuto—, y contra
  IPs de destino distintas, porque el estado que se pierde es del lado del
  origen.

H2 tiene una objeción conocida y anotada acá para no olvidarla: el `net.Dialer`
por defecto de Go pone **TCP keepalive a 30 s**, y los keepalives suelen
refrescar el estado de un firewall. Para que H2 se sostenga hace falta un
dispositivo que los ignore o que expire por debajo de 30 s. El brazo de 30 s
existe justamente para eso.

**Por qué importa para el diseño:** un pinger que abra conexión nueva cada vez
—`curl`, por ejemplo— **no reproduce el fenómeno en ninguna de las dos
familias**. Daría "v4 limpio, v6 limpio" y eso se leería como "ya no pasa".

## Diseño: factorial 2×2 + un brazo de cadencia

Cinco brazos sobre los **mismos destinos**, mismo timeout de 10 s:

| brazo | familia | reusa conexión | cadencia |
|---|---|---|---|
| `v6-ka` | IPv6 | sí | 60 s |
| `v4-ka` | IPv4 | sí | 60 s |
| `v6-fresh` | IPv6 | no | 60 s |
| `v4-fresh` | IPv4 | no | 60 s |
| `v6-ka-30s` | IPv6 | sí | 30 s |

`v6-ka` es la réplica de producción. Los otros cuatro mueven **una sola
variable** cada uno respecto de él: la familia, el reuso, o el tiempo de ocio.

### Instrumento

Un binario Go aparte (`cmd/egress-probe`), no `curl`: la fidelidad es el punto.
`curl` es otra pila de red y otra política de pooling; si el fenómeno vive en el
reuso de conexiones de `net/http`, `curl` no lo ve. El pinger usa el mismo
`net/http` con `ForceAttemptHTTP2` para igualar al `DefaultTransport` de
producción, y cambia solo:

- **familia**: `DialContext` propio que diala `tcp4` o `tcp6` ignorando la red
  que le pasa el transporte;
- **reuso**: el brazo "fresh" llama `CloseIdleConnections()` justo antes de cada
  pedido. **No** se usa `DisableKeepAlives`, que en Go además apaga HTTP/2 y
  movería dos variables a la vez.

Cada brazo tiene su **propio cliente**: compartirlo mezclaría los pools y
contaminaría el experimento.

Cada intento registra, vía `httptrace`: instante, destino, brazo, familia,
`Reused`, `WasIdle`, **`IdleTime`**, protocolo negociado, IP remota, tiempos de
DNS/connect/TLS/total, status y error crudo. `IdleTime` es la que decide H2: si
los que fallan tienen ~60 s de ocio y los que andan no, está contestado.

### Destinos

Los cuatro que pasan por Cloudflare y vuelven por el túnel al mismo VPS
(`jadd.com.ar`, `comm.jadd.com.ar/health`, `workshop.jadd.com.ar`,
`supabase-gym.jadd.com.ar/auth/v1/health` — este último sin `apikey`, contesta
401 y no obliga a tocar secretos: para transporte el status da igual), más uno
**fuera de Cloudflare** para separar "tramo a Cloudflare" de "egress del VPS en
general": `www.google.com/generate_204`, dual-stack y hecho para esto.

5 destinos × 5 brazos ≈ 25 pedidos/min, contra los 5/min de producción.

### Qué no toca

Binario aparte, unit aparte, salida a JSONL propio en `/var/lib/egress-probe/`.
Ni la base ni la unit de `server-status` se tocan.

### Control positivo

Producción sigue corriendo y sigue anotando en `probe_results`. **Si en la
ventana de medición producción registra resets y el brazo `v6-ka` no, el
instrumento no es fiel y no hay conclusión sobre la red.** Se chequea antes de
leer cualquier otra cosa.

### Regla de parada

Por eventos, no por tiempo: con 23/día el brazo `v6-ka` junta ~70 fallas en
72 h; con la base de 0,31/día junta 1 y no alcanza. Se corre hasta **≥20 fallas
de transporte en `v6-ka`**, o **7 días**, lo que pase primero, con **mínimo
72 h**.

## Regla de decisión — pre-registrada

Con la misma cantidad de intentos por brazo:

| Resultado | Lectura | Qué se hace |
|---|---|---|
| `v6-ka` y `v6-fresh` fallan, los `v4-*` en cero | **H1 confirmada**: es la familia | forzar `tcp4` en el `DialContext` del prober |
| `v6-ka` y `v4-ka` fallan parecido, los `*-fresh` ~cero | **H1 refutada, H2 confirmada**: es el reuso de conexiones ociosas | apagar keep-alive en el prober, o bajar `IdleConnTimeout` por debajo del intervalo |
| falla **solo `v6-ka`** | las dos cosas: se pierde estado de flujo y solo por v6 | `tcp4` **y** revisar el reuso |
| fallan los cinco brazos | ni la familia ni el reuso salvan: es el uplink del VPS | no es cambio de código: reintento/tolerancia, y reclamo a OVH |
| v6 falla y v4 tampoco está limpio, sin separación clara | **no concluye** | comparar los instantes: fallas de v4 y v6 en los mismos minutos → uplink compartido; independientes → familia |
| cero fallas en los cinco brazos **y producción tampoco falló** | el fenómeno cesó | sin conclusión; decidir si se espera la próxima tanda |
| cero fallas en los cinco brazos **pero producción sí falló** | **el instrumento no es fiel** | sin conclusión sobre la red: arreglar el pinger |
| un brazo falla en **≥50 %** de sus intentos | esa familia **no tiene salida**, o el destino no existe | arreglar eso antes de leer el 2×2 |

`v6-ka-30s` no entra en la tabla principal: es el desempate de H2. Si `v6-ka`
falla y `v6-ka-30s` no, el tiempo de expiración del estado está entre 30 y 60 s.

La última fila se agregó tras el smoke test local, antes de que existiera un
solo dato del VPS: corriendo desde una Mac sin IPv6, los cuatro intentos por v6
fallaban con `no route to host` y la regla dictaminaba "es la familia". Es
verdadero y más chico que su uso — la familia no tenía salida, que no es lo
mismo que tener blips. El guardia corta a partir de 10 intentos, y como el
fenómeno investigado anda por el 0,4 % no puede tapar el resultado real.

**Umbral de separación:** la diferencia entre dos brazos cuenta como real con
**≥10 eventos en el brazo alto** y **Fisher exacto de dos colas p < 0,01**.
Abajo de eso es empate y va a la fila de "no concluye". El test está
implementado en `internal/egress` y se aplica solo: la conclusión no se saca a
ojo.

## El cambio de código no va antes de la medición

Queda dicho, porque es la trampa: **si sale H2, el arreglo no es `tcp4`, es el
keep-alive** — y forzar v4 habría "funcionado" igual, por accidente, al
reiniciar el pool de conexiones, dejando la causa intacta.

## Higiene: el repo es público

Ninguna IP del VPS entra en ningún archivo. El error crudo que devuelve Go
—`read tcp <origen>-><destino>: …`— **contiene la IP de origen**, así que el
pinger la tacha antes de escribirla (`egress.Redactar`), y registra del lado
local solo el puerto. En documentación se escribe `<ipv6-vps>` / `<ip-tailnet>`.
