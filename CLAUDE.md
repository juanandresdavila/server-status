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
| 4 | Avisos por Telegram | 🔄 |
| 5-9 | Panel, watchdog, portada pública, logs, comandos | ⏸️ |

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

## Arquitectura

Un proceso. Todo lo demás son caminos que entran o salen de él.

```
/proc, /sys ──→ collector/host ──┐
docker.sock ──→ collector/docker ─┼─→ store (SQLite) ──→ web (fase 5)
                                  │        ↑
URLs públicas ←── prober ─────────┘        │
                                    rules/motor ──→ notify ──→ comm-tool → Telegram
                                                        └────→ api.telegram.org (respaldo)
```

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
orden, cada una en su transacción y registradas en `schema_migrations`. **Nunca
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
- **Los Supabase no exponen ningún endpoint que dé 2xx sin `apikey`.**
  `/auth/v1/health` y `/rest/v1/` devuelven 401 desde Kong. Se usa
  `/auth/v1/authorize` con `estado_esperado: 400`, porque ese 400 lo emite GoTrue
  después de que el request atravesó el gateway — prueba bastante más que un 401.
- **`/containers/{id}/stats` sin `stream=false` no termina nunca.** Deja la
  conexión abierta mandando una muestra por segundo.
- **La memoria de un container se calcula descontando `inactive_file`**, igual que
  `docker stats` en cgroup v2. Sin eso, todo container que leyó archivos parece
  estarse comiendo la RAM.
- **`/proc/net/dev` no se suma entero.** Quedan afuera `lo`, `docker*`, `br-*` y
  `veth*`: ese tráfico ya está contado del lado de la interfaz real.
- **La memoria usada se calcula contra `MemAvailable`, no contra `MemFree`.** El
  page cache figura como ocupado pero el kernel lo suelta cuando hace falta.

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

## Operación

Rutas en el servidor:

| Qué | Dónde |
|---|---|
| Binario | `/usr/local/bin/server-status` |
| Config | `/etc/server-status/config.yaml` |
| Secretos | `/etc/server-status/env` (`0600`) |
| Base | `/var/lib/server-status/status.db` |
| Unit | `/etc/systemd/system/server-status.service` |

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
