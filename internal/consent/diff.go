package consent

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gitinterno "github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

const (
	versionEstadoDiff = 1
	limiteGrantDiff   = 4096
	directorioApp     = "vas-sentinel"
	directorioConsent = "consent"
)

// EstadoDiff registra solo el alcance local del grant; nunca contiene fuente.
type EstadoDiff struct {
	Version     int       `json:"version"`
	Otorgado    bool      `json:"otorgado"`
	Usuario     string    `json:"usuario"`
	Repositorio string    `json:"repositorio"`
	OtorgadoEn  time.Time `json:"otorgado_en,omitempty"`
	Ruta        string    `json:"-"`
}

func alcance(path string) (EstadoDiff, error) {
	commonDir, err := gitinterno.ObtenerGitCommonDir(path)
	if err != nil {
		return EstadoDiff{}, err
	}
	commonDir, err = filepath.Abs(commonDir)
	if err != nil {
		return EstadoDiff{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return EstadoDiff{}, err
	}
	identidad := filepath.Clean(home)
	if runtime.GOOS == "windows" {
		identidad = strings.ToLower(identidad)
	}
	suma := sha256.Sum256([]byte(identidad))
	usuario := hex.EncodeToString(suma[:8])
	ruta := filepath.Join(commonDir, directorioApp, directorioConsent, "external-diff-"+usuario+".json")
	return EstadoDiff{Version: versionEstadoDiff, Usuario: usuario, Repositorio: commonDir, Ruta: ruta}, nil
}

func abrirSubdirectorioSeguro(padre *os.Root, nombre string, crear, privado bool) (*os.Root, bool, error) {
	info, err := padre.Lstat(nombre)
	if errors.Is(err, os.ErrNotExist) {
		if !crear {
			return nil, false, nil
		}
		if err := padre.Mkdir(nombre, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		info, err = padre.Lstat(nombre)
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, fmt.Errorf("componente de consentimiento inseguro: %s", nombre)
	}
	if privado && info.Mode().Perm() != 0700 {
		return nil, false, fmt.Errorf("directorio de consentimiento sin modo privado: %s", nombre)
	}
	raiz, err := padre.OpenRoot(nombre)
	if err != nil {
		return nil, false, err
	}
	abierto, err := raiz.Stat(".")
	if err != nil || !os.SameFile(info, abierto) {
		raiz.Close()
		return nil, false, fmt.Errorf("componente de consentimiento manipulado: %s", nombre)
	}
	return raiz, true, nil
}

func abrirConsentimiento(estado EstadoDiff, crear bool) (*os.Root, bool, error) {
	common, err := os.OpenRoot(estado.Repositorio)
	if err != nil {
		return nil, false, err
	}
	defer common.Close()
	app, existe, err := abrirSubdirectorioSeguro(common, directorioApp, crear, false)
	if err != nil || !existe {
		return nil, existe, err
	}
	defer app.Close()
	return abrirSubdirectorioSeguro(app, directorioConsent, crear, true)
}

func nombreGrant(estado EstadoDiff) string {
	return filepath.Base(estado.Ruta)
}

func leerGrant(raiz *os.Root, estado EstadoDiff) (EstadoDiff, bool, error) {
	nombre := nombreGrant(estado)
	info, err := raiz.Lstat(nombre)
	if errors.Is(err, os.ErrNotExist) {
		return estado, false, nil
	}
	if err != nil {
		return EstadoDiff{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return EstadoDiff{}, false, fmt.Errorf("archivo de grant local inseguro")
	}
	archivo, err := raiz.Open(nombre)
	if err != nil {
		return EstadoDiff{}, false, err
	}
	defer archivo.Close()
	abierto, err := archivo.Stat()
	if err != nil || !os.SameFile(info, abierto) {
		return EstadoDiff{}, false, fmt.Errorf("archivo de grant local manipulado")
	}
	datos, err := io.ReadAll(io.LimitReader(archivo, limiteGrantDiff+1))
	if err != nil {
		return EstadoDiff{}, false, err
	}
	if len(datos) > limiteGrantDiff {
		return EstadoDiff{}, false, fmt.Errorf("grant local excede el limite permitido")
	}
	var guardado EstadoDiff
	decodificador := json.NewDecoder(bytes.NewReader(datos))
	decodificador.DisallowUnknownFields()
	if err := decodificador.Decode(&guardado); err != nil {
		return EstadoDiff{}, false, fmt.Errorf("grant local invalido: %w", err)
	}
	if err := decodificador.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return EstadoDiff{}, false, fmt.Errorf("grant local contiene datos adicionales")
	}
	if guardado.Version != estado.Version || guardado.Usuario != estado.Usuario || filepath.Clean(guardado.Repositorio) != estado.Repositorio || !guardado.Otorgado || guardado.OtorgadoEn.IsZero() {
		return EstadoDiff{}, false, fmt.Errorf("el grant local no coincide con su alcance")
	}
	canonico, err := json.MarshalIndent(guardado, "", "  ")
	if err != nil || !bytes.Equal(datos, append(canonico, '\n')) {
		return EstadoDiff{}, false, fmt.Errorf("grant local no canonico o manipulado")
	}
	guardado.Ruta = estado.Ruta
	return guardado, true, nil
}

func EstadoDiffExterno(path string) (EstadoDiff, error) {
	estado, err := alcance(path)
	if err != nil {
		return EstadoDiff{}, err
	}
	raiz, existe, err := abrirConsentimiento(estado, false)
	if err != nil || !existe {
		return estado, err
	}
	defer raiz.Close()
	guardado, _, err := leerGrant(raiz, estado)
	return guardado, err
}

func escribirTodo(archivo *os.File, datos []byte) error {
	for len(datos) > 0 {
		n, err := archivo.Write(datos)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		datos = datos[n:]
	}
	return nil
}

func crearTemporal(raiz *os.Root) (*os.File, string, error) {
	for intentos := 0; intentos < 8; intentos++ {
		aleatorio := make([]byte, 8)
		if _, err := rand.Read(aleatorio); err != nil {
			return nil, "", err
		}
		nombre := ".grant-" + hex.EncodeToString(aleatorio) + ".tmp"
		archivo, err := raiz.OpenFile(nombre, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		if err := archivo.Chmod(0600); err != nil {
			archivo.Close()
			raiz.Remove(nombre)
			return nil, "", err
		}
		return archivo, nombre, nil
	}
	return nil, "", fmt.Errorf("no se pudo reservar un temporal exclusivo")
}

func OtorgarDiffExterno(path string) (EstadoDiff, error) {
	estado, err := alcance(path)
	if err != nil {
		return EstadoDiff{}, err
	}
	raiz, _, err := abrirConsentimiento(estado, true)
	if err != nil {
		return EstadoDiff{}, err
	}
	defer raiz.Close()
	if guardado, existe, err := leerGrant(raiz, estado); err != nil || existe {
		return guardado, err
	}
	estado.Otorgado = true
	estado.OtorgadoEn = time.Now().UTC()
	datos, err := json.MarshalIndent(estado, "", "  ")
	if err != nil {
		return EstadoDiff{}, err
	}
	datos = append(datos, '\n')
	if len(datos) > limiteGrantDiff {
		return EstadoDiff{}, fmt.Errorf("grant local excede el limite permitido")
	}
	archivo, temporal, err := crearTemporal(raiz)
	if err != nil {
		return EstadoDiff{}, err
	}
	limpiar := true
	defer func() {
		if limpiar {
			raiz.Remove(temporal)
		}
	}()
	if err := escribirTodo(archivo, datos); err != nil {
		archivo.Close()
		return EstadoDiff{}, err
	}
	if err := archivo.Sync(); err != nil {
		archivo.Close()
		return EstadoDiff{}, err
	}
	if err := archivo.Close(); err != nil {
		return EstadoDiff{}, err
	}
	if err := raiz.Link(temporal, nombreGrant(estado)); err != nil {
		if guardado, existe, lecturaErr := leerGrant(raiz, estado); lecturaErr == nil && existe {
			return guardado, nil
		}
		return EstadoDiff{}, fmt.Errorf("no se pudo publicar el grant sin reemplazar el destino: %w", err)
	}
	if err := raiz.Remove(temporal); err != nil {
		return EstadoDiff{}, err
	}
	limpiar = false
	publicado, existe, err := leerGrant(raiz, estado)
	if err != nil {
		return EstadoDiff{}, fmt.Errorf("el grant publicado no supero la validacion: %w", err)
	}
	if !existe {
		return EstadoDiff{}, fmt.Errorf("el grant publicado desaparecio")
	}
	return publicado, nil
}

func RevocarDiffExterno(path string) error {
	estado, err := alcance(path)
	if err != nil {
		return err
	}
	raiz, existe, err := abrirConsentimiento(estado, false)
	if err != nil || !existe {
		return err
	}
	defer raiz.Close()
	if _, existe, err := leerGrant(raiz, estado); err != nil || !existe {
		return err
	}
	return raiz.Remove(nombreGrant(estado))
}
