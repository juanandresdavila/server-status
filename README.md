# server-status

Monitoreo, avisos por Telegram y página de estado para un VPS chico.

Un binario en Go, sin cgo, que corre como servicio de systemd **en el host** —
no en un container. Esa decisión es el centro del diseño: leer `/proc` desde
adentro de un container da la vista del container y no la de la máquina, y un
monitor que vive dentro de Docker se muere junto con Docker, justo cuando hacía
falta que avisara.

## Estado

🔄 En construcción. Fases 0 y 1 (scaffold y métricas del host) en curso.

| Fase | Qué trae | Estado |
|---|---|---|
| 0 | Scaffold, CI, unit de systemd | 🔄 |
| 1 | Métricas del host → SQLite | 🔄 |
| 2 | Estado de containers vía API de Docker | ⏸️ |
| 3 | Probes de servicios e incidentes | ⏸️ |
| 4 | Avisos por Telegram | ⏸️ |
| 5 | Panel privado | ⏸️ |
| 6 | Watchdog externo | ⏸️ |
| 7 | Portada pública | ⏸️ |
| 8 | Logs: búsqueda, tail y alertas | ⏸️ |
| 9 | Comandos por Telegram | ⏸️ |

## Diseño

- [Spec](docs/superpowers/specs/2026-08-08-server-status-design.md) — decisiones,
  arquitectura, modelo de datos e invariantes.
- [Plan de las fases 0 y 1](docs/superpowers/plans/2026-08-08-server-status-fase-0-1.md)

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
