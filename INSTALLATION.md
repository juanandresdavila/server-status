# Instalación

`server-status` es un solo binario de Go, sin cgo, que corre como unit de
systemd **en el host** — no en un container. Si Docker se cae, el monitor tiene
que seguir vivo para poder avisarlo.

## Requisitos

- Un servidor Linux (amd64) con **systemd**. Probado en Ubuntu.
- **Docker** solo si querés monitorear containers; sin Docker el resto funciona
  igual.
- **Go 1.26+** para compilar — en tu máquina o en el servidor, donde prefieras.
  El binario es estático: compilado en una Mac o en cualquier Linux, corre igual
  en el servidor sin instalar nada más.
- Opcionales, cada uno activa una parte:
  - Un **bot de Telegram** (de [@BotFather](https://t.me/BotFather)) para los avisos.
  - Un check de [Healthchecks.io](https://healthchecks.io) para el watchdog externo.
  - **Caddy** (o cualquier server de estáticos) para servir la portada pública.

## Camino rápido: `install.sh`

En el servidor, con el repo clonado:

```bash
git clone https://github.com/juanandresdavila/server-status.git
cd server-status
sudo ./deploy/install.sh
```

El script compila (o usa `dist/server-status-linux-amd64` si ya lo trajiste
compilado), crea el usuario de sistema, los directorios, copia la config de
ejemplo y deja la unit instalada y habilitada. **No arranca el servicio**: antes
hay que completar la configuración (siguiente sección). Es idempotente — se
puede correr de nuevo para actualizar el binario y no pisa una config existente.

Si preferís compilar en tu máquina y no tener Go en el servidor:

```bash
make linux                                  # deja dist/server-status-linux-amd64
scp -r dist deploy tu-servidor:server-status/
ssh tu-servidor 'cd server-status && sudo ./deploy/install.sh'
```

## Camino manual

Lo mismo que hace el script, paso a paso:

```bash
# 1. Compilar (acá o en tu máquina; sin cgo el binario es portable)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o server-status ./cmd/server-status

# 2. Usuario de sistema, con acceso al socket de Docker
sudo useradd --system --no-create-home --shell /usr/sbin/nologin server-status
sudo usermod -aG docker server-status

# 3. Directorios
sudo install -m 0755 server-status /usr/local/bin/server-status
sudo mkdir -p /etc/server-status /opt/status/public
sudo install -d -o server-status -g server-status /var/lib/server-status
sudo chown server-status:server-status /opt/status/public

# 4. Configuración
sudo cp deploy/config.example.yaml /etc/server-status/config.yaml
sudo cp deploy/env.example /etc/server-status/env
sudo chmod 0600 /etc/server-status/env
sudo chown server-status:server-status /etc/server-status/env

# 5. Unit
sudo cp deploy/server-status.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable server-status
```

## Configurar

### `/etc/server-status/config.yaml`

El [ejemplo](deploy/config.example.yaml) está comentado campo por campo. Lo
mínimo que hay que tocar:

| Campo | Qué es |
|---|---|
| `servicios` | La lista de qué probar y con qué URL. **Los probes salen por la URL pública**, nunca por localhost: así prueban también el túnel y el proxy. |
| `panel_addr` | Dónde escucha el panel privado. Atarlo a una IP privada (Tailscale/WireGuard), **nunca** `0.0.0.0`: el panel muestra todo. Vacío lo apaga. |
| `portada_path` | Dónde escribir el HTML de la portada pública. Vacío la apaga. |
| `url_publica` | La URL por la que el watchdog se verifica a sí mismo antes de latir. |

### `/etc/server-status/env`

Los secretos **nunca** van en la config ni en la base; van acá, con modo
`0600`. El [ejemplo](deploy/env.example) explica cada variable:

- `TELEGRAM_BOT_TOKEN` y `TELEGRAM_CHAT_ID` — para los avisos por Telegram.
  El token te lo da BotFather; el chat id sale de mandarle `/start` al bot y
  mirar `https://api.telegram.org/bot<token>/getUpdates`.
- `HEALTHCHECKS_PING_URL` — la URL de ping del watchdog. Sin ella el proceso
  arranca igual y loguea `WARN watchdog apagado`.
- `COMM_TOOL_API_KEY` — solo si usás [comm-tool](https://github.com/juanandresdavila/communication-tool)
  como camino principal de avisos; con el token de Telegram solo ya funciona el
  camino directo.

### Portada pública (opcional)

El proceso **escribe** un archivo estático y otro lo sirve — el binario jamás
atiende un request de internet. Con Caddy alcanza:

```
status.tudominio.com {
    root * /opt/status/public
    file_server
}
```

## Arrancar y verificar

```bash
sudo systemctl start server-status
systemctl status server-status
sudo journalctl -u server-status -n 50 --no-pager
```

El binario trae subcomandos de diagnóstico que usan la misma config:

```bash
/usr/local/bin/server-status -config /etc/server-status/config.yaml sample      # métricas del host, ahora
/usr/local/bin/server-status -config /etc/server-status/config.yaml containers  # qué ve por el socket de Docker
/usr/local/bin/server-status -config /etc/server-status/config.yaml incidents   # incidentes registrados
```

Si configuraste Telegram, mandale `/status` al bot: tiene que responder con el
estado de los servicios y los recursos del host.

## Actualizar

Compilar de nuevo y reinstalar el binario; la base y la config quedan como
están (el esquema se migra solo al arrancar):

```bash
sudo ./deploy/install.sh          # en el servidor
```

o desde tu máquina, si tenés un alias `vps` en `~/.ssh/config`:

```bash
make deploy
```

## Desinstalar

```bash
sudo systemctl disable --now server-status
sudo rm /etc/systemd/system/server-status.service /usr/local/bin/server-status
sudo rm -r /etc/server-status /var/lib/server-status   # config, secretos e historial
```
