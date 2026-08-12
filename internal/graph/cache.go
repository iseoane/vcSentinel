package graph

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	versionCacheGrafo = 3
	maxCacheGrafo     = 16 << 20
	maxEntradasCache  = 8
)

type cacheGrafo struct {
	Version int                     `json:"version"`
	Entries map[string]entradaCache `json:"entries"`
}

type entradaCache struct {
	TreeOID    string              `json:"tree_oid"`
	Imports    map[string][]string `json:"imports"`
	Archivos   map[string]string   `json:"files"`
	Tests      map[string]string   `json:"tests"`
	Consumidos []string            `json:"consumed"`
}

var mutexCache sync.Mutex

func bloquearCache(raiz *os.Root) (func(), error) {
	const nombre = ".write.lock"
	if info, err := raiz.Lstat(nombre); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("archivo de bloqueo inseguro")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	archivo, err := abrirArchivoBloqueo(raiz, nombre)
	if err != nil {
		return nil, err
	}
	infoArchivo, errArchivo := archivo.Stat()
	infoRuta, errRuta := raiz.Lstat(nombre)
	if errArchivo != nil || errRuta != nil || !infoArchivo.Mode().IsRegular() ||
		infoRuta.Mode()&os.ModeSymlink != 0 || !os.SameFile(infoArchivo, infoRuta) {
		_ = archivo.Close()
		return nil, fmt.Errorf("archivo de bloqueo inseguro")
	}
	limite := time.Now().Add(250 * time.Millisecond)
	for {
		adquirido, err := intentarBloqueoArchivo(archivo)
		if err != nil {
			_ = archivo.Close()
			return nil, err
		}
		if adquirido {
			return func() {
				_ = desbloquearArchivo(archivo)
				_ = archivo.Close()
			}, nil
		}
		if time.Now().After(limite) {
			_ = archivo.Close()
			return nil, fmt.Errorf("tiempo de espera del bloqueo agotado")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodificarCache(datos []byte) (cacheGrafo, bool) {
	var c cacheGrafo
	dec := json.NewDecoder(bytes.NewReader(datos))
	dec.DisallowUnknownFields()
	ok := dec.Decode(&c) == nil && dec.Decode(&struct{}{}) == io.EOF
	return c, ok
}

func oidSeguro(oid string) bool {
	_, err := hex.DecodeString(oid)
	return (len(oid) == 40 || len(oid) == 64) && err == nil && strings.ToLower(oid) == oid
}

func raizCache(directorio string) (*os.Root, error) {
	comun, err := gitSnapshot(directorio, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	comun = strings.TrimSpace(comun)
	if !filepath.IsAbs(comun) {
		comun = filepath.Join(directorio, comun)
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

func (c cacheGrafo) valida(oid string) bool {
	if c.Version != versionCacheGrafo || !oidSeguro(oid) || len(c.Entries) == 0 || len(c.Entries) > maxEntradasCache {
		return false
	}
	for fingerprint, entrada := range c.Entries {
		if len(fingerprint) != 64 || entrada.TreeOID != oid || !entrada.valida() {
			return false
		}
	}
	return true
}

func (c entradaCache) valida() bool {
	if len(c.Archivos) == 0 {
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

func (p *proveedorNativo) cargarCache() ([]string, bool, error) {
	if !oidSeguro(p.identidad) {
		return nil, false, nil
	}
	raiz, err := raizCache(p.directorio)
	if err != nil {
		return nil, false, nil
	}
	defer raiz.Close()
	nombre := p.identidad + ".json"
	info, err := raiz.Lstat(nombre)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("entrada insegura")
	}
	archivo, err := raiz.Open(nombre)
	if err != nil {
		return nil, false, err
	}
	defer archivo.Close()
	limitado := &io.LimitedReader{R: archivo, N: maxCacheGrafo + 1}
	datos, err := io.ReadAll(limitado)
	if err != nil || len(datos) > maxCacheGrafo {
		return nil, false, nil
	}
	c, ok := decodificarCache(datos)
	if !ok || !c.valida(p.identidad) {
		return nil, false, nil
	}
	entrada, ok := c.Entries[p.contexto.fingerprint()]
	if !ok {
		return nil, false, nil
	}
	p.imports, p.archivos, p.tests = clonarImports(entrada.Imports), clonarMapa(entrada.Archivos), clonarMapa(entrada.Tests)
	for i, ruta := range entrada.Consumidos {
		entrada.Consumidos[i] = filepath.Join(p.directorio, filepath.FromSlash(ruta))
	}
	return entrada.Consumidos, true, nil
}

func (p *proveedorNativo) guardarCache(consumidos []string) error {
	mutexCache.Lock()
	defer mutexCache.Unlock()
	return p.guardarCacheSinMutex(consumidos)
}

func (p *proveedorNativo) guardarCacheSinMutex(consumidos []string) error {
	relativas := make([]string, 0, len(consumidos))
	for _, archivo := range consumidos {
		relativa, err := filepath.Rel(p.directorio, archivo)
		if err != nil || !rutaCacheValida(filepath.ToSlash(relativa)) {
			return nil
		}
		relativas = append(relativas, filepath.ToSlash(relativa))
	}
	ordenarUnicos(&relativas)
	raiz, err := raizCache(p.directorio)
	if err != nil {
		return err
	}
	defer raiz.Close()
	desbloquear, err := bloquearCache(raiz)
	if err != nil {
		return err
	}
	defer desbloquear()
	c := cacheGrafo{Version: versionCacheGrafo, Entries: map[string]entradaCache{}}
	if anterior, ok := p.leerCacheValidaDesde(raiz); ok {
		c = anterior
	}
	fingerprint := p.contexto.fingerprint()
	if _, existe := c.Entries[fingerprint]; !existe && len(c.Entries) >= maxEntradasCache {
		claves := make([]string, 0, len(c.Entries))
		for clave := range c.Entries {
			claves = append(claves, clave)
		}
		sort.Strings(claves)
		delete(c.Entries, claves[0])
	}
	c.Entries[fingerprint] = entradaCache{p.identidad, clonarImports(p.imports), clonarMapa(p.archivos), clonarMapa(p.tests), relativas}
	datos, err := json.MarshalIndent(c, "", "  ")
	if err != nil || len(datos)+1 > maxCacheGrafo {
		return fmt.Errorf("cache excede el límite")
	}
	temporal := fmt.Sprintf(".graph-%d.tmp", time.Now().UnixNano())
	defer raiz.Remove(temporal)
	archivo, err := raiz.OpenFile(temporal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer archivo.Close()
	if _, err := archivo.Write(append(datos, '\n')); err != nil || archivo.Close() != nil {
		return fmt.Errorf("escritura de cache fallida")
	}
	nombre := p.identidad + ".json"
	if err := raiz.Rename(temporal, nombre); err == nil {
		return nil
	}
	if info, err := raiz.Lstat(nombre); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("entrada existente insegura")
		}
		if err := raiz.Remove(nombre); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return raiz.Rename(temporal, nombre)
}

func (p *proveedorNativo) leerCacheValida() (cacheGrafo, bool) {
	raiz, err := raizCache(p.directorio)
	if err != nil {
		return cacheGrafo{}, false
	}
	defer raiz.Close()
	return p.leerCacheValidaDesde(raiz)
}

func (p *proveedorNativo) leerCacheValidaDesde(raiz *os.Root) (cacheGrafo, bool) {
	info, err := raiz.Lstat(p.identidad + ".json")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return cacheGrafo{}, false
	}
	archivo, err := raiz.Open(p.identidad + ".json")
	if err != nil {
		return cacheGrafo{}, false
	}
	defer archivo.Close()
	datos, err := io.ReadAll(&io.LimitedReader{R: archivo, N: maxCacheGrafo + 1})
	c, ok := decodificarCache(datos)
	return c, err == nil && len(datos) <= maxCacheGrafo && ok && c.valida(p.identidad)
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
