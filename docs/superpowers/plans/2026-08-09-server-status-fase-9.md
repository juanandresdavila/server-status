# server-status — Plan de implementación, fase 9: comandos por Telegram

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Escribirle `/status` al bot desde el celular y que conteste. Más `/logs`, `/incidentes` y `/silenciar`.

**Spec:** §11 del spec principal.

---

## El problema de red, ya medido

El spec dejó esto explícitamente **para medir, no para decidir en el papel**. Medido el 9/8/2026 desde adentro del container de comm-tool:

| Destino | Resultado | Por qué |
|---|---|---|
| `127.0.0.1:8090` | inalcanzable | Es el loopback del **container**, no el del host |
| IP de tailnet del host | **inalcanzable** | El paquete entra por `docker0`, no por `tailscale0`, y las reglas de ufw están escritas por interfaz |
| Gateway del bridge | inalcanzable | El panel no escucha ahí |

comm-tool vive en `comm-tool_default` = **`172.20.0.0/16`**.

**La solución elegida:** un **listener aparte** para el webhook, en otro puerto, y una regla de ufw acotada a esa subred y ese puerto.

**Por qué no ampliar la regla al puerto del panel:** el panel muestra todo —errores crudos, métricas, nombres de containers, logs— y abrirlo a cualquier container del servidor es superficie regalada. El webhook, en cambio, no expone nada: solo acepta cuerpos firmados.

---

## Task 1: Verificar la firma HMAC

Es el control de acceso entero del webhook: si esto está mal, cualquiera que llegue al puerto puede mandar comandos.

**Files:** `internal/notify/commtool/firma.go`, `firma_test.go`

- [ ] **Step 1: Tests**

```go
// La firma se calcula sobre los BYTES EXACTOS del cuerpo. Volver a serializar
// el JSON parseado cambia el HMAC — lo dice el propio cliente de comm-tool.
func TestFirmaValidaConElCuerpoExacto(t *testing.T)
func TestUnByteDistintoEnElCuerpoInvalidaLaFirma(t *testing.T)
func TestFirmaViejaSeRechaza(t *testing.T)   // ventana de 300 s
func TestFirmaFuturaSeRechaza(t *testing.T)  // |deriva| > 300
func TestHeaderMalFormadoSeRechaza(t *testing.T)
func TestSecretoDistintoSeRechaza(t *testing.T)
```

- [ ] **Step 2-4: Implementar** con `hmac.Equal` (timing-safe) y ventana de 300 s, idéntico a `firmaValida` de comm-tool.

---

## Task 2: Migración 0007 — dedupe y silencio

**Files:** `internal/store/store.go`, tests

- [ ] Tabla `comandos_procesados(delivery_id TEXT PRIMARY KEY, procesado_en INTEGER)`:
  comm-tool reintenta hasta 5 veces, y sin dedupe un `/silenciar 2h` se aplicaría cinco veces.
- [ ] Tabla `silencio(hasta INTEGER)` de una sola fila.
- [ ] Tests: procesar dos veces el mismo id no repite; el silencio vencido no silencia.

---

## Task 3: Parseo de comandos

**Files:** `internal/comandos/comandos.go`, tests

- [ ] `Parsear("/logs comm-tool 50")` → `{Nombre:"logs", Args:["comm-tool","50"]}`
- [ ] Tolerar `/status@serverstatusjaddbot` — Telegram agrega el sufijo en grupos.
- [ ] Texto que no arranca con `/` → no es comando.
- [ ] Comando desconocido → responde con la lista, no con silencio.

---

## Task 4: Los cuatro comandos

**Files:** `internal/comandos/ejecutar.go`, tests

| Comando | Responde |
|---|---|
| `/status` | Estado de cada servicio, disco, RAM, uptime |
| `/logs <container> [n]` | Últimas n líneas (default 20, tope 50 por el límite de 4096 caracteres de Telegram) |
| `/incidentes` | Los últimos 10 |
| `/silenciar <duración>` | Guarda hasta cuándo callar; `/silenciar 0` cancela |

- [ ] Test: la respuesta **nunca** pasa los 4096 caracteres, con logs largos de verdad.

---

## Task 5: Respetar el silencio al avisar

**Files:** `internal/notify/notificador.go`

- [ ] Durante el silencio, los avisos `warning` **no se mandan pero SÍ se marcan**
      —si no, se acumulan y se vomitan todos juntos al vencer.
- [ ] Los `critical` pasan igual: silenciar no puede tapar que se cayó un servicio.
- [ ] Test de las dos cosas.

---

## Task 6: Listener del webhook y regla de ufw

**Files:** `cmd/server-status/main.go`, `internal/config/config.go`

- [ ] Listener aparte en `webhook_addr` (IP de tailnet, puerto distinto al del panel).
- [ ] Regla acotada:

```bash
sudo ufw allow proto tcp from 172.20.0.0/16 to <ip-tailnet> port 8091 \
  comment 'comm-tool -> server-status webhook'
```

- [ ] Verificar que **el panel sigue sin ser alcanzable** desde un container.

---

## Task 7: Registrar el webhook y verificar

⚠️ **`setWebhook` es exclusivo por bot.** El bot de status no tiene webhook hoy, así que es inofensivo — pero el comando es el mismo que le sacaría los updates a otro bot, así que hay que apuntar bien el token.

- [ ] Actualizar el `delivery_url` de la app en comm-tool.
- [ ] `setWebhook` del bot de status contra comm-tool, con su `secret_token`.
- [ ] Mandarle `/status` al bot desde Telegram y que conteste.
- [ ] Confirmar que **los avisos siguen saliendo** — el webhook no puede haber roto la vía de salida.

---

## Autorevisión

**Riesgo más alto: la Task 1.** La firma es el control de acceso entero del webhook. Seis tests, incluido el de que un byte distinto en el cuerpo invalida.

**Segundo riesgo: la regla de ufw.** Acotada a una subred y un puerto, y con verificación explícita de que el panel sigue cerrado.

**Fuera de alcance:** cualquier comando que **modifique** el servidor. `/silenciar` cambia estado del propio server-status y nada más — un `/reiniciar` por Telegram sería una puerta trasera a root con la seguridad de un HMAC y una regla de firewall.
