# Un container recreado no avisa si el minuto cae en la recreación: diseño

**Fecha:** 17 de septiembre de 2026
**Estado:** ✅ implementado el 17/09/2026 (plan en `docs/superpowers/plans/2026-09-17-reinicio-durante-la-recreacion.md`), sin deployar

> Bug encontrado en producción. El detector de reinicios de containers
> (`internal/rules/eventos.go`, tanda del 22/08/2026) no ve una recreación
> cuando el tick de ese minuto cae justo mientras Docker borra el container
> viejo. Juan lo notó porque le llegó el aviso de un stack y no el del otro.

---

## 1. Qué pasó, medido

El 17/09/2026 se recrearon dos containers de GoTrue con
`docker compose up -d --no-deps auth`, uno en cada stack de Supabase, con 31
minutos de diferencia. Mismo comando, mismo tipo de container.

| Container | Recreado (UTC) | Evento | Aviso |
|---|---|---|---|
| `supabase-gym-auth` | 15:18:24 | ✅ `eventos.id = 31`, «1 container arrancó de nuevo» | ✅ `notifications.delivery_id = evento:31`, por comm-tool, sin error |
| `supabase-auth` | 14:47:52 | ❌ ninguno | ❌ ninguno |

Las muestras de `supabase-auth` en `container_samples`, leídas de la base de
producción:

| `ts` (UTC) | `state` | `health` | `started_at` |
|---|---|---|---|
| 14:46:00 | running | healthy | 05:00:41 del 22/08 |
| **14:47:00** | running | **(vacío)** | **`0`** |
| 14:48:00 | running | healthy | 14:47:52 |

Y el journal del servicio en ese tick:

```
14:47:52 WARN no se pudo inspeccionar container=supabase-auth err="GET /containers/54017bee…/json: 404 Not Found: No such container"
14:47:52 WARN no se pudieron leer los stats container=supabase-auth err="… 404 Not Found …"
14:47:56 WARN no se pudieron leer los logs container=supabase-auth err="… 404 Not Found …"
```

## 2. La causa

Tres piezas, cada una razonable por separado:

1. **El colector lista y después inspecciona.** El listado del tick de las
   14:47 todavía trajo el container viejo; cuando se pidió el `Inspect` por
   ID, compose ya lo había borrado y Docker contestó 404. En
   `internal/collector/docker/containers.go:198-205`, un `Inspect` fallido
   deja `StartedAt` en su valor cero, y así se persiste: `started_at = 0`.
2. **La base del detector es la foto del minuto anterior.**
   `cmd/server-status/main.go:296` le pasa al detector
   `UltimoEstadoContainers()`, que devuelve las filas del `MAX(ts)`. A las
   14:48 eso fue la fila con el cero.
3. **El detector saltea los ceros** (`internal/rules/eventos.go:82`,
   `case anterior.IsZero() || c.StartedAt.IsZero(): continue`). La guarda
   existe por una razón válida: las filas anteriores a la migración 10 no
   tienen `started_at`, y leerlas como un arranque en 1970 inventaría 21
   reinicios en el primer tick.

🚨 **El problema es que el cero significa dos cosas.** Además de «fila vieja»,
ahora también significa «el inspect falló». Y el inspect falla justo cuando un
container se está recreando, que es uno de los hechos que el detector tiene que
ver. **El evento a detectar es el mismo que borra la base contra la que se
compara.**

La ventana es corta (entre el listado y el inspect de ese tick), así que no pasa
en cada recreación. Pero el 17/09 pasó en una de dos.

## 3. Diseño

### Opción elegida: comparar contra el último arranque CONOCIDO

La base de cada container deja de ser «la foto del minuto anterior» y pasa a ser
**el `started_at` distinto de cero más reciente de ese container**, dentro de una
ventana acotada.

- Query nueva en el store, `UltimoArranqueConocido(ventana time.Duration)`:
  por cada `name`, la muestra más reciente con `started_at > 0` y
  `ts >= MAX(ts) - ventana`.
- La lectura de la base, el insert y la detección van juntos en
  `guardarContainersYDetectar` (`cmd/server-status/reinicios.go`), que el ciclo
  de `main.go` llama. **El panel sigue usando `UltimoEstadoContainers()`**, que
  está bien para mostrar la foto actual.
- La guarda del cero en el detector se queda: sigue cubriendo el `despues` que
  vuelva en cero (el tick que cae justo en la ventana).

**Ventana: 60 minutos, contados desde el último tick guardado.** Decidido por
Juan el 17/09/2026; este spec proponía 10 minutos contados desde el reloj.
- Sin ventana, un container que se borró hace un mes y vuelve con el mismo
  nombre se avisaría como «reiniciado», cuando en realidad es nuevo.
- 60 y no 10: quedarse corto es un aviso perdido, que es el bug; pasarse cuesta
  como mucho un «arrancó de nuevo» para un nombre reusado. Con 10, un stack
  bajado 15 minutos y vuelto a subir no avisaba nada.
- Desde el último tick y no desde el reloj: `UltimoEstadoContainers()` tomaba el
  último tick sin importar su edad, así que después de una caída de
  server-status los reinicios de ese lapso sí avisaban. Contar desde el reloj
  habría perdido eso.

Con esto:

| Secuencia de `started_at` | Hoy | Con el cambio |
|---|---|---|
| conocido → **0** → nuevo | ❌ nada | ✅ evento en el tick del «nuevo» |
| conocido → 0 → 0 → nuevo | ❌ nada | ✅ evento (dentro de 60 min) |
| todo en 0 (filas pre-migración 10) | nada | nada |
| container que aparece por primera vez | nada | nada |
| conocido → nuevo (sin cero en el medio) | ✅ evento | ✅ evento |
| conocido → server-status caído 2 h → nuevo | ✅ evento | ✅ evento (la ventana se cuenta desde el último tick) |
| conocido → ausente 30 min (`down` y `up`) → nuevo | ❌ nada | ✅ evento |
| conocido → ausente más de 60 min → nuevo | nada | nada (es un container nuevo) |

«Ausente» es que el listado no lo trae: con `down` el container no existe. Hoy
no avisa porque la foto del minuto anterior no lo tiene.

### Descartada: arrastrar el `started_at` anterior cuando el inspect falla

Que el colector copie el valor del tick previo en vez de dejar cero. Se
descarta por dos motivos:
- Escribe en `container_samples` un dato que no se midió. Además, el conteo de
  reinicios del panel (`store.go:1111-1124`) cuenta `DISTINCT started_at`, así
  que empezaría a descansar sobre valores inventados.
- Depende de tener el tick anterior en memoria, y eso se pierde si el proceso
  se reinicia entre ticks.

## 4. Verificación

La lección del 22/08 manda acá: **el bug de aquella tanda solo se veía pasando
por SQLite**, porque los tests usaban tiempos sin fracción. Así que:

1. **Test a nivel store, con la base real de SQLite de los tests.** Insertar
   las tres muestras de la tabla del §1, con `StartedAt` en **nanosegundos**
   (`14:47:52.509434637`, como lo devuelve Docker). Después, leer la base con
   la query nueva y correr el detector contra el tick de las 14:48. Tiene que
   devolver el evento.
2. **Mutación:** volver `main.go` a `UltimoEstadoContainers()` (o la query a
   «último tick») tiene que poner ese test en rojo. Si queda verde, el test no
   mide el bug.
3. **Regresiones:** todo en cero, container nuevo, dos ceros seguidos, y un
   container visto por última vez hace más de 60 minutos. Tabla del §3.
4. **Contra datos de producción:** tomar la copia de `status.db` que deja el
   backup del servicio y reproducir los ticks de 14:46 a 14:48 del 17/09 con
   el binario nuevo. A las 14:48 tiene que salir un `container_restart` para
   `supabase-auth`. No hace falta recrear nada en producción para probarlo.
   Es `TestReproduccionContraLaCopiaDeProduccion`, con la copia de
   `server-status backup` del 17/09 en `SERVER_STATUS_COPIA`.
5. **Después del deploy:** recrear un container de nuestros, sin riesgo, y
   confirmar el aviso por Telegram. Una sola recreación prueba el caso normal,
   no el de la ventana: el caso de la ventana lo prueba el punto 4.

## 5. Fuera de alcance

- Distinguir «recreado» (cambió el ID) de «reiniciado» (mismo ID). Hoy
  `container_samples` no guarda el ID, y los dos casos merecen el mismo aviso.
- Avisar que un container **desapareció** (404 sostenido). Es otro detector.

## 6. Archivos

| Archivo | Cambio |
|---|---|
| `internal/store/store.go` | query `UltimoArranqueConocido` |
| `cmd/server-status/reinicios.go` (nuevo) | `ventanaReinicios` y `guardarContainersYDetectar`: base, insert y detección |
| `cmd/server-status/main.go` (~316) | el ciclo llama a `guardarContainersYDetectar` |
| `internal/rules/eventos.go` | sin cambio de lógica, salvo el comentario de la guarda (hoy dice que el cero son solo filas viejas) |
| `internal/store/store_test.go`, `cmd/server-status/reinicios_test.go` | los tests del §4 |
| `CLAUDE.md` | una línea en la sección de eventos: el cero también es «inspect fallido» |
