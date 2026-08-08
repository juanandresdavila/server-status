# server-status — Diseño

**Fecha:** 8 de agosto de 2026
**Estado:** 🔄 spec aprobado, sin implementar

> Monitoreo, avisos y página de estado del VPS de OVH. Un binario en Go corriendo
> como servicio de systemd en el host. Repo público.
> El contexto del servidor está en
> `~/Documents/1. Proyectos/4. Servidor/2. VPS/VPS OVHcloud - Configuracion inicial.md`.

---

## 1. Qué resuelve

Hoy no hay forma de saber si el VPS está sano sin entrar por SSH y mirar a mano.
Con tres stacks de aplicaciones corriendo —uno de ellos, comm-tool, en el camino
crítico de GymTracker— eso significa que una caída se descubre cuando alguien
nota que una app dejó de andar.

El proyecto entrega tres cosas:

1. **Aviso proactivo por Telegram** cuando algo se rompe, o está por romperse.
2. **Un panel privado** con métricas del host, estado de containers y logs.
3. **Una portada pública** en `status.jadd.com.ar` que dice si los servicios andan.

---

## 2. Decisiones tomadas

| Decisión | Elegido | Por qué |
|---|---|---|
| Naturaleza del proyecto | App propia de punta a punta | Es un proyecto de programación para repo público, no una configuración de herramientas ajenas |
| Lenguaje | **Go** | Binario estático sin runtime, ~15 MB de RAM, es el lenguaje del tooling de infra que ya se usa (Docker, Caddy, Tailscale) |
| Dónde corre | **Binario + unit de systemd en el host**, fuera de Docker | Mide bien el host (leer `/proc` desde un container da la vista del container) y **sobrevive a que Docker se caiga**, que es justo el aviso que hay que poder dar |
| Almacenamiento | **SQLite** (`modernc.org/sqlite`) | Un archivo, cero containers extra, FTS5 para los logs. Menos servicios que se puedan romper — en un monitor eso pesa más que en cualquier otra app |
| Visibilidad | Portada pública mínima + panel privado completo | Le decís al mundo si tus apps andan sin regalarle el mapa de la infraestructura |
| Panel privado | **Solo por tailnet** (`<ip-tailnet>:8090`) | Mismo patrón que Cockpit y Dockge. Access queda como agregado posterior si hace falta |
| Vía de aviso | **comm-tool**, con fallback directo a la API de Telegram | comm-tool además **recibe**: habilita los comandos de la fase 9. El fallback cubre el caso en que lo caído sea comm-tool |
| Watchdog externo | **Healthchecks.io** (free) | Confirmado: 20 checks e integración con Telegram en el plan gratuito |
| Política anti-ruido | Un aviso al caer, uno al recuperarse, silencio en el medio | Más amortiguación de rebotes, único agregado (ver §6) |

**Descartado explícitamente:** UptimeRobot como watchdog — su plan gratuito da 50
monitores pero solo 5 de 12 integraciones, sin webhooks, así que no se lo puede
hacer llegar a un bot propio.

---

## 3. Arquitectura

Un proceso, `/usr/local/bin/server-status`, corriendo bajo systemd con usuario
propio. Todo lo demás son caminos que entran o salen de él.

```
Host — OVH VPS
│
├── server-status (systemd)
│     ├── lee /proc y /sys .......................... métricas del host
│     ├── habla con docker.sock ..................... estado, health, restarts, CPU/RAM, logs
│     ├── escribe /var/lib/server-status/status.db .. SQLite
│     ├── escribe /opt/status/public/index.html ..... la portada, cada 30 s
│     └── escucha <ip-tailnet>:8090 .................. panel privado, solo tailnet
│
└── Docker daemon
      ├── caddy ── sirve /opt/status/public (bind mount ro) ── status.jadd.com.ar
      ├── cloudflared
      ├── comm-tool + comm-tool-db
      └── supabase-gym (6 containers) · supabase-sm (7 containers)
```

**Salidas del proceso:**

| Salida | Destino | Frecuencia |
|---|---|---|
| Probes de servicios | URLs públicas (`comm.jadd.com.ar/health`, etc.) | 60 s |
| Avisos | comm-tool → Telegram; fallback `api.telegram.org` | por evento |
| Auto-consulta | `https://status.jadd.com.ar/` (vuelta completa por Cloudflare) | 5 min |
| Latido | Healthchecks.io | 5 min, solo si la auto-consulta pasó |

### Dos decisiones que definen la forma del sistema

**El proceso nunca atiende un request público.** La portada es un archivo
estático que escribe el proceso y sirve Caddy. El proceso necesita hablarle al
socket de Docker, y eso equivale a ser root en el host; un proceso
root-equivalente que además atiende a internet es exactamente lo que no se
quiere. La superficie pública queda reducida a un `.html` en disco.

**Los probes salen por la URL pública, no por localhost.** Pinchar
`https://comm.jadd.com.ar/health` en vez de `http://127.0.0.1:8787/health`
prueba de paso Cloudflare, el túnel y Caddy — el camino que usa la gente. Un
probe interno diría "todo verde" el día que se rompa el túnel.

---

## 4. Componentes

| Paquete | Responsabilidad | Depende de |
|---|---|---|
| `internal/collector/host` | Parsea `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, `/proc/net/dev`, `statfs` | nada |
| `internal/collector/docker` | Cliente mínimo de la API de Docker sobre socket unix | `docker.sock` |
| `internal/store` | SQLite: esquema, migraciones, escritura, consultas, retención | archivo |
| `internal/prober` | Probes HTTP, latencia, clasificación del resultado | red |
| `internal/rules` | Umbrales, histéresis, máquina de estados de incidentes | `store` |
| `internal/notify` | `commtool` + `telegram`, política anti-ruido, resumen diario | red |
| `internal/web` | Panel privado y render de la portada estática | `store` |
| `internal/watchdog` | Auto-consulta pública y latido a Healthchecks | red |
| `cmd/server-status` | Único lugar que arma las dependencias y lee el entorno | todos |

**Dos elecciones de librería que no son opinables:**

- **`modernc.org/sqlite`, no `mattn/go-sqlite3`.** El segundo necesita cgo, y con
  cgo el binario deja de ser estático — se cae la premisa entera de correr como
  unit de systemd desplegada por `scp`.
- **Cliente de Docker escrito a mano, no la SDK oficial.** Son cuatro endpoints
  (`/containers/json`, `/containers/{id}/stats`, `/containers/{id}/logs`,
  `/events`) más un `DialContext` al socket unix. La SDK arrastra un árbol de
  dependencias enorme para eso.

**`/containers/{id}/stats?stream=false` bloquea entre 1 y 2 segundos** porque
necesita dos lecturas para calcular el porcentaje de CPU. Con 19 containers en
serie son más de 30 segundos y no cierra con un ciclo de 60. Va concurrente, con
límite de 8 en vuelo.

---

## 5. Modelo de datos

Todo en UTC. La conversión a `America/Argentina/Buenos_Aires` es de presentación
y de las reglas horarias, nunca de almacenamiento.

```sql
-- Muestras del host: se muestrea cada 15 s en memoria y se persiste
-- el agregado del minuto. Se guarda promedio y máximo porque un pico
-- de 20 segundos desaparece en un promedio de 60.
host_samples(
  ts INTEGER PRIMARY KEY,           -- unix seconds, truncado al minuto
  cpu_pct_avg REAL, cpu_pct_max REAL,
  load1 REAL, load5 REAL, load15 REAL,
  mem_used_bytes INTEGER, mem_total_bytes INTEGER,
  swap_used_bytes INTEGER, swap_total_bytes INTEGER,
  disk_used_bytes INTEGER, disk_total_bytes INTEGER,
  net_rx_bytes INTEGER, net_tx_bytes INTEGER,   -- contadores acumulados del kernel
  uptime_seconds INTEGER
)

container_samples(
  ts INTEGER, container TEXT,       -- nombre, no id: el id cambia en cada recreación
  state TEXT,                       -- running | exited | restarting | paused | dead
  health TEXT,                      -- healthy | unhealthy | starting | none
  restarts INTEGER,
  cpu_pct REAL, mem_bytes INTEGER,
  PRIMARY KEY (ts, container)
)

probe_results(
  ts INTEGER, service TEXT,
  ok INTEGER, status_code INTEGER, latency_ms INTEGER, error TEXT,
  PRIMARY KEY (ts, service)
)

-- La única tabla con estado, y la que gobierna los avisos.
incidents(
  id INTEGER PRIMARY KEY,
  subject TEXT NOT NULL,     -- 'service:comm-tool' | 'host:disk' | 'container:supabase-db' | 'logs:comm-tool'
  kind TEXT NOT NULL,        -- down | unhealthy | threshold | log_pattern | flapping
  severity TEXT NOT NULL,    -- critical | warning
  opened_at INTEGER NOT NULL,
  closed_at INTEGER,
  detail TEXT NOT NULL
)

-- Un solo incidente abierto por sujeto. Garantía de la base, no del código.
CREATE UNIQUE INDEX incidentes_abierto_unico
  ON incidents(subject) WHERE closed_at IS NULL;

-- Avisos ya entregados. El id es determinístico a propósito.
notifications(
  delivery_id TEXT PRIMARY KEY,   -- '<incident_id>:opened' | '<incident_id>:closed'
  sent_at INTEGER NOT NULL,
  via TEXT NOT NULL,              -- commtool | telegram
  error TEXT
)

logs(
  ts INTEGER, container TEXT, stream TEXT,   -- stdout | stderr
  line TEXT
)
-- Tabla virtual FTS5 sobre logs.line, para la búsqueda del panel.
```

**`delivery_id` determinístico** es una lección copiada literal de comm-tool: con
un uuid nuevo por intento, un reintento manda el aviso dos veces.

**Retención**, en una pasada diaria a las 04:00 ART:

| Tabla | Se conserva |
|---|---|
| `host_samples`, `container_samples`, `probe_results` | 30 días a resolución de 1 minuto |
| Rollups horarios de las tres | 1 año |
| `logs` | 7 días o 500 MB, lo que llegue primero |
| `incidents`, `notifications` | sin límite — son pocos y son la historia del servidor |

La misma pasada hace `VACUUM INTO /var/lib/server-status/backup/status.db`.
**Copiar el archivo vivo de una base con WAL no da una copia consistente**, y el
backup con restic de la Mac mini corre a las 04:30: lo que se lleva es esa copia,
no la base en uso.

---

## 6. Ciclo de vida de un incidente

La cadena es `muestra → regla → incidente → aviso`. **Solo el paso incidente
guarda estado**: si el proceso se reinicia, lee la tabla y sigue donde estaba —
no reabre nada ni remanda nada.

### Máquina de estados, por sujeto

```
sano ──[condición de apertura]──→ caído ──[condición de cierre]──→ sano
          📱 se abre incidente                📱 se cierra incidente
                (un mensaje)                        (un mensaje)
```

Mientras dura la caída no se dice una palabra.

**Hay dos familias de sujeto y cada una usa un disparador distinto** — es la
única sutileza del modelo:

| Familia | Sujetos | Abre | Cierra |
|---|---|---|---|
| **Por conteo** | `service:*`, `container:*` | 3 resultados fallidos seguidos | 2 resultados buenos seguidos |
| **Por umbral** | `host:*` | el valor cruza el umbral de apertura y **se mantiene** el tiempo indicado | el valor cae al umbral de cierre (histéresis) |

**Tres fallas antes de abrir** porque el VPS está a 179 ms medidos de Buenos
Aires y un hipo suelto no es una caída. **Dos éxitos antes de cerrar** porque un
servicio que rebota no se recuperó.

### Las tres reglas que evitan el ruido

**1 · Histéresis en los umbrales.** Cada umbral tiene un valor de apertura y uno
de cierre, distintos. Sin eso, un disco parado en 79,8% manda cuarenta mensajes
por noche.

| Sujeto | Abre | Sostenido | Cierra |
|---|---|---|---|
| `host:disk` | uso ≥ 80% | 5 min | uso ≤ 75% |
| `host:mem` | uso ≥ 90% | 10 min | uso ≤ 85% |
| `host:swap` | uso ≥ 25% | 10 min | uso ≤ 10% |
| `host:load` | `load1` ≥ 6 | 10 min | `load1` ≤ 4 |

`load1 ≥ 6` es un promedio de 1,0 por core sobre los 6 vCore del VPS-3.

**2 · Amortiguación de rebotes.** Si un mismo sujeto abre y cierra **más de 3
veces en una hora**, se emite un único incidente `flapping` —
*"comm-tool está inestable: 5 caídas en 40 minutos"* — y se suprimen las
transiciones de ese sujeto por el resto de la hora. Es el único agregado a la
política elegida, y es el que la hace sostenible: "uno al caer, uno al
recuperarse" con un servicio que rebota son doce mensajes.

**3 · Los logs se cuentan, no se reenvían.** Un patrón que matchea no dispara un
mensaje por línea. Abre incidente al pasar **10 coincidencias en 5 minutos**, y
el mensaje lleva **el conteo y una línea de muestra**, no las diez. Sin esto, una
app en loop de error manda mil mensajes y el bot termina silenciado — que es la
forma real en que muere un sistema de monitoreo.

---

## 7. Avisos

**Camino principal:** `POST /v1/messages` de comm-tool, autenticado con la API
key de la app `server-status`.

**Fallback:** si comm-tool no responde o devuelve 5xx, `POST` directo a
`https://api.telegram.org/bot<token>/sendMessage` con el `chat_id` del env. Es el
mismo bot: comm-tool impone un bot por app vía `bots_app_channel_unico`, así que
el token existe de todos modos.

Si **también** falla el fallback, hay backoff exponencial (1, 2, 4, 8, 16 min) y
**no** se escribe la fila en `notifications` — el reintento sigue vivo.

**Alta en comm-tool** (`scripts/registrar-app.ts` del repo de comm-tool):

- slug de app `server-status`, bot nuevo de BotFather con slug `status`
- `--delivery-url` apuntando al panel — ver §11, que **no** es `127.0.0.1`
- El `app_user_id` es un uuid fijo generado una vez y guardado en la config.
  server-status no tiene tabla de usuarios, y comm-tool **nunca interpreta un
  `app_user_id`**: es una invariante suya, no un accidente.
- Vinculación con `/vincular <código>` desde el Telegram propio, una sola vez.

**Resumen diario a las 08:00 ART**, siempre, aunque esté todo bien: uptime,
disco, RAM, incidentes de las últimas 24 h. Además de informar, confirma que el
circuito de avisos sigue vivo — **el silencio de un monitor roto es idéntico al
de un servidor sano**.

---

## 8. Watchdog externo

Healthchecks.io funciona al revés que un monitor común: el servicio late hacia
afuera y, si los latidos paran, avisa. Eso deja un agujero — si el túnel de
Cloudflare se cae pero el proceso sigue vivo, el latido sale igual y nadie se
entera.

Se cierra así, cada 5 minutos:

1. `GET https://status.jadd.com.ar/` — la vuelta completa: salida a internet,
   Cloudflare, túnel, Caddy, archivo.
2. **No alcanza con el 200.** Caddy sirve feliz un archivo viejo si el proceso
   dejó de escribirlo. Se verifica que la marca de tiempo embebida en el HTML
   tenga **menos de 3 minutos**.
3. Recién si las dos cosas dan bien, se le hace ping a Healthchecks.

Con período de 5 minutos y gracia de 15, cualquier falla del proceso, del túnel,
de Caddy o de la conectividad saliente termina en un mensaje de Telegram mandado
por Healthchecks, **desde afuera del servidor**.

---

## 9. Las dos caras web

### Portada pública — `status.jadd.com.ar`

Archivo estático que el proceso reescribe cada 30 segundos en
`/opt/status/public/index.html`, servido por Caddy con un bind mount de solo
lectura desde el stack `edge`.

**Se arma por lista blanca, no por lista negra.** Solo se renderizan cuatro
campos: nombre del servicio, estado, uptime del mes y la marca de tiempo. Todo lo
demás está prohibido por construcción, y hay un test que lo blinda.

Queda afuera, explícitamente: métricas del host, nombres de containers,
versiones, IPs y **el texto crudo del error de un probe** — un
`dial tcp 127.0.0.1:8787: connection refused` publica el mapa interno.

### Panel privado — `<ip-tailnet>:8090`

Solo por tailnet, mismo patrón que Cockpit y Dockge, sin autenticación propia: la
red es el control de acceso. Muestra métricas del host con gráficos, tabla de
containers, historial de incidentes, y desde la fase 8 la búsqueda y el tail de
logs.

Si más adelante hace falta entrar desde una máquina sin Tailscale, agregar
Cloudflare Access es una ruta en el Caddyfile y una política en Zero Trust — no
se tira nada de lo hecho.

---

## 10. Logs (fase 8)

Es la fase más grande y **va a tener su propio spec** cuando se llegue. El
alcance comprometido acá:

- **Ingesta** desde `/containers/{id}/logs?follow=1&since=<último>` por container,
  con reconexión y sin perder el punto de corte entre reinicios del proceso.
- **Búsqueda** por FTS5 en el panel: por container, rango de tiempo y texto.
- **Tail en vivo** por SSE en el panel.
- **Alertas por patrón**, con la regla de conteo por ventana de §6.

Los patrones son configurables por servicio. El default es
`(?i)\b(panic|fatal|error)\b`.

---

## 11. Comandos por Telegram (fase 9)

Entran por comm-tool, que ya resuelve webhook, dedupe, verificación de secreto e
identidad, y los entrega al panel — sin exponer nada a internet.

⚠️ **El `delivery_url` no puede ser `http://127.0.0.1:8090`.** comm-tool corre
adentro de un container: su `127.0.0.1` es el loopback del container, no el del
host. La entrega tiene que ir al host por IP, y eso choca con dos cosas de la
configuración actual del servidor:

- Un paquete de un container hacia la IP de tailnet entra al host por `docker0`,
  no por `tailscale0` — y las reglas de ufw del VPS están escritas por interfaz.
  Es probable que ufw lo descarte.
- Bindear a `172.17.0.1` (el gateway del bridge) evita ufw pero ata el servicio a
  una IP que Docker puede cambiar.

**Se resuelve al empezar la fase 9**, con dos caminos conocidos: una regla de ufw
que permita el origen de la subred del bridge hacia el 8090, o un segundo
listener del panel sobre el gateway del bridge. La fase arranca midiendo cuál
hace falta; no se decide en el papel.

| Comando | Qué hace |
|---|---|
| `/status` | Estado de todos los servicios, disco, RAM, uptime |
| `/logs <servicio> [n]` | Últimas n líneas (default 20) |
| `/silenciar <duración>` | Suprime avisos no críticos por un rato |
| `/incidentes` | Los últimos 10, abiertos y cerrados |

---

## 12. Seguridad y repo público

### Lo que nunca entra al repo

| Dato | Por qué |
|---|---|
| Token del bot de Telegram | acceso total al bot |
| API key de comm-tool | permite mandar mensajes como server-status |
| **URL de ping de Healthchecks** | es un UUID secreto: quien la tenga late en tu nombre y te deja el watchdog desactivado sin que lo notes |
| IP pública y de tailnet del VPS | La gracia de la arquitectura es que el origen sea inalcanzable: publicar la IPv4 de origen la anula. En este documento aparecen como `<ip-tailnet>`; los valores reales viven en `/etc/server-status/config.yaml`, en el servidor |
| Chat ID de Telegram | no es crítico, pero no hace falta que esté |

Mecánica: `config.example.yaml` versionado, `config.yaml` real en
`/etc/server-status/`, secretos en `/etc/server-status/env` con modo `0600` y
dueño del usuario del servicio, cargado por `EnvironmentFile=` de systemd.
`.gitignore` cubre `config.yaml`, `*.env`, `*.db` y `.superpowers/`.

### En un repo público, gitleaks en CI llega tarde

Cuando CI falla, el commit ya está pusheado y ya es público; hay bots que
escanean GitHub y prueban credenciales en segundos. La defensa real es un **hook
de `pre-push`** que corre gitleaks antes de que el objeto salga de la máquina.
CI lo corre igual, como segunda red — no como la primera.

### El proceso está en el grupo `docker`, y eso es ser root en el host

No hay forma de leer estado y logs de containers sin acceso al socket, y el
socket no distingue lectura de escritura. Se declara en vez de disimularse. Lo
que lo hace aceptable:

1. **No atiende internet** — la portada es un archivo estático (§3).
2. La unit lleva `NoNewPrivileges=yes`, `ProtectSystem=strict`,
   `ProtectHome=yes`, `PrivateTmp=yes`, `RestrictAddressFamilies=AF_INET AF_INET6
   AF_UNIX`, `ReadWritePaths=/var/lib/server-status /opt/status/public` y
   `MemoryMax=256M`.
3. Queda anotado como deuda: el endurecimiento futuro es un proxy de socket de
   solo lectura.

---

## 13. Configuración y rutas

| Qué | Dónde |
|---|---|
| Binario | `/usr/local/bin/server-status` |
| Configuración | `/etc/server-status/config.yaml` |
| Secretos | `/etc/server-status/env` (`0600`) |
| Base | `/var/lib/server-status/status.db` |
| Copia consistente | `/var/lib/server-status/backup/status.db` |
| Portada | `/opt/status/public/index.html` |
| Unit | `/etc/systemd/system/server-status.service` |

Usuario de sistema `server-status`, sin shell ni home, miembro del grupo
`docker`.

**Variables del env:** `COMM_TOOL_URL`, `COMM_TOOL_API_KEY`,
`TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`, `HEALTHCHECKS_PING_URL`.

**La config define los servicios.** Cada servicio es un nombre, una URL de probe
con su path, y la lista de containers que lo componen:

```yaml
servicios:
  - nombre: jadd.com.ar
    probe: https://jadd.com.ar/
    containers: [caddy, cloudflared]
  - nombre: comm-tool
    probe: https://comm.jadd.com.ar/health
    containers: [comm-tool, comm-tool-db]
  - nombre: study-master
    probe: https://supabase-sm.jadd.com.ar/auth/v1/health
    containers: [supabase-kong, supabase-auth, supabase-rest, supabase-db,
                 supabase-storage, supabase-meta, supabase-imgproxy]
  - nombre: gym-tracker
    probe: https://supabase-gym.jadd.com.ar/auth/v1/health
    containers: [supabase-gym-kong, supabase-gym-auth, supabase-gym-rest,
                 supabase-gym-db, supabase-gym-meta, supabase-gym-studio]
```

El path del probe es configuración, no código. La fase 3 arranca verificando que
cada uno devuelva 200 contra el servicio real; si alguno no, se corrige el path
en la config. Los de Supabase son los candidatos a confirmar.

---

## 14. Despliegue

Go sin cgo cross-compila limpio: `GOOS=linux GOARCH=amd64 go build` desde la
MacBook produce el binario exacto que corre en el VPS. **No hace falta instalar
Go en el servidor.**

```
build en la Mac → scp por Tailscale → systemctl restart server-status
```

GitHub Actions publica además el binario como release del repo público.

**Un cambio fuera de este repo:** el stack `edge` de `vps-stacks` necesita el
bind mount `/opt/status/public:/srv/status:ro` y un bloque de sitio para
`status.jadd.com.ar` con `root` y `file_server`. Es la única pieza que vive en
otro lado.

---

## 15. Fases

| # | Fase | Cómo se verifica |
|---|---|---|
| 0 | Scaffold: módulo Go, layout, CI, hook de pre-push, unit de systemd, `config.example.yaml` | compila, CI en verde, la unit arranca y queda viva |
| 1 | Métricas del host + SQLite + retención | `server-status sample --once` contra `free -h`, `df -h`, `uptime` en el VPS |
| 2 | Containers vía API de Docker | `server-status containers` contra `docker stats` real |
| 3 | Probes + máquina de incidentes | parar un container a propósito y ver el incidente abrir y cerrar |
| 4 | **Avisos por Telegram** + resumen diario | parar un container y recibir el mensaje real |
| 5 | Panel privado por tailnet | abrirlo y contrastar los números con Cockpit |
| 6 | Watchdog: auto-consulta + Healthchecks | apuntar la auto-consulta a una URL rota y ver que deja de latir |
| 7 | Portada pública + bloque en Caddy | `curl https://status.jadd.com.ar` desde afuera del tailnet |
| 8 | Logs (spec propia) | búsqueda y tail contra logs reales |
| 9 | Comandos por Telegram | mandarle `/status` al bot |

Los avisos van en la 4 y el panel en la 5 a propósito: la CLI de la fase 3 ya
permite verificar incidentes sin navegador, así que el valor real —que te
avise— llega antes de invertir en gráficos.

---

## 16. Testing

Calcado del estilo de comm-tool: **ningún test toca la red ni el socket de
Docker real.**

- **Parsers contra golden files.** Se capturan `/proc/stat`, `/proc/meminfo`,
  `/proc/loadavg` y `/proc/net/dev` del VPS real a `testdata/` y se testea
  contra esos archivos.
- **Cliente de Docker contra `httptest`** sobre un socket unix temporal, con
  respuestas grabadas de la API real.
- **Reloj inyectado** en toda la máquina de estados. Las reglas de 3 fallas, 2
  éxitos, histéresis, rebotes y ventanas horarias se testean con tiempo
  simulado.
- **Store contra SQLite en memoria.**
- **Test de lista blanca de la portada**: renderiza con datos que incluyen
  métricas del host y errores crudos de probe, y verifica que **no** aparezcan en
  la salida.
- **Test de reinicio**: con incidentes abiertos en la base, arrancar de nuevo no
  emite ningún aviso.

---

## 17. Invariantes

1. **El proceso nunca atiende un request público.** La portada es un archivo en
   disco que sirve Caddy.
2. **Un incidente abierto por sujeto**, garantizado por
   `incidentes_abierto_unico`. Sin ese índice, "el incidente de este servicio"
   depende del orden del `SELECT`.
3. **`delivery_id` es determinístico**: `<incidente>:<transición>`.
4. **La portada se arma por lista blanca.**
5. **Nadie llama a `time.Now()` fuera del reloj inyectado.**
6. **Los probes salen por la URL pública, nunca por localhost.**
7. **Sin cgo.** El binario tiene que ser estático.
8. **Los secretos viven en el env file.** La base guarda configuración y estado,
   nunca el valor de un secreto.
9. **`cmd/server-status` es el único que lee el entorno.** Los paquetes de
   `internal/` reciben sus dependencias inyectadas.

---

## 18. Fuera de alcance

- Monitorear más de un servidor.
- Métricas de aplicación (APM, trazas, tiempos por endpoint).
- Avisos por mail o SMS.
- Autenticación de usuarios en el panel — la resuelve la red.
- Historial de más de un año.
- Página de incidentes con postmortems redactados.

---

## 19. Riesgos y deuda anotada

| Riesgo | Estado |
|---|---|
| El proceso en el grupo `docker` es root-equivalente | Aceptado y mitigado (§12). Deuda: proxy de socket de solo lectura |
| Si Cloudflare se cae, la portada pública se cae aunque todo esté sano | Aceptado: el panel por tailnet sigue andando y el watchdog avisa |
| Healthchecks.io es un tercero en el camino del último aviso | Aceptado: es el único punto donde un tercero es preferible a uno propio |
| Los paths de probe de Supabase están sin confirmar | Se confirman al empezar la fase 3, contra el servicio real |
| La fase 8 (logs) es grande | Va a tener spec propia antes de implementarse |

---

## 20. Entregables de documentación

Por el `CLAUDE.md` de `1. Proyectos`, todo tema documentado va en dos formatos.
Al cerrar la fase 7 se escriben, en una subcarpeta nueva
`4. Servidor/4. Monitoreo y status/`:

- **`Monitoreo del VPS - server-status.md`** — memoria técnica: arquitectura,
  rutas, comandos de operación, cómo se dio de alta el bot, cómo se diagnostica.
- **`Monitoreo del VPS - Resumen ejecutivo.docx`** — versión legible.

Y se actualiza la tabla de temas activos del `CLAUDE.md` de la carpeta.

---

## 21. Convenciones del repo

Heredadas de comm-tool, sin cambios:

- **NO agregar `Co-Authored-By: Claude`** ni líneas de autoría de IA.
- Prefijos convencionales (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`).
- Commits chicos y revisables.
- **Los PR se mergean con `--rebase` o `--merge`, NUNCA con `--squash`.**

---

## 22. Fuentes

- [Healthchecks.io — pricing y free tier](https://drumbeats.io/blog/drumbeats-vs-healthchecks-io-pricing-features-comparison)
- [UptimeRobot — límites del plan gratuito](https://stillup.org/blog/uptimerobot-free-plan-limits)
- Estado del VPS medido por Tailscale el 8/8/2026: 19 containers, 3,0 GB de 11 en
  uso, 16 GB de 96 en disco, load 0,41.
