package store

import (
	"database/sql"
	"strings"
	"time"
)

// Helpers que solo existen para los tests. Viven en un archivo _test.go a
// propósito: así no entran al binario ni ensucian la API del store, que es lo
// que pasaría si se agregaran métodos de producción para poder testear.

// ContarNiveles dice cuántas filas tiene la tabla lateral. Sirve para
// verificar que la retención no deja huérfanos.
func (s *Store) ContarNiveles() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM log_niveles`).Scan(&n)
	return n, err
}

// ReiniciarBackfillParaTest pone el techo en la última fila y el progreso en
// cero, para poder ejercitar la pasada sobre filas recién insertadas.
//
// En producción el techo lo fija la migración 9 con lo que ya existía, y las
// filas nuevas las clasifica la ingesta: no hay forma legítima de reabrir la
// pasada, y no debería haberla.
func (s *Store) ReiniciarBackfillParaTest() error {
	_, err := s.db.Exec(`
		UPDATE backfill_niveles
		SET hasta_rowid = (SELECT COALESCE(MAX(rowid), 0) FROM logs), ultimo = 0
		WHERE id = 1`)
	return err
}

// OlvidarNivelParaTest borra la fila lateral de una línea, para simular lo que
// dejó la migración 9: filas viejas sin nivel hasta que el backfill las
// alcance. En producción eso no se provoca, se hereda.
func (s *Store) OlvidarNivelParaTest(rowid int64) error {
	_, err := s.db.Exec(`DELETE FROM log_niveles WHERE rowid = ?`, rowid)
	return err
}

// AbrirEnVersionParaTest abre una base aplicando las migraciones SOLO hasta
// version, para poder armar el estado que una migración nueva encuentra en
// producción. Después se cierra y se reabre con Open, que aplica el resto.
func AbrirEnVersionParaTest(ruta string, version int) (*Store, error) {
	db, err := sql.Open("sqlite", ruta+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrarHasta(db, version); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// ExecParaTest escribe SQL crudo. Existe para cargar una base con el esquema
// de una versión vieja, donde los métodos del store ya no sirven porque
// escriben columnas que esa versión todavía no tiene.
func (s *Store) ExecParaTest(q string, args ...any) error {
	_, err := s.db.Exec(q, args...)
	return err
}

// PlanDeBuscarLogs devuelve el EXPLAIN QUERY PLAN de la MISMA consulta que
// arma BuscarLogs, renglón por renglón.
func (s *Store) PlanDeBuscarLogs(texto, container string, niveles []string, desde, hasta time.Time, limite int) (string, error) {
	q, args := consultaBuscarLogs(texto, container, niveles, desde, hasta, limite)
	filas, err := s.db.Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		return "", err
	}
	defer filas.Close()
	var out []string
	for filas.Next() {
		var id, padre, nada int
		var detalle string
		if err := filas.Scan(&id, &padre, &nada, &detalle); err != nil {
			return "", err
		}
		out = append(out, detalle)
	}
	return strings.Join(out, "\n"), filas.Err()
}
