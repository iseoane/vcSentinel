package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	gitinterno "github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

const versionEstadoDiff = 1

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
	ruta := filepath.Join(commonDir, "vas-sentinel", "consent", "external-diff-"+usuario+".json")
	return EstadoDiff{Version: versionEstadoDiff, Usuario: usuario, Repositorio: commonDir, Ruta: ruta}, nil
}

func EstadoDiffExterno(path string) (EstadoDiff, error) {
	estado, err := alcance(path)
	if err != nil {
		return EstadoDiff{}, err
	}
	datos, err := os.ReadFile(estado.Ruta)
	if os.IsNotExist(err) {
		return estado, nil
	}
	if err != nil {
		return EstadoDiff{}, err
	}
	var guardado EstadoDiff
	if err := json.Unmarshal(datos, &guardado); err != nil {
		return EstadoDiff{}, fmt.Errorf("grant local invalido: %w", err)
	}
	if guardado.Version != estado.Version || guardado.Usuario != estado.Usuario || filepath.Clean(guardado.Repositorio) != estado.Repositorio || !guardado.Otorgado {
		return EstadoDiff{}, fmt.Errorf("el grant local no coincide con su alcance")
	}
	guardado.Ruta = estado.Ruta
	return guardado, nil
}

func OtorgarDiffExterno(path string) (EstadoDiff, error) {
	estado, err := alcance(path)
	if err != nil {
		return EstadoDiff{}, err
	}
	estado.Otorgado = true
	estado.OtorgadoEn = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(estado.Ruta), 0700); err != nil {
		return EstadoDiff{}, err
	}
	datos, err := json.MarshalIndent(estado, "", "  ")
	if err != nil {
		return EstadoDiff{}, err
	}
	if err := os.WriteFile(estado.Ruta, append(datos, '\n'), 0600); err != nil {
		return EstadoDiff{}, err
	}
	return estado, nil
}

func RevocarDiffExterno(path string) error {
	estado, err := alcance(path)
	if err != nil {
		return err
	}
	if err := os.Remove(estado.Ruta); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
