package store

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
