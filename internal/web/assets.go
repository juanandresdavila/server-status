package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// versionAssets es un hash corto del contenido de cada asset embebido. Se
// calcula una vez al arrancar: los assets viajan adentro del binario, así que
// solo cambian con un deploy.
//
// Existe porque echarts.min.js es 1 MB y se servía sin nada con qué
// cachearlo: el navegador lo bajaba entero en cada visita a /. Medido el
// 22/09/2026, eso era 1,08 s de cada carga del panel.
var versionAssets = func() map[string]string {
	out := map[string]string{}
	err := fs.WalkDir(assets, "assets", func(ruta string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(assets, ruta)
		if err != nil {
			return err
		}
		suma := sha256.Sum256(b)
		out[strings.TrimPrefix(ruta, "assets/")] = hex.EncodeToString(suma[:6])
		return nil
	})
	if err != nil {
		panic("no se pudieron leer los assets embebidos: " + err.Error())
	}
	return out
}()

// urlAsset es la URL con la que las plantillas piden un asset: lleva la
// versión del contenido, y por eso se puede cachear para siempre. Un nombre
// que no existe es un error de la plantilla, no un 404 silencioso.
func urlAsset(nombre string) (string, error) {
	v, ok := versionAssets[nombre]
	if !ok {
		return "", fmt.Errorf("no hay un asset %q", nombre)
	}
	return "/assets/" + nombre + "?v=" + v, nil
}

// cachearAssets pone las cabeceras de caché de /assets/.
//
// Con la versión actual en la URL es inmutable por un año: si el contenido
// cambia, cambia la URL. Sin versión, o con una que no es la de este binario
// —una pestaña abierta desde antes del deploy—, se revalida en cada uso: fijar
// esa URL para siempre la dejaría pegada a un contenido que no es el suyo.
//
// El ETag es débil porque el mismo contenido sale a veces en gzip y a veces
// no, y los bytes difieren; http.ServeContent lo compara igual y contesta 304.
func cachearAssets(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := versionAssets[strings.TrimPrefix(r.URL.Path, "/assets/")]; ok {
			w.Header().Set("ETag", `W/"`+v+`"`)
			if r.URL.Query().Get("v") == v {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
		}
		h.ServeHTTP(w, r)
	})
}
