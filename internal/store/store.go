// Package store guarda las muestras en SQLite.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juanandresdavila/server-status/internal/model"

	_ "modernc.org/sqlite" // driver puro Go: sin cgo, invariante 7 del spec
)

// migraciones se aplican en orden y nunca se editan una vez aplicadas.
// Para cambiar el esquema se agrega una nueva al final.
var migraciones = []string{
	`CREATE TABLE host_samples (
		ts               INTEGER PRIMARY KEY,
		cpu_pct_avg      REAL    NOT NULL,
		cpu_pct_max      REAL    NOT NULL,
		load1            REAL    NOT NULL,
		load5            REAL    NOT NULL,
		load15           REAL    NOT NULL,
		mem_used_bytes   INTEGER NOT NULL,
		mem_total_bytes  INTEGER NOT NULL,
		swap_used_bytes  INTEGER NOT NULL,
		swap_total_bytes INTEGER NOT NULL,
		disk_used_bytes  INTEGER NOT NULL,
		disk_total_bytes INTEGER NOT NULL,
		net_rx_bytes     INTEGER NOT NULL,
		net_tx_bytes     INTEGER NOT NULL,
		uptime_seconds   INTEGER NOT NULL
	) STRICT;`,

	`CREATE TABLE container_samples (
		ts        INTEGER NOT NULL,
		name      TEXT    NOT NULL,
		state     TEXT    NOT NULL,
		health    TEXT    NOT NULL,
		restarts  INTEGER NOT NULL,
		cpu_pct   REAL    NOT NULL,
		mem_bytes INTEGER NOT NULL,
		PRIMARY KEY (ts, name)
	) STRICT;`,

	`CREATE TABLE probe_results (
		ts          INTEGER NOT NULL,
		service     TEXT    NOT NULL,
		ok          INTEGER NOT NULL,
		status_code INTEGER NOT NULL,
		latency_ms  INTEGER NOT NULL,
		error       TEXT    NOT NULL,
		PRIMARY KEY (ts, service)
	) STRICT;

	CREATE TABLE incidents (
		id        INTEGER PRIMARY KEY,
		subject   TEXT    NOT NULL,
		kind      TEXT    NOT NULL,
		severity  TEXT    NOT NULL,
		opened_at INTEGER NOT NULL,
		closed_at INTEGER,
		detail    TEXT    NOT NULL
	) STRICT;

	CREATE UNIQUE INDEX incidentes_abierto_unico
		ON incidents(subject) WHERE closed_at IS NULL;`,

	`CREATE TABLE notifications (
		delivery_id TEXT    PRIMARY KEY,
		sent_at     INTEGER NOT NULL,
		via         TEXT    NOT NULL,
		error       TEXT    NOT NULL
	) STRICT;`,

	// El cursor se guarda en NANOSEGUNDOS: con segundos, toda línea con
	// fracción se re-ingería en cada tick.
	`CREATE VIRTUAL TABLE logs USING fts5(
		linea,
		container UNINDEXED,
		stream    UNINDEXED,
		ts        UNINDEXED
	);

	CREATE TABLE log_cursors (
		container TEXT    PRIMARY KEY,
		ultimo_ts INTEGER NOT NULL
	) STRICT;`,

	// Los cursores de la 0005 se guardaban en SEGUNDOS y ahora se leen en
	// NANOSEGUNDOS: interpretarlos con la unidad nueva daría 1970 y traería
	// toda la historia que Docker conserve, de golpe. Se limpian, y cada
	// container vuelve a arrancar desde ahora.
	`DELETE FROM log_cursors;`,

	`CREATE TABLE comandos_procesados (
		delivery_id  TEXT    PRIMARY KEY,
		procesado_en INTEGER NOT NULL
	) STRICT;

	CREATE TABLE silencio (
		id    INTEGER PRIMARY KEY CHECK (id = 1),
		hasta INTEGER NOT NULL
	) STRICT;`,

	// eventos son HECHOS PUNTUALES: un reinicio no se "cierra". Por eso no
	// tienen cerrado_en y no chocan con incidentes_abierto_unico, que es lo que
	// pasaría si se los modelara como incidentes que abren y cierran al toque.
	//
	// Existen porque el motor de reglas solo sabe de estados sostenidos —tres
	// muestras malas seguidas, o un umbral aguantado diez minutos— y un reinicio
	// dura segundos. El del 22/08/2026 duró 18 y no lo vio nadie.
	`CREATE TABLE eventos (
		id          INTEGER PRIMARY KEY,
		tipo        TEXT    NOT NULL,
		sujeto      TEXT    NOT NULL,
		severidad   TEXT    NOT NULL,
		ocurrido_en INTEGER NOT NULL,
		detalle     TEXT    NOT NULL
	) STRICT;

	CREATE INDEX eventos_por_fecha ON eventos(ocurrido_en);`,

	// El nivel va en una tabla LATERAL y no como columna de logs porque logs es
	// FTS5: agregarle una columna obliga a recrear la tabla y reindexar el texto
	// de 802 200 filas (191 MB) con el proceso bloqueado en Open. Y de paso
	// corresponde: el nivel es un filtro, no texto buscable.
	//
	// backfill_niveles guarda el techo —lo que ya existía al migrar— y por dónde
	// va la pasada. Con un techo fijo la pasada es O(n) y resumible: sin él
	// habría que preguntar en cada lote qué filas faltan, y eso vuelve a
	// escanear desde el principio cada vez.
	`CREATE TABLE log_niveles (
		rowid INTEGER PRIMARY KEY,
		nivel TEXT NOT NULL
	) STRICT;

	CREATE TABLE backfill_niveles (
		id          INTEGER PRIMARY KEY CHECK (id = 1),
		hasta_rowid INTEGER NOT NULL,
		ultimo      INTEGER NOT NULL
	) STRICT;

	INSERT INTO backfill_niveles (id, hasta_rowid, ultimo)
	SELECT 1, COALESCE(MAX(rowid), 0), 0 FROM logs;`,

	// RestartCount NO sirve para saber si un container se reinició: verificado
	// en el VPS después del reboot del 22/08, los 21 containers arrancaron a las
	// 05:00:38 y quedaron en restarts=0. Un arranque con el host no lo
	// incrementa y una recreación lo resetea. StartedAt sí se mueve siempre.
	`ALTER TABLE container_samples ADD COLUMN started_at INTEGER NOT NULL DEFAULT 0;`,
}

type Store struct{ db *sql.DB }

func Open(ruta string) (*Store, error) {
	dsn := ruta + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Un solo proceso y un solo escritor: con una conexión no hay SQLITE_BUSY
	// que resolver, y el costo es nulo para el volumen que maneja esto.
	db.SetMaxOpenConns(1)

	if err := migrar(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

func migrar(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	) STRICT;`); err != nil {
		return fmt.Errorf("crear schema_migrations: %w", err)
	}

	var aplicadas int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&aplicadas); err != nil {
		return err
	}
	// Una base más nueva que el binario significa que alguien deployó para
	// atrás. Mejor negarse que escribir con un esquema que no se conoce.
	if aplicadas > len(migraciones) {
		return fmt.Errorf("la base está en la migración %d y el binario conoce %d", aplicadas, len(migraciones))
	}

	for i := aplicadas; i < len(migraciones); i++ {
		version := i + 1
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migraciones[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migración %d: %w", version, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, unixepoch())`,
			version,
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("registrar migración %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// InsertHostSample guarda una muestra. El ts se trunca al minuto, así que dos
// muestras del mismo minuto se pisan en vez de duplicarse.
func (s *Store) InsertHostSample(m model.HostSample) error {
	_, err := s.db.Exec(`
		INSERT INTO host_samples (
			ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
			mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
			disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
			uptime_seconds
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(ts) DO UPDATE SET
			cpu_pct_avg=excluded.cpu_pct_avg, cpu_pct_max=excluded.cpu_pct_max,
			load1=excluded.load1, load5=excluded.load5, load15=excluded.load15,
			mem_used_bytes=excluded.mem_used_bytes, mem_total_bytes=excluded.mem_total_bytes,
			swap_used_bytes=excluded.swap_used_bytes, swap_total_bytes=excluded.swap_total_bytes,
			disk_used_bytes=excluded.disk_used_bytes, disk_total_bytes=excluded.disk_total_bytes,
			net_rx_bytes=excluded.net_rx_bytes, net_tx_bytes=excluded.net_tx_bytes,
			uptime_seconds=excluded.uptime_seconds`,
		m.TS.Truncate(time.Minute).Unix(),
		m.CPUPctAvg, m.CPUPctMax,
		m.Load1, m.Load5, m.Load15,
		int64(m.MemUsedBytes), int64(m.MemTotalBytes),
		int64(m.SwapUsedBytes), int64(m.SwapTotalBytes),
		int64(m.DiskUsedBytes), int64(m.DiskTotalBytes),
		int64(m.NetRxBytes), int64(m.NetTxBytes),
		int64(m.Uptime.Seconds()),
	)
	return err
}

// UltimasHostSamples devuelve las n más recientes, de la más nueva a la más vieja.
func (s *Store) UltimasHostSamples(n int) ([]model.HostSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
		       mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
		       disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
		       uptime_seconds
		FROM host_samples ORDER BY ts DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer filas.Close()
	return escanearHostSamples(filas)
}

// SerieHost devuelve las muestras de un rango, de la más VIEJA a la más nueva.
// El orden importa: un gráfico se dibuja hacia adelante en el tiempo.
func (s *Store) SerieHost(desde, hasta time.Time) ([]model.HostSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, cpu_pct_avg, cpu_pct_max, load1, load5, load15,
		       mem_used_bytes, mem_total_bytes, swap_used_bytes, swap_total_bytes,
		       disk_used_bytes, disk_total_bytes, net_rx_bytes, net_tx_bytes,
		       uptime_seconds
		FROM host_samples WHERE ts >= ? AND ts <= ? ORDER BY ts ASC`,
		desde.Unix(), hasta.Unix())
	if err != nil {
		return nil, err
	}
	defer filas.Close()
	return escanearHostSamples(filas)
}

// escanearHostSamples lo comparten las dos consultas: son quince columnas y
// duplicar el escaneo garantiza que un día diverjan.
func escanearHostSamples(filas *sql.Rows) ([]model.HostSample, error) {
	var out []model.HostSample
	for filas.Next() {
		var (
			m                                      model.HostSample
			ts, uptime                             int64
			memU, memT, swU, swT, dkU, dkT, rx, tx int64
		)
		if err := filas.Scan(
			&ts, &m.CPUPctAvg, &m.CPUPctMax, &m.Load1, &m.Load5, &m.Load15,
			&memU, &memT, &swU, &swT, &dkU, &dkT, &rx, &tx, &uptime,
		); err != nil {
			return nil, err
		}
		m.TS = time.Unix(ts, 0).UTC()
		m.MemUsedBytes, m.MemTotalBytes = uint64(memU), uint64(memT)
		m.SwapUsedBytes, m.SwapTotalBytes = uint64(swU), uint64(swT)
		m.DiskUsedBytes, m.DiskTotalBytes = uint64(dkU), uint64(dkT)
		m.NetRxBytes, m.NetTxBytes = uint64(rx), uint64(tx)
		m.Uptime = time.Duration(uptime) * time.Second
		out = append(out, m)
	}
	return out, filas.Err()
}

// InsertContainerSamples guarda la foto de un minuto, toda en una transacción:
// media foto es peor que ninguna.
func (s *Store) InsertContainerSamples(ms []model.ContainerSample) error {
	if len(ms) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO container_samples (ts, name, state, health, restarts, cpu_pct, mem_bytes, started_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(ts, name) DO UPDATE SET
			state=excluded.state, health=excluded.health, restarts=excluded.restarts,
			cpu_pct=excluded.cpu_pct, mem_bytes=excluded.mem_bytes,
			started_at=excluded.started_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range ms {
		// StartedAt cero se guarda como 0 y no como el unix de 1970: es la
		// marca de "no se sabe", y el detector de reinicios la ignora.
		var arranco int64
		if !m.StartedAt.IsZero() {
			arranco = m.StartedAt.Unix()
		}
		if _, err := stmt.Exec(
			m.TS.Truncate(time.Minute).Unix(), m.Name, m.State, m.Health,
			m.Restarts, m.CPUPct, int64(m.MemBytes), arranco,
		); err != nil {
			return fmt.Errorf("insertar %s: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// UltimoEstadoContainers devuelve la foto del minuto más reciente.
func (s *Store) UltimoEstadoContainers() ([]model.ContainerSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, name, state, health, restarts, cpu_pct, mem_bytes, started_at
		FROM container_samples
		WHERE ts = (SELECT MAX(ts) FROM container_samples)
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.ContainerSample
	for filas.Next() {
		var (
			c       model.ContainerSample
			ts      int64
			mem     int64
			arranco int64
		)
		if err := filas.Scan(&ts, &c.Name, &c.State, &c.Health, &c.Restarts, &c.CPUPct, &mem, &arranco); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0).UTC()
		c.MemBytes = uint64(mem)
		// started_at en 0 es "no se sabe": las filas anteriores a la migración
		// 10 no lo tienen. Se deja el cero de time.Time para que el detector
		// las ignore en vez de leerlas como un arranque en 1970.
		if arranco > 0 {
			c.StartedAt = time.Unix(arranco, 0).UTC()
		}
		out = append(out, c)
	}
	return out, filas.Err()
}

// AbrirIncidente falla si ya hay uno abierto para el mismo sujeto — lo impide
// incidentes_abierto_unico, y esa negativa es una feature.
func (s *Store) AbrirIncidente(i model.Incidente) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO incidents (subject, kind, severity, opened_at, closed_at, detail)
		VALUES (?,?,?,?,NULL,?)`,
		i.Sujeto, i.Tipo, i.Severidad, i.AbiertoEn.Unix(), i.Detalle)
	if err != nil {
		return 0, fmt.Errorf("abrir incidente de %s: %w", i.Sujeto, err)
	}
	return res.LastInsertId()
}

func (s *Store) CerrarIncidente(id int64, cuando time.Time) error {
	_, err := s.db.Exec(
		`UPDATE incidents SET closed_at = ? WHERE id = ? AND closed_at IS NULL`,
		cuando.Unix(), id)
	return err
}

func (s *Store) IncidentesAbiertos() ([]model.Incidente, error) {
	return s.consultarIncidentes(`
		SELECT id, subject, kind, severity, opened_at, closed_at, detail
		FROM incidents WHERE closed_at IS NULL ORDER BY opened_at`)
}

func (s *Store) UltimosIncidentes(n int) ([]model.Incidente, error) {
	return s.consultarIncidentes(`
		SELECT id, subject, kind, severity, opened_at, closed_at, detail
		FROM incidents ORDER BY opened_at DESC LIMIT ` + strconv.Itoa(n))
}

func (s *Store) consultarIncidentes(q string) ([]model.Incidente, error) {
	filas, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.Incidente
	for filas.Next() {
		var (
			i       model.Incidente
			abierto int64
			cerrado sql.NullInt64
		)
		if err := filas.Scan(&i.ID, &i.Sujeto, &i.Tipo, &i.Severidad, &abierto, &cerrado, &i.Detalle); err != nil {
			return nil, err
		}
		i.AbiertoEn = time.Unix(abierto, 0).UTC()
		if cerrado.Valid {
			t := time.Unix(cerrado.Int64, 0).UTC()
			i.CerradoEn = &t
		}
		out = append(out, i)
	}
	return out, filas.Err()
}

func (s *Store) InsertProbeResults(rs []model.ProbeResult) error {
	if len(rs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO probe_results (ts, service, ok, status_code, latency_ms, error)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(ts, service) DO UPDATE SET
			ok=excluded.ok, status_code=excluded.status_code,
			latency_ms=excluded.latency_ms, error=excluded.error`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, r := range rs {
		ok := 0
		if r.OK {
			ok = 1
		}
		if _, err := stmt.Exec(
			r.TS.Truncate(time.Minute).Unix(), r.Servicio, ok,
			r.StatusCode, r.Latencia.Milliseconds(), r.Error,
		); err != nil {
			return fmt.Errorf("insertar probe de %s: %w", r.Servicio, err)
		}
	}
	return tx.Commit()
}

func (s *Store) UltimoEstadoProbes() ([]model.ProbeResult, error) {
	filas, err := s.db.Query(`
		SELECT ts, service, ok, status_code, latency_ms, error
		FROM probe_results
		WHERE ts = (SELECT MAX(ts) FROM probe_results)
		ORDER BY service`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.ProbeResult
	for filas.Next() {
		var (
			r      model.ProbeResult
			ts, ms int64
			ok     int
		)
		if err := filas.Scan(&ts, &r.Servicio, &ok, &r.StatusCode, &ms, &r.Error); err != nil {
			return nil, err
		}
		r.TS = time.Unix(ts, 0).UTC()
		r.OK = ok == 1
		r.Latencia = time.Duration(ms) * time.Millisecond
		out = append(out, r)
	}
	return out, filas.Err()
}

// MarcarEnviado es idempotente a propósito: el proceso puede morirse entre
// mandar el mensaje y registrar que lo mandó, y el reintento tiene que poder
// escribir sin explotar.
func (s *Store) MarcarEnviado(deliveryID string, cuando time.Time, via, errMsg string) error {
	_, err := s.db.Exec(`
		INSERT INTO notifications (delivery_id, sent_at, via, error)
		VALUES (?,?,?,?)
		ON CONFLICT(delivery_id) DO NOTHING`,
		deliveryID, cuando.Unix(), via, errMsg)
	return err
}

// YaEnviado dice si un aviso ya salió. Se consulta ANTES de mandar: sin eso,
// reiniciar el servicio remanda el resumen del día, cuyo delivery id es la
// fecha. Por comm-tool lo taparía la idempotencyKey; por el camino directo no.
func (s *Store) YaEnviado(deliveryID string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM notifications WHERE delivery_id = ?`, deliveryID).Scan(&n)
	return n > 0, err
}

// AvisosPendientes deriva la cola de comparar incidents con notifications:
// un incidente sin su fila de aviso ES un aviso pendiente. Sin tabla de cola,
// una caída entre abrir el incidente y mandar el mensaje se resuelve sola.
func (s *Store) AvisosPendientes() ([]model.Aviso, error) {
	filas, err := s.db.Query(`
		SELECT i.id, i.subject, i.kind, i.severity, i.opened_at, i.closed_at, i.detail, 0 AS cierre
		FROM incidents i
		LEFT JOIN notifications n ON n.delivery_id = CAST(i.id AS TEXT) || ':opened'
		WHERE n.delivery_id IS NULL

		UNION ALL

		SELECT i.id, i.subject, i.kind, i.severity, i.opened_at, i.closed_at, i.detail, 1 AS cierre
		FROM incidents i
		LEFT JOIN notifications n ON n.delivery_id = CAST(i.id AS TEXT) || ':closed'
		WHERE i.closed_at IS NOT NULL AND n.delivery_id IS NULL

		ORDER BY 5, 8`)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.Aviso
	for filas.Next() {
		var (
			i       model.Incidente
			abierto int64
			cerrado sql.NullInt64
			cierre  int
		)
		if err := filas.Scan(&i.ID, &i.Sujeto, &i.Tipo, &i.Severidad,
			&abierto, &cerrado, &i.Detalle, &cierre); err != nil {
			return nil, err
		}
		i.AbiertoEn = time.Unix(abierto, 0).UTC()
		if cerrado.Valid {
			t := time.Unix(cerrado.Int64, 0).UTC()
			i.CerradoEn = &t
		}
		sufijo := ":opened"
		if cierre == 1 {
			sufijo = ":closed"
		}
		out = append(out, model.Aviso{
			DeliveryID: strconv.FormatInt(i.ID, 10) + sufijo,
			Incidente:  i,
			Cierre:     cierre == 1,
		})
	}
	return out, filas.Err()
}

// CiclosEnVentana cuenta cuántos incidentes de un sujeto se abrieron desde un
// momento dado. Es la medida de "esto está rebotando".
func (s *Store) CiclosEnVentana(sujeto string, desde time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM incidents WHERE subject = ? AND opened_at >= ?`,
		sujeto, desde.Unix()).Scan(&n)
	return n, err
}

// IncidentesDesde cuenta los incidentes abiertos a partir de un momento.
func (s *Store) IncidentesDesde(desde time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM incidents WHERE opened_at >= ?`, desde.Unix()).Scan(&n)
	return n, err
}

// UptimePorServicio es el porcentaje de probes buenos desde un momento dado.
// Es lo único de los probes que sale a la portada pública.
//
// Un servicio sin mediciones no aparece en el mapa: reportar 0% sería decir
// "caído todo el mes" cuando en realidad recién arranca.
func (s *Store) UptimePorServicio(desde time.Time) (map[string]float64, error) {
	filas, err := s.db.Query(`
		SELECT service, AVG(ok) * 100
		FROM probe_results WHERE ts >= ? GROUP BY service`, desde.Unix())
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	out := map[string]float64{}
	for filas.Next() {
		var nombre string
		var pct float64
		if err := filas.Scan(&nombre, &pct); err != nil {
			return nil, err
		}
		out[nombre] = pct
	}
	return out, filas.Err()
}

// escaparMatch envuelve el texto entre comillas dobles para que FTS5 lo tome
// literal. Sin esto, un paréntesis suelto tirado en el buscador rompe la
// consulta con un error de sintaxis — invariante 11 del spec de la fase 8.
//
// Se respeta el asterisco final, que es lo que hace útil una búsqueda
// incremental: "conex*" matchea "conexion".
func escaparMatch(texto string) string {
	texto = strings.TrimSpace(texto)
	prefijo := strings.HasSuffix(texto, "*")
	texto = strings.TrimSuffix(texto, "*")
	texto = `"` + strings.ReplaceAll(texto, `"`, `""`) + `"`
	if prefijo {
		texto += "*"
	}
	return texto
}

func (s *Store) InsertLogs(ls []model.LineaLog) error {
	if len(ls) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO logs (linea, container, stream, ts) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	// El nivel va en su tabla lateral, atado por el rowid que acaba de asignar
	// el FTS5. Las dos escrituras van en la MISMA transacción: una línea sin
	// su nivel quedaría invisible con cualquier filtro que no sea TRACE.
	stmtNivel, err := tx.Prepare(`INSERT INTO log_niveles (rowid, nivel) VALUES (?,?)`)
	if err != nil {
		return err
	}
	defer stmtNivel.Close()

	for _, l := range ls {
		res, err := stmt.Exec(l.Linea, l.Container, l.Stream, l.TS.Unix())
		if err != nil {
			return fmt.Errorf("insertar log de %s: %w", l.Container, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("rowid del log de %s: %w", l.Container, err)
		}
		nivel := l.Nivel
		if nivel == "" {
			nivel = "INFO"
		}
		if _, err := stmtNivel.Exec(id, nivel); err != nil {
			return fmt.Errorf("insertar nivel de %s: %w", l.Container, err)
		}
	}
	return tx.Commit()
}

// nivelesDesde arma el conjunto de niveles que pasan un mínimo dado.
//
// Se filtra por IN y no por una comparación de orden porque en SQL el nivel es
// texto: 'ERROR' < 'INFO' alfabéticamente, que es exactamente al revés de lo
// que hace falta.
func nivelesDesde(minimo string) []any {
	orden := []string{"TRACE", "INFO", "WARN", "ERROR"}
	desde := 1 // INFO es el default de la vista
	for i, n := range orden {
		if n == strings.ToUpper(strings.TrimSpace(minimo)) {
			desde = i
			break
		}
	}
	out := make([]any, 0, len(orden)-desde)
	for _, n := range orden[desde:] {
		out = append(out, n)
	}
	return out
}

// BuscarLogs devuelve las líneas más nuevas primero. Con texto vacío no usa
// MATCH: devuelve las últimas, que es lo que muestra el panel al entrar.
func (s *Store) BuscarLogs(texto, container, nivelMinimo string, desde, hasta time.Time, limite int) ([]model.LineaLog, error) {
	// COALESCE porque una fila insertada antes de la migración 9 todavía puede
	// no tener nivel si el backfill no llegó: se la trata como INFO en vez de
	// hacerla desaparecer.
	q := `SELECT l.linea, l.container, l.stream, l.ts, COALESCE(n.nivel, 'INFO')
	      FROM logs l LEFT JOIN log_niveles n ON n.rowid = l.rowid
	      WHERE l.ts <= ?`
	args := []any{hasta.Unix()}

	if !desde.IsZero() {
		q += ` AND l.ts >= ?`
		args = append(args, desde.Unix())
	}
	if container != "" {
		q += ` AND l.container = ?`
		args = append(args, container)
	}
	if niveles := nivelesDesde(nivelMinimo); len(niveles) < 4 {
		q += ` AND COALESCE(n.nivel, 'INFO') IN (?` + strings.Repeat(",?", len(niveles)-1) + `)`
		args = append(args, niveles...)
	}
	if strings.TrimSpace(texto) != "" {
		q += ` AND logs MATCH ?`
		args = append(args, escaparMatch(texto))
	}
	q += ` ORDER BY l.ts DESC LIMIT ?`
	args = append(args, limite)

	filas, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer filas.Close()

	var out []model.LineaLog
	for filas.Next() {
		var l model.LineaLog
		var ts int64
		if err := filas.Scan(&l.Linea, &l.Container, &l.Stream, &ts, &l.Nivel); err != nil {
			return nil, err
		}
		l.TS = time.Unix(ts, 0).UTC()
		out = append(out, l)
	}
	return out, filas.Err()
}

// ContarCoincidencias alimenta la alerta por patrón: devuelve cuántas líneas
// matchean y UNA de muestra. Nunca las diez — la regla 3 del spec principal.
func (s *Store) ContarCoincidencias(container, patron string, desde time.Time) (int, string, error) {
	var n int
	var muestra string
	err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(MAX(linea), '')
		FROM logs WHERE container = ? AND ts >= ? AND logs MATCH ?`,
		container, desde.Unix(), escaparMatch(patron)).Scan(&n, &muestra)
	return n, muestra, err
}

// CursorDeLog dice desde dónde seguir leyendo. El bool es false para un
// container que nunca se leyó: ese arranca desde AHORA y no desde su historia
// — invariante 10.
func (s *Store) CursorDeLog(container string) (time.Time, bool, error) {
	var ts int64
	err := s.db.QueryRow(`SELECT ultimo_ts FROM log_cursors WHERE container = ?`, container).Scan(&ts)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.Unix(0, ts).UTC(), true, nil
}

func (s *Store) GuardarCursorDeLog(container string, ts time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO log_cursors (container, ultimo_ts) VALUES (?,?)
		ON CONFLICT(container) DO UPDATE SET ultimo_ts = excluded.ultimo_ts`,
		container, ts.UnixNano())
	return err
}

func (s *Store) BorrarLogsAnterioresA(corte time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Los niveles se podan PRIMERO y por join: después del DELETE de logs sus
	// rowids ya no existen y no habría forma de saber cuáles quedaron sueltos.
	// Sin esto log_niveles crece para siempre.
	if _, err := tx.Exec(`
		DELETE FROM log_niveles WHERE rowid IN (
			SELECT rowid FROM logs WHERE ts < ?
		)`, corte.Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM logs WHERE ts < ?`, corte.Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// BackfillNiveles clasifica un lote de las líneas que ya estaban guardadas
// cuando se aplicó la migración 9. Devuelve cuántas procesó y si ya terminó.
//
// El clasificador entra por parámetro y no por import para que el store no
// dependa de internal/logs — la misma razón por la que rules declara su propia
// interfaz Store. Y así el backfill se testea con una función de tres líneas.
//
// Va por lotes y guarda por dónde iba: son 802 200 filas en la base del VPS y
// hacerlo de una dejaría el arranque bloqueado varios minutos.
func (s *Store) BackfillNiveles(clasificar func(linea, stream string) string, lote int) (int, bool, error) {
	var techo, ultimo int64
	err := s.db.QueryRow(`SELECT hasta_rowid, ultimo FROM backfill_niveles WHERE id = 1`).Scan(&techo, &ultimo)
	if err != nil {
		return 0, false, err
	}
	if ultimo >= techo {
		return 0, true, nil
	}

	filas, err := s.db.Query(`
		SELECT rowid, linea, stream FROM logs
		WHERE rowid > ? AND rowid <= ? ORDER BY rowid LIMIT ?`, ultimo, techo, lote)
	if err != nil {
		return 0, false, err
	}
	type fila struct {
		rowid         int64
		linea, stream string
	}
	var fs []fila
	for filas.Next() {
		var f fila
		if err := filas.Scan(&f.rowid, &f.linea, &f.stream); err != nil {
			filas.Close()
			return 0, false, err
		}
		fs = append(fs, f)
	}
	filas.Close()
	if err := filas.Err(); err != nil {
		return 0, false, err
	}

	// Sin filas pero con techo por delante: son rowids que se borraron por
	// retención. Se salta hasta el techo, si no la pasada no termina nunca.
	if len(fs) == 0 {
		_, err := s.db.Exec(`UPDATE backfill_niveles SET ultimo = ? WHERE id = 1`, techo)
		return 0, true, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO log_niveles (rowid, nivel) VALUES (?,?)
		ON CONFLICT(rowid) DO UPDATE SET nivel = excluded.nivel`)
	if err != nil {
		return 0, false, err
	}
	defer stmt.Close()

	for _, f := range fs {
		if _, err := stmt.Exec(f.rowid, clasificar(f.linea, f.stream)); err != nil {
			return 0, false, err
		}
	}
	fin := fs[len(fs)-1].rowid
	if _, err := tx.Exec(`UPDATE backfill_niveles SET ultimo = ? WHERE id = 1`, fin); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return len(fs), fin >= techo, nil
}

// MarcarComandoProcesado devuelve false si ese delivery ya se había procesado.
//
// comm-tool reintenta hasta 5 veces: sin esto, un "/silenciar 2h" se aplicaría
// cinco veces y el bot contestaría cinco veces.
func (s *Store) MarcarComandoProcesado(deliveryID string, cuando time.Time) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO comandos_procesados (delivery_id, procesado_en) VALUES (?,?)
		ON CONFLICT(delivery_id) DO NOTHING`, deliveryID, cuando.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) Silenciar(hasta time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO silencio (id, hasta) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET hasta = excluded.hasta`, hasta.Unix())
	return err
}

// SilenciadoHasta devuelve hasta cuándo callar. Cero significa "no hay silencio".
func (s *Store) SilenciadoHasta() (time.Time, error) {
	var hasta int64
	err := s.db.QueryRow(`SELECT hasta FROM silencio WHERE id = 1`).Scan(&hasta)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(hasta, 0).UTC(), nil
}

// VacuumInto deja una copia consistente y compacta de la base.
//
// Copiar el archivo vivo NO sirve: con WAL activo puede quedar a mitad de una
// transacción, y el restic de la Mac mini corre a las 04:30 sin saber nada de
// eso. VACUUM INTO produce una base íntegra en un archivo aparte, que es lo
// que el backup se lleva.
//
// El destino se borra primero porque VACUUM INTO falla si ya existe: sin eso
// el backup dejaría de actualizarse en silencio a partir del segundo día.
func (s *Store) VacuumInto(destino string) error {
	if err := os.MkdirAll(filepath.Dir(destino), 0o750); err != nil {
		return err
	}
	if err := os.Remove(destino); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("borrar la copia anterior: %w", err)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, destino); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destino, err)
	}
	return nil
}
