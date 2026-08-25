# Mejoras de UI del panel — plan de implementación

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ocho mejoras de uso pedidas el 25/08/2026: fechas completas, filtros
como toggles que se aplican solos, sacar los carteles de ayuda, panel bilingüe
con rutas en inglés, orden por defecto por estado, CPU de containers sobre la
capacidad total, y resolver/archivar incidentes desde el panel.

**Architecture:** Todo vive en los caminos ya existentes: el collector corrige
una fórmula, el store suma una migración y cambia una firma, y el resto es
`internal/web` (handlers + plantillas). No se toca el motor de reglas: cerrar
un incidente a mano usa el `CerrarIncidente` que ya existe, y el motor relee
`IncidentesAbiertos()` en cada tick, así que si el sujeto sigue mal lo reabre
solo — que es el comportamiento correcto.

**Tech Stack:** Go stdlib (html/template, net/http con method patterns de 1.22),
SQLite vía modernc, sin dependencias nuevas.

**Los 8 pedidos → tareas:**

| # | Pedido | Tarea |
|---|---|---|
| 1 | Fecha DD/MM/YYYY a la izquierda de la hora | T4 |
| 2 | Niveles/severidades como toggle por ítem, en /logs y /eventos | T3 (backend) + T6 (UI) |
| 3 | Sacar el cartel de ayuda | T7 |
| 4 | Rutas en inglés + toggle es/en | T9 |
| 5 | Servicios: por estado y después latencia | T5 |
| 6 | Containers: por estado y RAM; CPU sobre el 100 % de la máquina | T1 (CPU) + T5 (orden) |
| 7 | Marcar solucionados y archivar incidentes | T2 (store) + T8 (UI) |
| 8 | Filtros que se aplican solos, sin botón | T7 |

**Decisiones tomadas (para no reabrirlas):**

- **CPU de containers**: se corrige en el collector (la fuente), no en la vista.
  La fórmula vieja era la de `docker stats` (100 % = un core); la nueva divide
  por la capacidad total (`deltaCPU/deltaSys × 100`, sin `× cpus`). El
  histórico en `container_samples` queda en la escala vieja — se documenta, no
  se migra: no hay forma de recomputarlo.
- **Resolver** = `CerrarIncidente` de siempre. Eso genera el aviso `<id>:closed`
  por Telegram (cola derivada) — deseado: si lo cerraste a mano, avisar que
  cerró es verdad.
- **Archivar** exige incidente cerrado (`closed_at IS NOT NULL`): archivar uno
  abierto lo escondería mientras sigue roto. El panel esconde archivados; la
  línea de tiempo de /events los sigue mostrando — es historia.
- **Filtro por conjunto, no por mínimo**: `nivel=INFO&nivel=ERROR` (param
  repetido). Sin params → INFO+WARN+ERROR (el default de siempre). En /events
  igual con `sev=`. La semántica de `sev=critical` pasa de "critical o peor" a
  "solo critical" — con toggles es lo que el usuario ve.
- **Idioma**: cookie `lang` (es|en), la setea el query param `?lang=`. Dos sets
  de plantillas parseados al arrancar, uno por idioma, con FuncMap `t`. Español
  es el default. La portada pública NO cambia.
- **Rutas**: `/eventos` → `/events` con redirect 301 (marcadores viejos).
  Acciones nuevas: `POST /incidents/{id}/resolve` y `POST /incidents/{id}/archive`.
- **Auto-submit**: selects, checkboxes y datetime → submit al `change`; el campo
  de texto con debounce de 700 ms. Se van los botones "buscar"/"ver"; queda
  "exportar" (y un botón oculto primero en el form para que Enter no dispare el
  export por implicit submission).

---

### Task 1: CPU de containers sobre la capacidad total de la máquina

**Files:**
- Modify: `internal/collector/docker/containers.go` (cpuPct)
- Test: `internal/collector/docker/docker_test.go`

- [ ] **Step 1: Ajustar los tests que afirman la escala vieja**

En `docker_test.go`, los asserts de 60 pasan a 10 (deltaCPU=1000,
deltaSys=10000 → 10 % de la máquina, ya sin multiplicar por los 6 cores):

```go
// deltaCPU=1000, deltaSys=10000 → 10% de la capacidad total de la máquina.
// La fórmula vieja multiplicaba por los cores (docker stats: 100% = un core)
// y el panel mostraba containers "al 25%" con el host al 8%.
if !casiIgual(got.CPUPct, 10) {
    t.Errorf("CPUPct = %v, quería 10", got.CPUPct)
}
```

(igual en `TestRecolectarJuntaTodoYLimitaLaConcurrencia`: `casiIgual(uno.CPUPct, 10)`)

- [ ] **Step 2: Correr y ver fallar**

`go test ./internal/collector/docker/ -run TestStats -v` → FAIL (da 60).

- [ ] **Step 3: Corregir la fórmula**

```go
// cpuPct devuelve el uso como % de la CAPACIDAD TOTAL de la máquina, no de un
// core. docker stats multiplica por OnlineCPUs (ahí 100% = un core y el tope
// real es cores×100); acá eso hacía ver containers "al 25%" con el host al 8%.
// system_cpu_usage ya suma todos los cores, así que el cociente solo alcanza.
func cpuPct(s statsAPI) float64 {
    deltaCPU := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
    deltaSys := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
    if deltaCPU <= 0 || deltaSys <= 0 {
        return 0
    }
    return deltaCPU / deltaSys * 100
}
```

`OnlineCPUs` queda en el struct (documenta el gotcha) pero ya no se usa en la
cuenta.

- [ ] **Step 4: Verificar** — `go test ./internal/collector/docker/` → PASS
- [ ] **Step 5: Commit** — `fix(collector): el % de CPU de un container es sobre la máquina, no sobre un core`

### Task 2: archived_at en incidents + ArchivarIncidente

**Files:**
- Modify: `internal/store/store.go` (migración 11, consultarIncidentes, ArchivarIncidente)
- Modify: `internal/model/model.go` (campo ArchivadoEn)
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Tests primero**

`TestUltimaMigracionAplicada`: 10 → 11. Y el ciclo de archivo:

```go
// Archivar saca el incidente del panel sin borrarlo: exige que esté cerrado,
// porque archivar uno abierto lo escondería mientras sigue roto.
func TestArchivarIncidente(t *testing.T) {
    s := abrir(t)
    id, err := s.AbrirIncidente(model.Incidente{
        Sujeto: "service:x", Tipo: "down", Severidad: "critical",
        AbiertoEn: time.Unix(1000, 0), Detalle: "HTTP 502",
    })
    if err != nil {
        t.Fatal(err)
    }

    // Abierto no se puede archivar.
    if err := s.ArchivarIncidente(id, time.Unix(2000, 0)); err != nil {
        t.Fatal(err)
    }
    is, _ := s.UltimosIncidentes(10)
    if len(is) != 1 || is[0].ArchivadoEn != nil {
        t.Fatal("un incidente abierto no tiene que poder archivarse")
    }

    // Cerrado sí.
    if err := s.CerrarIncidente(id, time.Unix(3000, 0)); err != nil {
        t.Fatal(err)
    }
    if err := s.ArchivarIncidente(id, time.Unix(4000, 0)); err != nil {
        t.Fatal(err)
    }
    is, _ = s.UltimosIncidentes(10)
    if len(is) != 1 || is[0].ArchivadoEn == nil {
        t.Fatal("el incidente cerrado tenía que quedar archivado")
    }
    if got := is[0].ArchivadoEn.Unix(); got != 4000 {
        t.Errorf("ArchivadoEn = %d, quería 4000", got)
    }
}
```

- [ ] **Step 2: Ver fallar** — `go test ./internal/store/ -run 'Archivar|UltimaMigracion' -v`
- [ ] **Step 3: Implementar**

Migración nueva al final del slice:

```go
// archived_at es "ya lo vi, sacalo del panel": el incidente no se borra —
// /events lo sigue mostrando como historia— pero deja de ocupar la tabla del
// panel. Solo se archiva uno cerrado; ver ArchivarIncidente.
`ALTER TABLE incidents ADD COLUMN archived_at INTEGER;`,
```

`model.Incidente` gana `ArchivadoEn *time.Time`. Las tres queries de
`consultarIncidentes` agregan `archived_at` al SELECT y el scan un
`sql.NullInt64`. Método nuevo:

```go
// ArchivarIncidente esconde del panel un incidente YA CERRADO. El WHERE exige
// closed_at porque archivar uno abierto lo haría desaparecer mientras sigue
// roto — y no falla si no aplica: archivar dos veces o archivar uno abierto es
// un no-op, no un error.
func (s *Store) ArchivarIncidente(id int64, cuando time.Time) error {
    _, err := s.db.Exec(
        `UPDATE incidents SET archived_at = ? WHERE id = ? AND closed_at IS NOT NULL AND archived_at IS NULL`,
        cuando.Unix(), id)
    return err
}
```

- [ ] **Step 4: Verificar** — `go test ./internal/store/` → PASS
- [ ] **Step 5: Commit** — `feat(store): archived_at en incidents y ArchivarIncidente`

### Task 3: BuscarLogs por conjunto de niveles

**Files:**
- Modify: `internal/logs/nivel.go` (func Conjunto)
- Modify: `internal/store/store.go` (firma BuscarLogs, nivelesDesde → conjunto)
- Modify: `internal/comandos/comandos.go` (interfaz y llamada)
- Modify: `internal/web/panel.go` (interfaz Datos)
- Tests: `internal/logs/nivel_test.go`, `internal/store/store_test.go`

- [ ] **Step 1: Test de logs.Conjunto**

```go
// Conjunto valida lo que viene de la query string: filtra basura, dedup, y con
// nada elegido cae al default de la vista (INFO+WARN+ERROR — todo menos TRACE).
func TestConjunto(t *testing.T) {
    casos := []struct {
        entrada []string
        quiero  []logs.Nivel
    }{
        {nil, []logs.Nivel{logs.Info, logs.Warn, logs.Error}},
        {[]string{"basura"}, []logs.Nivel{logs.Info, logs.Warn, logs.Error}},
        {[]string{"ERROR"}, []logs.Nivel{logs.Error}},
        {[]string{"error", " warn ", "ERROR"}, []logs.Nivel{logs.Warn, logs.Error}},
        {[]string{"TRACE", "INFO", "WARN", "ERROR"}, []logs.Nivel{logs.Trace, logs.Info, logs.Warn, logs.Error}},
    }
    for _, c := range casos {
        if got := logs.Conjunto(c.entrada); !reflect.DeepEqual(got, c.quiero) {
            t.Errorf("Conjunto(%v) = %v, quería %v", c.entrada, got, c.quiero)
        }
    }
}
```

- [ ] **Step 2: Implementar en nivel.go**

```go
// Conjunto convierte los valores repetidos de una query string (?nivel=WARN&
// nivel=ERROR) en el conjunto de niveles a mostrar, en orden fijo y sin
// repetidos. Con nada válido cae al default de la vista: todo menos TRACE.
// Existe porque el filtro dejó de ser "mínimo" y pasó a ser un toggle por
// ítem: WARN apagado con TRACE y ERROR prendidos no se puede decir con un piso.
func Conjunto(ss []string) []Nivel {
    elegidos := map[Nivel]bool{}
    for _, s := range ss {
        n := Nivel(strings.ToUpper(strings.TrimSpace(s)))
        if _, conocido := orden[n]; conocido {
            elegidos[n] = true
        }
    }
    if len(elegidos) == 0 {
        return []Nivel{Info, Warn, Error}
    }
    out := make([]Nivel, 0, len(elegidos))
    for _, n := range []Nivel{Trace, Info, Warn, Error} {
        if elegidos[n] {
            out = append(out, n)
        }
    }
    return out
}
```

- [ ] **Step 3: Cambiar la firma en el store**

`BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int)`.
Adentro, `nivelesDesde` se reemplaza por `logs.Conjunto` (el store ya puede
importar `internal/logs`; no hay ciclo):

```go
if conjunto := logs.Conjunto(niveles); len(conjunto) < 4 {
    q += ` AND COALESCE(n.nivel, 'INFO') IN (?` + strings.Repeat(",?", len(conjunto)-1) + `)`
    for _, n := range conjunto {
        args = append(args, string(n))
    }
}
```

Actualizar los tests del store que llamaban con string, y el test de
`nivelesDesde` si existe (borrarlo: la lógica vive en logs.Conjunto).

- [ ] **Step 4: Propagar la firma**

- `internal/comandos/comandos.go`: interfaz `BuscarLogs(..., niveles []string, ...)`;
  la llamada del /logs del bot pasa `[]string{"INFO", "WARN", "ERROR"}` (mismo
  filtro que tenía con mínimo INFO).
- `internal/web/panel.go`: interfaz `Datos` igual; los tres call sites (vista,
  export, /eventos que pedía "ERROR") pasan slices. Los dobles de test
  (`datosFalsos`, `espia`) cambian de firma.

- [ ] **Step 5: Verificar** — `go test ./...` (suelto, sin pipes) → PASS
- [ ] **Step 6: Commit** — `feat(store): BuscarLogs filtra por conjunto de niveles, no por mínimo`

### Task 4: Fechas completas DD/MM/YYYY

**Files:**
- Modify: `internal/web/panel.go` (func hora), `plantillas/logs.html`, `plantillas/eventos.html`
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Test**

```go
// La hora sola obliga a adivinar el día: con 30 días de rango, "02:00:42"
// puede ser cualquiera de treinta madrugadas. Fecha completa, DD/MM/YYYY.
func TestLasFechasLlevanDiaMesYAnio(t *testing.T) {
    if cuerpo := pedir(t, "/logs").Body.String(); !strings.Contains(cuerpo, "09/08/2026 09:00:00") {
        t.Error("/logs no muestra la fecha completa (esperaba 09/08/2026 09:00:00)")
    }
    if cuerpo := pedir(t, "/events?horas=720").Body.String(); !strings.Contains(cuerpo, "22/08/2026 02:00:31") {
        t.Error("/events no muestra la fecha completa")
    }
    if cuerpo := pedir(t, "/").Body.String(); !strings.Contains(cuerpo, "09/08/2026") {
        t.Error("el panel no muestra la fecha completa en incidentes")
    }
}
```

(la línea falsa de logs es 12:00 UTC del 09/08 → 09:00 en Buenos Aires; en la
tarea 9 la ruta ya es /events — si esta tarea corre antes, usar /eventos y
ajustar al renombrar)

- [ ] **Step 2: Implementar** — tres formatos:
  - `panel.go` func `hora`: `"02/01 15:04"` → `"02/01/2006 15:04"`
  - `logs.html`: `.Format "15:04:05"` → `.Format "02/01/2006 15:04:05"`, y la
    grilla `.l` pasa de `5.5rem` a `10rem` en la primera columna
  - `eventos.html`: `.Format "02/01 15:04:05"` → `.Format "02/01/2006 15:04:05"`
- [ ] **Step 3: Verificar** — `go test ./internal/web/` → PASS
- [ ] **Step 4: Commit** — `feat(web): fecha completa DD/MM/YYYY junto a cada hora`

### Task 5: Orden por defecto por estado

**Files:**
- Modify: `internal/web/panel.go` (handler `GET /{$}`)
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Test** (a `datosFalsos` se le agregan filas para que el orden
  se vea: un probe OK lento, un container exited y uno unhealthy)

```go
// El orden por defecto pone lo roto arriba: para eso existe el panel. Después,
// servicios por latencia y containers por RAM, los más pesados primero.
func TestServiciosYContainersOrdenanPorEstado(t *testing.T) {
    cuerpo := pedir(t, "/").Body.String()

    caido := strings.Index(cuerpo, ">sitio<")
    lento := strings.Index(cuerpo, ">lento<")
    rapido := strings.Index(cuerpo, ">comm-tool<")
    if !(caido < lento && lento < rapido) {
        t.Errorf("servicios: quería caído(%d) < lento(%d) < rápido(%d)", caido, lento, rapido)
    }

    muerto := strings.Index(cuerpo, "container=muerto")
    enfermo := strings.Index(cuerpo, "container=enfermo")
    pesado := strings.Index(cuerpo, "container=supabase-db")
    liviano := strings.Index(cuerpo, "container=comm-tool")
    if !(muerto < pesado && enfermo < pesado && pesado < liviano) {
        t.Errorf("containers: los rotos van arriba y después por RAM: muerto=%d enfermo=%d pesado=%d liviano=%d",
            muerto, enfermo, pesado, liviano)
    }
}
```

- [ ] **Step 2: Implementar** en el handler, después de leer probes y containers:

```go
// Lo roto arriba, que es para lo que uno abre el panel; entre lo sano, el
// servicio más lento y el container más pesado primero. El orden por columna
// del navegador sigue disponible para todo lo demás.
sort.SliceStable(v.Probes, func(i, j int) bool {
    if v.Probes[i].OK != v.Probes[j].OK {
        return !v.Probes[i].OK
    }
    return v.Probes[i].Latencia > v.Probes[j].Latencia
})
mal := func(c model.ContainerSample) bool {
    return c.State != "running" || c.Health == "unhealthy"
}
sort.SliceStable(v.Containers, func(i, j int) bool {
    if mal(v.Containers[i]) != mal(v.Containers[j]) {
        return mal(v.Containers[i])
    }
    return v.Containers[i].MemBytes > v.Containers[j].MemBytes
})
```

- [ ] **Step 3: Verificar y commit** — `feat(web): servicios y containers ordenados por estado por defecto`

### Task 6: Toggles por ítem para nivel y severidad

**Files:**
- Modify: `internal/web/panel.go` (nivelesDe, severidadesDe, structs de vista)
- Modify: `internal/web/eventos.go` (armarNovedades por conjunto)
- Modify: `plantillas/logs.html`, `plantillas/eventos.html`
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Tests**

```go
// El filtro es un toggle por ítem, no un "mínimo": WARN apagado con ERROR
// prendido no se puede decir con un piso.
func TestElFiltroDeNivelEsPorConjunto(t *testing.T) {
    var visto []string
    h := web.NuevoPanel(espiaNiveles{cb: func(ns []string) { visto = ns }}, zonaDePrueba)
    w := httptest.NewRecorder()
    h.ServeHTTP(w, httptest.NewRequest("GET", "/logs?nivel=TRACE&nivel=ERROR", nil))
    if !reflect.DeepEqual(visto, []string{"TRACE", "ERROR"}) {
        t.Errorf("niveles = %v, quería [TRACE ERROR]", visto)
    }
    // Y la vista pinta los cuatro checkboxes con los elegidos marcados.
    cuerpo := w.Body.String()
    if !strings.Contains(cuerpo, `value="TRACE" checked`) || strings.Contains(cuerpo, `value="INFO" checked`) {
        t.Error("los toggles no reflejan la selección")
    }
}

func TestVistaEventosFiltraPorConjunto(t *testing.T) {
    // sev=critical significa SOLO critical: el warning del log no va.
    cuerpo := pedir(t, "/events?horas=720&sev=critical").Body.String()
    if strings.Contains(cuerpo, "conexion rechazada") {
        t.Error("con solo critical no va un error de log, que es warning")
    }
    if !strings.Contains(cuerpo, "el servidor se reinició") {
        t.Error("el reboot es critical y tenía que estar")
    }
}
```

- [ ] **Step 2: Implementar**

`panel.go`:

```go
// nivelesDe lee los toggles. Param repetido (?nivel=WARN&nivel=ERROR); sin
// ninguno vale el default de la vista, que es todo menos TRACE.
func nivelesDe(r *http.Request) []string {
    conjunto := logs.Conjunto(r.URL.Query()["nivel"])
    out := make([]string, len(conjunto))
    for i, n := range conjunto {
        out[i] = string(n)
    }
    return out
}
```

La vista pasa `Niveles []struct{ Valor string; Activo bool }` (los 4, con
Activo si están en el conjunto). En `eventos.go`, `severidadesValidas` con la
misma forma (default las 3) y `armarNovedades` filtra por membresía:

```go
// severidadesValidas normaliza los toggles de /events. Sin ninguno, todo:
// el default de la vista es no esconder nada.
func severidadesValidas(ss []string) []string {
    elegidas := map[string]bool{}
    for _, s := range ss {
        if _, ok := pesoSeveridad[s]; ok {
            elegidas[s] = true
        }
    }
    if len(elegidas) == 0 {
        return []string{"info", "warning", "critical"}
    }
    out := make([]string, 0, len(elegidas))
    for _, s := range []string{"info", "warning", "critical"} {
        if elegidas[s] {
            out = append(out, s)
        }
    }
    return out
}
```

Plantillas: el `<select name="nivel">` y el `<select name="sev">` se vuelven
pills de checkbox:

```html
<span class="toggles">
  {{ range .Niveles }}
  <label class="toggle"><input type="checkbox" name="nivel" value="{{ .Valor }}"{{ if .Activo }} checked{{ end }}><span>{{ .Valor }}</span></label>
  {{ end }}
</span>
```

con CSS de pill (input oculto, `:checked + span` con borde y color).

- [ ] **Step 3: Verificar y commit** — `feat(web): filtros de nivel y severidad como toggles por ítem`

### Task 7: Sin carteles de ayuda y filtros que se aplican solos

**Files:**
- Modify: `plantillas/logs.html`, `plantillas/eventos.html`
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Test**

```go
// Los carteles de ayuda se van (pedido del 25/08) y los filtros se aplican
// solos: no hay botón "buscar" que apretar.
func TestSinCartelDeAyudaYSinBotonBuscar(t *testing.T) {
    for _, ruta := range []string{"/logs", "/events"} {
        cuerpo := pedir(t, ruta).Body.String()
        if strings.Contains(cuerpo, "La búsqueda es por palabra completa") ||
            strings.Contains(cuerpo, "Todo lo que pasó, en orden") {
            t.Errorf("%s todavía muestra el cartel de ayuda", ruta)
        }
        if strings.Contains(cuerpo, ">buscar</button>") || strings.Contains(cuerpo, ">ver</button>") {
            t.Errorf("%s todavía tiene botón de submit manual", ruta)
        }
        if !strings.Contains(cuerpo, "form.submit()") {
            t.Errorf("%s no auto-aplica los filtros", ruta)
        }
    }
}
```

- [ ] **Step 2: Implementar**

- Borrar los `<div class="ayuda">` de logs.html y eventos.html (y su CSS).
- Sacar `<button type="submit">buscar</button>` y `>ver</button>`. En logs.html
  poner primero `<button hidden tabindex="-1" aria-hidden="true"></button>`:
  sin él, Enter en el campo de texto dispararía el primer botón visible, que es
  "exportar" con `formaction`, y bajaría un archivo en vez de buscar.
- Script al final de las dos plantillas:

```html
<script>
  // Los filtros se aplican solos: cambiar un toggle, un select o una fecha
  // recarga con la selección nueva. El texto espera 700 ms desde la última
  // tecla — recargar en cada pulsación haría imposible escribir.
  const form = document.querySelector('form')
  for (const el of form.querySelectorAll('select, input[type=checkbox], input[type=datetime-local]'))
    el.addEventListener('change', () => form.submit())
  const q = form.querySelector('input[name=q]')
  if (q) {
    let espera
    q.addEventListener('input', () => { clearTimeout(espera); espera = setTimeout(() => form.submit(), 700) })
    // Tras la recarga el foco vuelve al final de lo tipeado, no al principio.
    q.setSelectionRange(q.value.length, q.value.length)
  }
</script>
```

- [ ] **Step 3: Verificar y commit** — `feat(web): filtros que se aplican solos y sin carteles de ayuda`

### Task 8: Resolver y archivar incidentes desde el panel

**Files:**
- Modify: `internal/web/panel.go` (interfaz Datos + 2 handlers POST + filtro de archivados)
- Modify: `plantillas/panel.html` (columna de acciones)
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Tests**

```go
// Resolver cierra por el mismo camino que el motor (y por la cola derivada,
// eso manda el aviso de cierre — deseado). Archivar lo saca del panel.
func TestResolverYArchivarIncidentes(t *testing.T) {
    var cerrado, archivado int64
    d := espiaIncidentes{cerrar: func(id int64) { cerrado = id }, archivar: func(id int64) { archivado = id }}
    h := web.NuevoPanel(d, zonaDePrueba)

    w := httptest.NewRecorder()
    h.ServeHTTP(w, httptest.NewRequest("POST", "/incidents/7/resolve", nil))
    if w.Code != 303 || cerrado != 7 {
        t.Errorf("resolve: código=%d cerrado=%d, quería 303 y 7", w.Code, cerrado)
    }

    w = httptest.NewRecorder()
    h.ServeHTTP(w, httptest.NewRequest("POST", "/incidents/9/archive", nil))
    if w.Code != 303 || archivado != 9 {
        t.Errorf("archive: código=%d archivado=%d, quería 303 y 9", w.Code, archivado)
    }
}

// El panel esconde los archivados; /events los sigue mostrando: es historia.
func TestElPanelEscondeArchivadosYEventosNo(t *testing.T) { ... con datosFalsos que
    devuelven un incidente archivado: no aparece en "/" y sí en "/events" ... }

// El botón que corresponde: abierto → resolver; cerrado → archivar.
func TestLosBotonesDeIncidentes(t *testing.T) { ... "/" contiene
    /incidents/1/resolve para el abierto y /incidents/2/archive para el cerrado ... }
```

- [ ] **Step 2: Implementar**

Interfaz `Datos` suma:

```go
CerrarIncidente(id int64, cuando time.Time) error
ArchivarIncidente(id int64, cuando time.Time) error
```

(el store ya tiene los dos). Handlers:

```go
// Las acciones son POST y redirigen al panel: un GET que muta estado termina
// prefetcheado por un navegador. Resolver reusa CerrarIncidente — el mismo
// camino que el motor— así el aviso de cierre sale por la cola derivada, y si
// el sujeto sigue mal el motor lo reabre en el próximo tick, que es la verdad.
mux.HandleFunc("POST /incidents/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
    id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
    if err != nil {
        http.Error(w, "id inválido", http.StatusBadRequest)
        return
    }
    if err := d.CerrarIncidente(id, time.Now()); err != nil {
        http.Error(w, "no se pudo resolver", http.StatusInternalServerError)
        return
    }
    http.Redirect(w, r, "/", http.StatusSeeOther)
})
```

(archive igual con `d.ArchivarIncidente`). En el handler del panel, después de
`UltimosIncidentes(50)` se filtran `ArchivadoEn != nil` y se corta en 15. En
`panel.html`, columna nueva:

```html
<td>
  {{ if not .CerradoEn }}
  <form method="post" action="/incidents/{{ .ID }}/resolve"><button>{{ t "resolver" }}</button></form>
  {{ else }}
  <form method="post" action="/incidents/{{ .ID }}/archive"><button>{{ t "archivar" }}</button></form>
  {{ end }}
</td>
```

(hasta la tarea 9, los textos van literales "resolver"/"archivar"; la 9 los
pasa por `t`)

- [ ] **Step 3: Verificar y commit** — `feat(web): resolver y archivar incidentes desde el panel`

### Task 9: Panel bilingüe y ruta /events

**Files:**
- Create: `internal/web/idiomas.go` (tabla de textos + tr + idiomaDe)
- Modify: `internal/web/panel.go` (plantillas por idioma, /events, redirect /eventos)
- Modify: `internal/web/eventos.go` (títulos por idioma)
- Modify: las 5 plantillas (todo texto visible pasa por `{{ t "clave" }}`)
- Test: `internal/web/panel_test.go`

- [ ] **Step 1: Tests**

```go
// El panel habla español por defecto; ?lang=en lo cambia y lo recuerda en una
// cookie, así el auto-reload de 60 s no lo vuelve a castellano.
func TestElPanelEsBilingue(t *testing.T) {
    if cuerpo := pedir(t, "/").Body.String(); !strings.Contains(cuerpo, "Servicios") {
        t.Error("sin elegir nada el panel habla español")
    }
    rec := pedir(t, "/?lang=en")
    if !strings.Contains(rec.Body.String(), "Services") {
        t.Error("?lang=en no cambia el idioma")
    }
    if !strings.Contains(rec.Header().Get("Set-Cookie"), "lang=en") {
        t.Error("?lang=en no persiste en la cookie")
    }
    // Y la cookie sola alcanza.
    req := httptest.NewRequest("GET", "/", nil)
    req.AddCookie(&http.Cookie{Name: "lang", Value: "en"})
    w := httptest.NewRecorder()
    web.NuevoPanel(datosFalsos{}, zonaDePrueba).ServeHTTP(w, req)
    if !strings.Contains(w.Body.String(), "Services") {
        t.Error("la cookie lang=en no aplica")
    }
}

// /eventos era la única ruta en castellano; pasa a /events con redirect para
// los marcadores viejos.
func TestEventosRedirigeAEvents(t *testing.T) {
    rec := pedir(t, "/eventos?horas=720")
    if rec.Code != 301 || !strings.Contains(rec.Header().Get("Location"), "/events?horas=720") {
        t.Errorf("código=%d Location=%q, quería 301 a /events", rec.Code, rec.Header().Get("Location"))
    }
}
```

- [ ] **Step 2: idiomas.go completo**

```go
package web

import "net/http"

// texto es un rótulo del panel en los dos idiomas. El español es el original.
type texto struct{ ES, EN string }

// textos: la clave es lo que las plantillas escriben en {{ t "clave" }}.
var textos = map[string]texto{
    "nav-eventos": {"eventos", "events"},
    // ... (tabla completa: nav, tarjetas, h2, columnas, estados, botones,
    //      gráficos, rangos "rango-<horas>", labels de logs, títulos de
    //      novedades "se-abrio"/"se-cerro"/"reboot"/..., orígenes, sujetos
    //      legibles, truncado, export)
}

// tr traduce una clave. Una clave sin texto vuelve tal cual: un rótulo raro a
// la vista es mejor que uno desaparecido en silencio.
func tr(idioma, clave string) string {
    t, ok := textos[clave]
    if !ok {
        return clave
    }
    if idioma == "en" {
        return t.EN
    }
    return t.ES
}

// idiomaDe resuelve el idioma del request: el query param manda (y se guarda
// en la cookie, porque el panel se recarga solo cada 60 s), después la cookie,
// y el default es español.
func idiomaDe(w http.ResponseWriter, r *http.Request) string {
    if l := r.URL.Query().Get("lang"); l == "es" || l == "en" {
        http.SetCookie(w, &http.Cookie{Name: "lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600})
        return l
    }
    if c, err := r.Cookie("lang"); err == nil && (c.Value == "es" || c.Value == "en") {
        return c.Value
    }
    return "es"
}
```

- [ ] **Step 3: Plantillas por idioma en panel.go**

```go
// Un set de plantillas por idioma, parseados al arrancar: t queda cerrada
// sobre el idioma y las plantillas se escriben igual que antes. Parsear en
// cada request sería tirar el trabajo 1440 veces por día por pestaña abierta.
func plantillasCon(idioma string) *template.Template {
    return template.Must(template.New("panel").Funcs(template.FuncMap{
        "pct": pct, "gib": gib,
        "mib":  func(b uint64) float64 { return float64(b) / (1024 * 1024) },
        "hora": func(t time.Time, loc *time.Location) string { return enZona(t, loc).Format("02/01/2006 15:04") },
        "en":   enZona,
        "t":    func(clave string) string { return tr(idioma, clave) },
        "lang": func() string { return idioma },
    }).ParseFS(plantillas, "plantillas/nav.html", "plantillas/panel.html",
        "plantillas/logs.html", "plantillas/tail.html", "plantillas/eventos.html"))
}

var plantillasIdioma = map[string]*template.Template{
    "es": plantillasCon("es"), "en": plantillasCon("en"),
}
```

Cada handler arranca con `idioma := idiomaDe(w, r)` y usa
`plantillasIdioma[idioma]`. El truncado y los headers del export se arman con
`tr(idioma, ...)`. `armarNovedades`, `tituloDeEvento` y `NombreLegible` reciben
`idioma`.

- [ ] **Step 4: Rutas**

```go
mux.HandleFunc("GET /events", ...)      // el handler que era /eventos
mux.HandleFunc("GET /eventos", func(w http.ResponseWriter, r *http.Request) {
    destino := "/events"
    if r.URL.RawQuery != "" {
        destino += "?" + r.URL.RawQuery
    }
    http.Redirect(w, r, destino, http.StatusMovedPermanently)
})
```

`nav.Activo` pasa de "eventos" a "events"; nav.html linkea `/events` y suma el
toggle de idioma a la derecha:

```html
<a class="idioma" href="#" onclick="const u=new URL(location);u.searchParams.set('lang','{{ if eq lang "en" }}es{{ else }}en{{ end }}');location=u;return false">{{ if eq lang "en" }}ES{{ else }}EN{{ end }}</a>
```

(con `.nav .idioma { margin-left: auto; }` — JS y no href fijo para no perder
la query actual: q, container, desde/hasta)

- [ ] **Step 5: Pasar todos los textos visibles de las 5 plantillas por `t`**,
  incluidos los títulos de los gráficos en el JS del panel
  (`titulo: '{{ t "g-cpu" }}'`), y `<html lang="{{ lang }}">`. Actualizar los
  tests viejos que referencian `/eventos` a `/events`.
- [ ] **Step 6: Verificar** — `go test ./...` → PASS. Además `make vet`.
- [ ] **Step 7: Commit** — `feat(web): panel bilingüe es/en y ruta /events`

### Task 10: Cierre — main.go, docs y verificación real

**Files:**
- Modify: `cmd/server-status/main.go` (si la interfaz Datos requiere algo — el
  store ya cumple, verificar que compila)
- Modify: `CLAUDE.md` (estado + gotcha del cambio de escala de CPU)
- Test: suite completa

- [ ] **Step 1:** `go build ./...` y `make test` (comandos sueltos, sin pipes)
- [ ] **Step 2:** Levantar el binario local con una config mínima apuntando a
  una base de prueba y mirar el panel en el navegador (validación visual la
  hace Juan; acá se verifica que renderiza sin panics y que el HTML trae lo
  esperado con curl)
- [ ] **Step 3:** CLAUDE.md: párrafo de la tanda del 25/08 en "Estado", y en
  gotchas la nota: el `cpu_pct` de `container_samples` anterior al 25/08/2026
  está en escala docker-stats (100 % = un core); desde entonces es % de la
  máquina.
- [ ] **Step 4: Commit** — `docs: la tanda de UI del 25/08 en CLAUDE.md`

## Self-review

- Pedido 1 → T4 ✓ · 2 → T3+T6 ✓ · 3 → T7 ✓ · 4 → T9 ✓ · 5 → T5 ✓ ·
  6 → T1+T5 ✓ · 7 → T2+T8 ✓ · 8 → T7 ✓
- Firmas consistentes: `BuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int)`
  en store, comandos y web. `ArchivarIncidente(id int64, cuando time.Time)`.
  `tr(idioma, clave string) string`.
- Orden de tareas pensado para que cada una compile y teste sola; la 9 (i18n)
  va al final porque toca los textos que las anteriores agregan.
