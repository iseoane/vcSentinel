package graph

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitinterno "github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

const (
	versionCacheGrafo = 2
	maxCacheGrafo     = 16 << 20
)

type cacheGrafo struct {
	Version     int                 `json:"version"`
	TreeOID     string              `json:"tree_oid"`
	Fingerprint string              `json:"fingerprint"`
	Imports     map[string][]string `json:"imports"`
	Archivos    map[string]string   `json:"files"`
	Tests       map[string]string   `json:"tests"`
	Consumidos  []string            `json:"consumed"`
}

func oidSeguro(oid string) bool {
	_, err := hex.DecodeString(oid)
	return (len(oid) == 40 || len(oid) == 64) && err == nil && strings.ToLower(oid) == oid
}

func raizCache(directorio string) (*os.Root, error) {
	comun, err := gitinterno.ObtenerGitCommonDir(directorio)
	if err != nil {
		return nil, err
	}
	raiz, err := os.OpenRoot(comun)
	if err != nil {
		return nil, err
	}
	defer raiz.Close()
	ruta := filepath.Join("vas-sentinel", "graph")
	if err := raiz.MkdirAll(ruta, 0700); err != nil {
		return nil, err
	}
	app, appErr := raiz.Lstat("vas-sentinel")
	info, infoErr := raiz.Lstat(ruta)
	if appErr != nil || infoErr != nil || app.Mode()&os.ModeSymlink != 0 || !app.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("directorio de cache inseguro")
	}
	graph, err := raiz.OpenRoot(ruta)
	if err != nil {
		return nil, err
	}
	return graph, nil
}

func rutaCacheValida(ruta string) bool {
	nativa := filepath.FromSlash(ruta)
	return ruta != "" && ruta != "." && ruta != ".." && !filepath.IsAbs(nativa) && !strings.HasPrefix(ruta, "../") && filepath.ToSlash(filepath.Clean(nativa)) == ruta
}

func (c cacheGrafo) valida(oid, fingerprint string) bool {
	if c.Version != versionCacheGrafo || c.TreeOID != oid || c.Fingerprint != fingerprint || !oidSeguro(oid) || len(c.Archivos) == 0 {
		return false
	}
	for _, ruta := range c.Consumidos {
		if !rutaCacheValida(ruta) {
			return false
		}
	}
	for paquete, imports := range c.Imports {
		if paquete == "" || !validosOpcionales(imports) {
			return false
		}
	}
	for ruta, paquete := range c.Archivos {
		if paquete == "" || !rutaCacheValida(ruta) {
			return false
		}
	}
	for paquete, ruta := range c.Tests {
		if paquete == "" || !strings.HasPrefix(ruta, "./") || ruta != "./." && !rutaCacheValida(strings.TrimPrefix(ruta, "./")) {
			return false
		}
	}
	return true
}

func (p *proveedorNativo) cargarCache() ([]string, bool) {
	if !oidSeguro(p.identidad) {
		return nil, false
	}
	raiz, err := raizCache(p.directorio)
	if err != nil {
		return nil, false
	}
	defer raiz.Close()
	nombre := p.identidad + ".json"
	info, err := raiz.Lstat(nombre)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false
	}
	archivo, err := raiz.Open(nombre)
	if err != nil {
		return nil, false
	}
	defer archivo.Close()
	limitado := &io.LimitedReader{R: archivo, N: maxCacheGrafo + 1}
	datos, err := io.ReadAll(limitado)
	if err != nil || len(datos) > maxCacheGrafo {
		return nil, false
	}
	var c cacheGrafo
	dec := json.NewDecoder(bytes.NewReader(datos))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(&struct{}{}) != io.EOF || !c.valida(p.identidad, p.contexto.fingerprint()) {
		return nil, false
	}
	p.imports, p.archivos, p.tests = clonarImports(c.Imports), clonarMapa(c.Archivos), clonarMapa(c.Tests)
	for i, ruta := range c.Consumidos {
		c.Consumidos[i] = filepath.Join(p.directorio, filepath.FromSlash(ruta))
	}
	return c.Consumidos, true
}

func (p *proveedorNativo) guardarCache(consumidos []string) {
	relativas := make([]string, 0, len(consumidos))
	for _, archivo := range consumidos {
		relativa, err := filepath.Rel(p.directorio, archivo)
		if err != nil || !rutaCacheValida(filepath.ToSlash(relativa)) {
			return
		}
		relativas = append(relativas, filepath.ToSlash(relativa))
	}
	ordenarUnicos(&relativas)
	c := cacheGrafo{versionCacheGrafo, p.identidad, p.contexto.fingerprint(), clonarImports(p.imports), clonarMapa(p.archivos), clonarMapa(p.tests), relativas}
	datos, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(datos) > maxCacheGrafo {
		return
	}
	raiz, err := raizCache(p.directorio)
	if err != nil {
		return
	}
	defer raiz.Close()
	temporal := fmt.Sprintf(".graph-%d.tmp", time.Now().UnixNano())
	defer raiz.Remove(temporal)
	archivo, err := raiz.OpenFile(temporal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return
	}
	defer archivo.Close()
	if _, err := archivo.Write(append(datos, '\n')); err == nil && archivo.Close() == nil {
		_ = raiz.Rename(temporal, p.identidad+".json")
	}
}

func clonarImports(origen map[string][]string) map[string][]string {
	destino := make(map[string][]string, len(origen))
	for clave, valores := range origen {
		destino[clave] = clonar(valores)
	}
	return destino
}

func clonarMapa(origen map[string]string) map[string]string {
	destino := make(map[string]string, len(origen))
	for clave, valor := range origen {
		destino[clave] = valor
	}
	return destino
}

func PurgarCacheGrafo(directorio string, antiguedad time.Duration) error {
	raiz, err := raizCache(directorio)
	if err != nil {
		return err
	}
	entradas, err := fs.ReadDir(raiz.FS(), ".")
	defer raiz.Close()
	if err != nil {
		return err
	}
	for _, entrada := range entradas {
		oid := strings.TrimSuffix(entrada.Name(), ".json")
		info, fallo := entrada.Info()
		if fallo == nil && entrada.Name() == oid+".json" && oidSeguro(oid) && info.Mode().IsRegular() && time.Since(info.ModTime()) >= antiguedad {
			_ = raiz.Remove(entrada.Name())
		}
	}
	return nil
}
