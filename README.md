# server-status

Monitoreo, avisos por Telegram y página de estado para un VPS chico.

Un binario en Go, sin cgo, que corre como servicio de systemd **en el host** —
no en un container. Esa decisión es el centro del diseño: leer `/proc` desde
adentro de un container da la vista del container y no la de la máquina, y un
monitor que vive dentro de Docker se muere junto con Docker, justo cuando hacía
falta que avisara.

## Estado

✅ **Completo.** Fases 0 a 9, corriendo en el VPS. El servicio muestrea el host
cada 15 s, lee el estado de los containers, pincha cada servicio por su URL
pública, abre y cierra incidentes solo y avisa por Telegram. Sirve una portada
pública estática, un panel privado por tailnet (con historial graficado, tablas
ordenables por columna, memoria y disco alternables entre % y GiB, búsqueda de logs con FTS5, tail en vivo y export de
logs como texto plano) y comandos por Telegram; un watchdog externo cubre el
caso de que se muera todo.

Además detecta **hechos puntuales** —que la máquina se reinició, que volvieron
containers— que las reglas por estado sostenido no pueden ver, y los muestra
junto a los incidentes y los errores de log en una línea de tiempo única en
`/eventos`. Cada línea de log se clasifica en **TRACE / INFO / WARN / ERROR**,
que es lo que permite filtrar el ruido: en esta instalación el 79 % del volumen
es TRACE, y sin ese filtro una consulta de "últimas 24 h" alcanzaba el tope de
líneas a las 5 horas y recortaba el resto sin decirlo.

| Fase | Qué trae | Estado |
|---|---|---|
| 0 | Scaffold, CI, unit de systemd | ✅ |
| 1 | Métricas del host → SQLite | ✅ |
| 2 | Estado de containers vía API de Docker | ✅ |
| 3 | Probes de servicios e incidentes | ✅ |
| 4 | Avisos por Telegram | ✅ |
| 5 | Panel privado | ✅ |
| 6 | Watchdog externo | ✅ |
| 7 | Portada pública | ✅ |
| 8 | Logs: búsqueda, tail y alertas | ✅ |
| 9 | Comandos por Telegram | ✅ |

## Diseño

- [Spec](docs/superpowers/specs/2026-08-08-server-status-design.md) — decisiones,
  arquitectura, modelo de datos e invariantes.
- [Plan de las fases 0 y 1](docs/superpowers/plans/2026-08-08-server-status-fase-0-1.md)

## Instalación

**[INSTALLATION.md](INSTALLATION.md)** tiene la guía completa: requisitos, el
script `deploy/install.sh` que deja todo instalado en un paso, el camino manual
equivalente y cómo configurar los avisos, el watchdog y la portada. La versión
corta:

```bash
git clone https://github.com/juanandresdavila/server-status.git
cd server-status
sudo ./deploy/install.sh
```

## Desarrollo

```bash
make test      # go test ./... -race
make vet
make build     # binario para la máquina local
make linux     # cross-compile a linux/amd64, sin cgo
make hooks     # activa el hook de pre-push con gitleaks
```

**Antes del primer push hace falta gitleaks** (`brew install gitleaks`): el hook
de `pre-push` escanea el repo antes de que algo salga de la máquina. En un repo
público, esperar a que CI lo detecte llega tarde — cuando CI falla, el secreto ya
es público.

## Configuración

Nada sensible vive en este repo. `deploy/config.example.yaml` se copia a
`/etc/server-status/config.yaml` en el servidor, y los tokens van aparte en
`/etc/server-status/env` con modo `0600`.

## Licencia

[MIT](LICENSE) — usalo, copialo y adaptalo, solo conservá el aviso de copyright.

Las dependencias de frontend se vendorean con su licencia al lado, sin CDN: el
panel vive en el tailnet y no tiene por qué depender de que el navegador llegue
a internet para dibujar un gráfico.
