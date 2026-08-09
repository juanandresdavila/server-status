// Package store guarda las muestras en SQLite.
package store

import (
	"database/sql"
	"fmt"
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
		INSERT INTO container_samples (ts, name, state, health, restarts, cpu_pct, mem_bytes)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(ts, name) DO UPDATE SET
			state=excluded.state, health=excluded.health, restarts=excluded.restarts,
			cpu_pct=excluded.cpu_pct, mem_bytes=excluded.mem_bytes`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range ms {
		if _, err := stmt.Exec(
			m.TS.Truncate(time.Minute).Unix(), m.Name, m.State, m.Health,
			m.Restarts, m.CPUPct, int64(m.MemBytes),
		); err != nil {
			return fmt.Errorf("insertar %s: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// UltimoEstadoContainers devuelve la foto del minuto más reciente.
func (s *Store) UltimoEstadoContainers() ([]model.ContainerSample, error) {
	filas, err := s.db.Query(`
		SELECT ts, name, state, health, restarts, cpu_pct, mem_bytes
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
			c   model.ContainerSample
			ts  int64
			mem int64
		)
		if err := filas.Scan(&ts, &c.Name, &c.State, &c.Health, &c.Restarts, &c.CPUPct, &mem); err != nil {
			return nil, err
		}
		c.TS = time.Unix(ts, 0).UTC()
		c.MemBytes = uint64(mem)
		out = append(out, c)
	}
	return out, filas.Err()
}
