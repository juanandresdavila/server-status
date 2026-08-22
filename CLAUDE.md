# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

---

## Qué es esto

Monitoreo, avisos por Telegram y página de estado de un VPS chico (OVH VPS-3,
6 vCore / 12 GB / 96 GB, Ubuntu 26.04). Un binario en Go, sin cgo, que corre
como **unit de systemd en el host** — no en un container.

Esa decisión es el centro del diseño y hay que respetarla: leer `/proc` desde
adentro de un container da la vista del container y no la de la máquina, y un
monitor que vive dentro de Docker se muere junto con Docker, justo cuando hacía
falta que avisara. Por eso la unit lleva `After=docker.service` pero **nunca**
`Requires=`.

## Estado

| Fase | Qué trae | Estado |
|---|---|---|
| 0-1 | Scaffold, CI, métricas del host → SQLite | ✅ en el VPS |
| 2-3 | Containers por la API de Docker, probes e incidentes | ✅ en el VPS |
| 4 | Avisos por Telegram | ✅ en el VPS |
| 5-9 | Panel, watchdog, portada pública, logs, comandos | ✅ en el VPS |

Proyecto terminado: fases 0 a 9, todas corriendo. Después de la fase 9 se
sumaron mejoras de uso: export de logs como texto plano (`/logs/export`, mismo
form que la vista), orden por columna en las tablas del panel, memoria y disco alternables entre % y GiB, unidades con dos
decimales, cpu promedio en el `/status` de Telegram e `INSTALLATION.md` con
`deploy/install.sh`.

**Tanda del 22/08/2026 — eventos discretos y niveles de log.** La motivó un
reinicio del host que no avisó a nadie. Ver
`docs/superpowers/plans/2026-08-22-eventos-niveles-y-rango.md`:

- **Eventos discretos** (`internal/rules/eventos.go`): reinicio del host y de
  containers. El motor de reglas solo modela estados *sostenidos*, así que un
  corte de 18 segundos no entraba en ninguna regla.
- **Niveles de log** TRACE/INFO/WARN/ERROR (`internal/logs`), con filtro en el
  visor y en el export.
- **Rango desde–hasta** explícito en `/logs`, y **aviso de truncado**.
- **Vista `/eventos`**: incidentes, reinicios y errores de log en una línea de
  tiempo única.

## Required reading

- **`docs/superpowers/specs/2026-08-08-server-status-design.md`** — spec vigente.
  Leer antes de cualquier decisión arquitectónica. Tiene las invariantes, el
  modelo de datos y el porqué de cada elección.
- Los planes de cada fase están en `docs/superpowers/plans/`.

## Build, test, run

```bash
make test      # go test ./... -race
make vet
make build     # binario local
make linux     # cross-compile a linux/amd64, CGO_ENABLED=0
make deploy    # linux + scp por Tailscale + systemctl restart
make hooks     # activa el hook de pre-push con gitleaks
```

Un test solo:

```bash
go test ./internal/rules/ -run TestLaTerceraFallaAbre -v
```

**No encadenar con pipes al verificar.** `go test ./... | tail` devuelve el exit
code de `tail` y tapa el fallo — ya pasó en este repo y se commiteó un test roto.
Usar `set -e` y comandos sueltos.

**El deploy no necesita Go en el servidor.** Sin cgo, `GOOS=linux GOARCH=amd64
go build` desde la Mac produce el binario exacto que corre en el VPS.

**Para instalar desde cero en otro servidor** está `deploy/install.sh` (guía en
`INSTALLATION.md`): idempotente, compila o usa `dist/`, crea usuario y
directorios, y nunca pisa una config existente. `make deploy` sigue siendo el
camino para ESTE VPS; el script es para terceros o para reinstalar.

## Arquitectura

Un proceso. Todo lo demás son caminos que entran o salen de él.

```
/proc, /sys ──→ collector/host ──┐
docker.sock ──→ collector/docker ─┼─→ store (SQLite) ──→ web (fase 5)
                                  │        ↑                └─ /eventos
URLs públicas ←── prober ─────────┘        │
                        logs/Clasificar ───┤
                                    rules/motor ────→ notify ──→ comm-tool → Telegram
                                    rules/eventos ──┘     └────→ api.telegram.org (respaldo)
```

**`internal/rules` tiene dos mitades y conviene no confundirlas.** `motor.go`
modela estados **sostenidos** —3 fallas seguidas, un umbral aguantado 10
minutos— y produce *incidentes*, que abren y cierran. `eventos.go` modela
hechos **puntuales** —un reinicio— y produce *eventos*, que solo ocurren. Son
tablas distintas porque un evento no se cierra, y meterlo en `incidents`
chocaría contra `incidentes_abierto_unico`.

- **`cmd/server-status`** es el único que lee `process.env` y arma las
  dependencias. Los paquetes de `internal/` las reciben inyectadas — por eso
  todos los tests corren sin red, sin base y sin socket.
- **`internal/rules`** es el corazón: traduce muestras en transiciones. Sus
  políticas (`PorConteo`, `PorUmbral`) son **funciones puras sobre un estado
  explícito**. Es lo único del sistema que puede mandar un mensaje a las 3 de la
  mañana, así que tiene que testearse sin montar medio sistema.
- **`rules.Store` es una interfaz declarada en `rules`**, no un import de
  `store`. Eso corta la dependencia circular y deja el motor testeable con un
  doble de treinta líneas.
- **`internal/model`** existe para que ni el colector importe al store ni al
  revés. No importa nada del proyecto.

### El ciclo

Cada 15 s se muestrea el host en memoria; cada minuto se persiste el agregado
(promedio **y máximo** de CPU — un pico de 15 s desaparece en un promedio), se
leen los containers, se pinchan los servicios y se evalúan las reglas.

### Dos elecciones de librería que no son opinables

- **`modernc.org/sqlite`, no `mattn/go-sqlite3`.** El segundo necesita cgo, y con
  cgo el binario deja de ser estático — se cae la premisa de correr como unit
  desplegada por `scp`. `make linux` y CI lo verifican con `CGO_ENABLED=0`.
- **Cliente de Docker escrito a mano**, no la SDK oficial. Son cuatro endpoints
  más un `DialContext` al socket unix; la SDK arrastra un árbol enorme para eso.

## Invariantes

1. **El proceso nunca atiende un request público.** La portada (fase 7) es un
   archivo estático que escribe el proceso y sirve Caddy. El proceso habla con el
   socket de Docker, y eso equivale a root en el host: no puede además atender a
   internet.
2. **Un incidente abierto por sujeto**, garantizado por el índice único parcial
   `incidentes_abierto_unico`. Sin ese índice, "el incidente de este servicio"
   depende del orden del `SELECT`.
3. **`delivery_id` es determinístico**: `<incidenteID>:opened` o `:closed`. Con un
   uuid nuevo por intento, un reintento manda el aviso dos veces.
4. **La portada se arma por lista blanca**, nunca por lista negra.
5. **Nadie llama a `time.Now()` fuera de `internal/clock`.** Las reglas de 3
   fallas, 2 éxitos, histéresis y ventanas horarias se testean con tiempo simulado.
   (Excepción explícita y comentada: la medición de latencia del prober, que es
   una duración real y no una marca lógica.)
6. **Los probes salen por la URL pública, nunca por localhost.** Pinchar
   `comm.jadd.com.ar` en vez de `127.0.0.1:8787` prueba de paso Cloudflare, el
   túnel y Caddy. Un probe interno diría "todo verde" el día que se rompa el túnel.
7. **Sin cgo.**
8. **Los secretos vienen del entorno.** La base guarda configuración y estado,
   nunca el valor de un secreto.
9. **La cola de avisos no existe como tabla**: se deriva de comparar `incidents`
   contra `notifications`. Un incidente sin su fila `<id>:opened` ES un aviso
   pendiente. Por eso una caída entre abrir el incidente y mandar el mensaje se
   resuelve sola en el tick siguiente.

## Migraciones

Strings en el slice `migraciones` de `internal/store/store.go`, aplicadas en
orden, cada una en su transacción y registradas en `schema_migrations`.

**El nivel de log vive en `log_niveles`, una tabla lateral atada por el `rowid`
del FTS5, y no como columna de `logs`.** Agregarle una columna a una tabla FTS5
obliga a recrearla y reindexar el texto entero con el proceso bloqueado en
`store.Open`; con 802 200 filas y 191 MB eso son minutos de arranque. Medido:
así las migraciones tardan **147 ms** sobre la base real. La contra es que
`BorrarLogsAnterioresA` tiene que podar las dos tablas o `log_niveles` crece
para siempre — va en la misma transacción y tiene test. **Nunca
se edita una ya aplicada**: para cambiar el esquema se agrega otra al final. El
runner se niega a arrancar si la base está más adelante que el binario — eso es
alguien deployando para atrás.

Un solo test afirma el número exacto de migración (`TestUltimaMigracionAplicada`).
Los demás solo verifican que se aplicaron: repetir el número en tres tests obliga
a tocarlos todos cada vez que se agrega una.

## Gotchas que costaron caro

- **Los porcentajes de CPU no se comparan con `!=`.** `1000/10000 × 6 × 100` da
  `60.00000000000001`. Los tests usan tolerancia.
- **Los sockets unix no pasan de ~104 caracteres de ruta.** En los tests del
  cliente de Docker el directorio va en `/tmp` y **no** en `t.TempDir()`, que en
  macOS devuelve algo largo bajo `/var/folders/...`. El error es un "invalid
  argument" en el bind que no dice nada de longitudes.
- **`df -h` redondea para arriba.** Muestra "16G" donde el binario dice 15,0 GiB.
  No es una diferencia: verificado contra `statfs` byte a byte.
- **Los Supabase no dejan pasar nada por Kong sin `apikey`.** El probe la manda
  en el header: en la config, `apikey_env` guarda el NOMBRE de la variable
  (`SUPABASE_SM_ANON_KEY`, `SUPABASE_GYM_ANON_KEY`) y el valor vive en
  `/etc/server-status/env`. Con eso `/auth/v1/health` —el healthcheck real de
  GoTrue— devuelve 200. Hasta el 18/8/2026 se pinchaba `/auth/v1/authorize` con
  `estado_esperado: 400`, el único endpoint que Kong deja pasar pelado: andaba,
  pero ese 400 es un `validation_failed` que GoTrue **loguea como error**, así
  que el monitor dejaba un falso error por minuto —dos, uno por Supabase— en el
  log que uno mira justo cuando algo se rompió de verdad.
- **Los dos stacks de Supabase publican el alias `kong` en la red `edge`.** El
  Caddyfile de `/opt/stacks/edge` decía `reverse_proxy kong:8000` para
  `supabase-sm`, y Docker resolvía ese alias a cualquiera de los dos: todo el
  tráfico público de study-master estaba entrando por el Kong del gym. El check
  daba verde igual, porque `/auth/v1/authorize` contesta lo mismo en los dos.
  Arreglado el 18/8/2026 apuntando al nombre del container (`supabase-kong:8000`).
  Con `apikey` el error se ve enseguida: la anon key de un stack da 401 en el
  otro. **Ojo con `sed -i` sobre un archivo bind-mounteado**: cambia el inode y
  el container sigue viendo el viejo. Hay que reiniciarlo.
- **`/containers/{id}/stats` sin `stream=false` no termina nunca.** Deja la
  conexión abierta mandando una muestra por segundo.
- **La memoria de un container se calcula descontando `inactive_file`**, igual que
  `docker stats` en cgroup v2. Sin eso, todo container que leyó archivos parece
  estarse comiendo la RAM.
- **`/proc/net/dev` no se suma entero.** Quedan afuera `lo`, `docker*`, `br-*` y
  `veth*`: ese tráfico ya está contado del lado de la interfaz real.
- **La memoria usada se calcula contra `MemAvailable`, no contra `MemFree`.** El
  page cache figura como ocupado pero el kernel lo suelta cuando hace falta.
- **`RestartCount` NO dice si un container se reinició.** Solo cuenta los
  reinicios por *política*: un arranque junto con el host lo deja igual y una
  recreación con `compose up -d` lo resetea a cero. Verificado el 22/08/2026
  después del reboot — los 21 containers arrancaron a las 05:00:38 y quedaron
  todos en `restarts=0`. La señal buena es `State.StartedAt`, que se mueve
  siempre. La columna REINICIOS del panel sigue mostrando `RestartCount`, que
  es otra cosa y está bien que la muestre.
- **Una marca de tiempo que se guarda en segundos no se compara con la que vino
  de Docker.** `State.StartedAt` trae nanosegundos —`05:00:38.932553068Z`— y
  `container_samples.started_at` guarda segundos, así que al releerla vuelve
  `05:00:38.000`: SIEMPRE un poco menos que el valor vivo. El detector de
  reinicios lo leyó como "arrancó de nuevo" en cada tick y mandó un aviso por
  minuto para los 21 containers, en producción, el 22/08/2026. La comparación va
  truncada al segundo. **Es la misma trampa que la del cursor de logs**, que está
  tres puntos más abajo y se resolvió al revés (guardando nanosegundos); si
  aparece un campo de tiempo nuevo, elegir una de las dos y dejarlo escrito.
- **El VPS corre en `Etc/UTC`, así que `time.Local` allá es UTC.** El panel usó
  `.Local()` hasta el 22/08/2026 y mostraba UTC mientras uno lo leía como hora
  argentina. La zona sale de `zona` en la config y entra a `web.NuevoPanel`.
  Cuidado al testear esto desde la Mac: ahí `time.Local` **es** Buenos Aires, y
  un test con esa zona pasa igual con el bug puesto. Los tests usan
  `Asia/Tokyo` justamente por eso.
- **`t.In(nil)` paniquea, y un panic adentro de una plantilla deja media página
  escrita con un `200` arriba.** El error de `ExecuteTemplate` hay que mirarlo:
  si no, una plantilla rota se ve como una página cortada y no como un fallo.
- **`ts` es `UNINDEXED` en la tabla FTS5 `logs`.** Una búsqueda sin texto es un
  scan completo. Con 800 000 filas se nota, y no está arreglado.
- **Un solo container ruidoso te tapa la ventana entera.** El cron de pomodoro
  de study-master vuelca 33 líneas de SQL por minuto: 582 757 de las 802 200
  filas guardadas (73 %). Por eso un export de "24 h" cubría 4 h 54 m. Los
  niveles lo mitigan —ese volcado es TRACE— pero la causa está en el otro repo.

## Este repo es público

- **`gitleaks` corre en un hook de `pre-push`, no solo en CI.** Cuando CI falla,
  el commit ya está pusheado y ya es público; hay bots que escanean GitHub en
  segundos. `make hooks` lo activa. Hace falta `brew install gitleaks`.
- **Las IPs del VPS no entran.** Ni la pública ni la de tailnet: la gracia de la
  arquitectura es que el origen sea inalcanzable. En la documentación se escriben
  `<ip-tailnet>`; los valores reales viven en `/etc/server-status/config.yaml`,
  en el servidor.
- **Los commits van con el noreply de GitHub**
  (`69881939+juanandresdavila@users.noreply.github.com`), configurado local al
  repo. `gitleaks` no atrapa ni una casilla ni una IP: no tienen forma de
  credencial. Revisar a mano antes de publicar documentación nueva.

## Avisos: cómo está dado de alta

El camino principal es **comm-tool** y el respaldo es Telegram directo. Los dos
usan el **mismo bot**, `@serverstatusjaddbot`: comm-tool impone un bot por app
con el índice `bots_app_channel_unico`, así que el token existe igual y el
respaldo sale casi gratis.

| Pieza | Dónde |
|---|---|
| App en comm-tool | slug `server-status`, bot slug `status` |
| Destinatario | uuid fijo en `comm_tool_user_id` de la config |
| Variables en comm-tool | `TELEGRAM_TOKEN_STATUS`, `TELEGRAM_WEBHOOK_SECRET_STATUS`, `DELIVERY_SECRET_STATUS` en `/opt/stacks/comm-tool/comm-tool.env` |
| Variables acá | `COMM_TOOL_API_KEY`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` en `/etc/server-status/env` |

**El contacto se insertó a mano en `contacts`, sin pasar por `/vincular`.** Es el
camino que la propia documentación de comm-tool recomienda cuando ya se conoce
el chat id: el saliente solo necesita el token y un contacto, no el webhook.

**El webhook del bot NO está registrado.** No hace falta hasta la fase 9
(comandos entrantes), y por eso el `delivery_url` de la app apunta a un
`ejemplo.invalid` a propósito: si algún día se registra el webhook sin cambiar
esa URL, los entrantes van a fallar y hay que acordarse de esto.

**`registrar-app.ts` es idempotente**: se corre igual para rotar la API key.
Guarda el *nombre* de cada variable de entorno, nunca su valor.

## Operación

Rutas en el servidor:

| Qué | Dónde |
|---|---|
| Binario | `/usr/local/bin/server-status` |
| Config | `/etc/server-status/config.yaml` |
| Secretos | `/etc/server-status/env` (`0600`) |
| Base | `/var/lib/server-status/status.db` |
| Unit | `/etc/systemd/system/server-status.service` |

### Watchdog

El check de Healthchecks.io se llama `server-status`, con **período de 5 min y
gracia de 10**: el período tiene que coincidir con el ticker de `main.go`, y los
15 minutos de silencio total son el margen que tolera un reinicio o un `make
deploy` sin despertar a nadie. Avisa por Telegram y por mail.

La URL de ping va en `HEALTHCHECKS_PING_URL`, en `/etc/server-status/env`. **Es
un secreto**: quien la tenga late en tu nombre y deja el watchdog neutralizado
sin que se note. Si falta, el proceso arranca igual y loguea `WARN watchdog
apagado`.

**Para reverificarlo sin tocar producción**, apuntar `url_publica` a una ruta que
no existe y reiniciar. `url_publica` la usa **solo** el watchdog, así que romperla
no afecta la portada ni los probes:

```yaml
url_publica: https://status.jadd.com.ar/no-existe
```

A los 5 minutos el journal tiene que decir `ERROR no se pudo latir err="la
portada devolvió 404 Not Found"`. Verificado así el 9/8/2026: el DOWN llegó por
Telegram y por mail al vencer la gracia, y el UP al revertir. **El 404 es a
propósito**: prueba que el watchdog evalúa el contenido y no se conforma con que
haya respuesta — que es justo el caso de Caddy sirviendo un archivo viejo con 200.

**La copia para el backup se rehace a las 04:00**, antes de que el restic de la
Mac mini corra a las 04:30. Copiar `status.db` en vivo **no sirve**: con WAL
puede quedar a mitad de una transacción. Para hacerla a mano:
`server-status backup`.

✅ **Verificado el 12/8/2026: la Mac mini ya se lleva
`/var/lib/server-status/backup/`.** Hasta esa fecha **no lo hacía**: la copia se
generaba todos los días y no la levantaba nadie, así que estos 41 MB de historial
no tenían respaldo. Se le agregó su propio `rsync` al script de la mini; **acá no
hubo que tocar nada**, porque el backup es pull y este lado solo tiene que dejar
el archivo listo.

Si alguna vez se reescribe el script de la mini, esto es lo primero que se cae
sin hacer ruido: `/var/lib/server-status/backup/` no cuelga de `/opt/stacks` ni
de `/opt/backups`, que es de donde sale todo lo demás.

```bash
ssh vps 'systemctl status server-status'
ssh vps 'sudo journalctl -u server-status -n 50 --no-pager'
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml sample'
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml containers'
ssh vps '/usr/local/bin/server-status -config /etc/server-status/config.yaml incidents'
```

**Para probar la máquina de estados sin cortar producción**, agregar un servicio
a la config que falle a propósito y después arreglarlo:

```yaml
  - nombre: prueba-caida
    probe: https://jadd.com.ar/
    estado_esperado: 500      # el sitio devuelve 200 → falla siempre
```

Cambiar el `estado_esperado` a 200 lo hace recuperar. Con eso se verifica el
ciclo completo —abre a la tercera falla, cierra al segundo éxito— sin tocar
ninguna app. **Acordarse de sacarlo después.**

## Convenciones de commit

Heredadas de communication-tool, sin cambios:

- **NO agregar `Co-Authored-By: Claude`** ni líneas de autoría de IA.
- Prefijos convencionales (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`).
- Commits chicos y revisables.
- **Los PR se mergean con `--rebase` o `--merge`, NUNCA con `--squash`.**
  Preferencia explícita del usuario: quiere ver todos los commits en `main`.

## Ante la duda

El spec responde la mayoría de las preguntas. Si algo genuinamente no está,
preguntar — no inventar.
