// Package notify manda los avisos.
package notify

import "context"

// Canal es una vía de salida. Las implementaciones viven en subpaquetes.
type Canal interface {
	// Nombre es lo que se guarda en notifications.via.
	Nombre() string
	// Configurado dice si tiene credenciales. Un canal sin configurar no se
	// intenta: fallaría siempre y ensuciaría el log.
	Configurado() bool
	Mandar(ctx context.Context, texto string) error
}
