package setup

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestElegirAssetParaSistema(t *testing.T) {
	tests := []struct {
		nombre      string
		goos        string
		goarch      string
		assets      []ReleaseAsset
		esperado    string
		quiereError bool
	}{
		{
			nombre:   "windows amd64 encuentra su asset",
			goos:     "windows",
			goarch:   "amd64",
			assets:   []ReleaseAsset{{Name: "sentinel-windows-amd64.exe", BrowserDownloadURL: "https://ejemplo/sentinel-windows-amd64.exe"}},
			esperado: "sentinel-windows-amd64.exe",
		},
		{
			nombre:   "windows amd64 es case-insensitive",
			goos:     "windows",
			goarch:   "amd64",
			assets:   []ReleaseAsset{{Name: "SENTINEL-WINDOWS-AMD64.EXE"}},
			esperado: "SENTINEL-WINDOWS-AMD64.EXE",
		},
		{
			nombre:   "linux amd64 encuentra su asset",
			goos:     "linux",
			goarch:   "amd64",
			assets:   []ReleaseAsset{{Name: "sentinel-linux-amd64"}},
			esperado: "sentinel-linux-amd64",
		},
		{
			nombre:   "linux arm64 encuentra su asset",
			goos:     "linux",
			goarch:   "arm64",
			assets:   []ReleaseAsset{{Name: "sentinel-linux-arm64"}},
			esperado: "sentinel-linux-arm64",
		},
		{
			nombre:      "windows sin asset de windows falla con el patron esperado",
			goos:        "windows",
			goarch:      "amd64",
			assets:      []ReleaseAsset{{Name: "sentinel-linux-amd64"}, {Name: "sentinel-linux-arm64"}},
			quiereError: true,
		},
		{
			nombre:      "assets vacio falla",
			goos:        "linux",
			goarch:      "amd64",
			assets:      nil,
			quiereError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.nombre, func(t *testing.T) {
			asset, err := elegirAssetParaSistema(tt.goos, tt.goarch, tt.assets)
			if tt.quiereError {
				if err == nil {
					t.Fatalf("se esperaba error, no lo hubo")
				}
				if tt.goos == "windows" && !strings.Contains(err.Error(), "sentinel-windows-amd64.exe") {
					t.Errorf("el error debe incluir el patrón esperado, obtuve: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error inesperado: %v", err)
			}
			if asset.Name != tt.esperado {
				t.Errorf("asset.Name = %q, esperado %q", asset.Name, tt.esperado)
			}
		})
	}
}

func TestDescargarBinario(t *testing.T) {
	t.Run("descarga exitosa escribe el contenido exacto", func(t *testing.T) {
		const contenidoEsperado = "BINARIO-FAKE"
		servidor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte(contenidoEsperado)); err != nil {
				t.Errorf("no se pudo escribir la respuesta: %v", err)
			}
		}))
		defer servidor.Close()

		destino := filepath.Join(t.TempDir(), "sentinel.exe")
		if err := descargarBinario(servidor.URL, destino); err != nil {
			t.Fatalf("descargarBinario devolvió error: %v", err)
		}

		contenido, err := os.ReadFile(destino)
		if err != nil {
			t.Fatalf("no se pudo leer el destino: %v", err)
		}
		if string(contenido) != contenidoEsperado {
			t.Errorf("contenido = %q, esperado %q", contenido, contenidoEsperado)
		}
	})

	t.Run("envía Bearer con GITHUB_TOKEN definido", func(t *testing.T) {
		const tokenEsperado = "token-privado-123"
		recibioToken := false
		servidor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got == "Bearer "+tokenEsperado {
				recibioToken = true
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer servidor.Close()

		original := os.Getenv("GITHUB_TOKEN")
		os.Setenv("GITHUB_TOKEN", tokenEsperado)
		defer os.Setenv("GITHUB_TOKEN", original)

		destino := filepath.Join(t.TempDir(), "sentinel.exe")
		if err := descargarBinario(servidor.URL, destino); err != nil {
			t.Fatalf("descargarBinario devolvió error: %v", err)
		}
		if !recibioToken {
			t.Error("la descarga no envió el header Authorization con el token")
		}
	})

	t.Run("respuesta 404 devuelve error", func(t *testing.T) {
		servidor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		}))
		defer servidor.Close()

		destino := filepath.Join(t.TempDir(), "sentinel.exe")
		err := descargarBinario(servidor.URL, destino)
		if err == nil {
			t.Fatalf("se esperaba error con HTTP 404")
		}
		if !strings.Contains(err.Error(), "404") {
			t.Errorf("el error debe mencionar el estado HTTP 404, obtuve: %v", err)
		}
	})
}

type transporteFalso struct {
	respuesta    *http.Response
	errorRed     error
	comprobarPct func(r *http.Request)
}

func (t *transporteFalso) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.comprobarPct != nil {
		t.comprobarPct(req)
	}
	if t.errorRed != nil {
		return nil, t.errorRed
	}
	return t.respuesta, nil
}

func fijarClienteHTTPFalso(t *testing.T, transporte http.RoundTripper) {
	t.Helper()
	original := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transporte}
	t.Cleanup(func() { http.DefaultClient = original })
}

func respuestaJSON(t *testing.T, cuerpo string, estado int) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: estado,
		Status:     http.StatusText(estado),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(cuerpo)),
	}
}

func TestObtenerUltimaRelease(t *testing.T) {
	t.Run("release valida", func(t *testing.T) {
		cuerpo := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-windows-amd64.exe","browser_download_url":"https://ejemplo/x"}]}`
		transport := &transporteFalso{respuesta: respuestaJSON(t, cuerpo, http.StatusOK)}
		transport.comprobarPct = func(r *http.Request) {
			if r.Header.Get("User-Agent") == "" {
				t.Errorf("la petición debe incluir User-Agent")
			}
		}
		fijarClienteHTTPFalso(t, transport)

		release, err := obtenerUltimaRelease()
		if err != nil {
			t.Fatalf("obtenerUltimaRelease devolvió error: %v", err)
		}
		if release.TagName != "v1.2.3" {
			t.Errorf("TagName = %q, esperado v1.2.3", release.TagName)
		}
		if len(release.Assets) != 1 || release.Assets[0].Name != "sentinel-windows-amd64.exe" {
			t.Errorf("assets inesperados: %+v", release.Assets)
		}
	})

	t.Run("HTTP 404 devuelve error de release no publicada", func(t *testing.T) {
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, "{}", http.StatusNotFound)})
		_, err := obtenerUltimaRelease()
		if err == nil {
			t.Fatalf("se esperaba error con HTTP 404")
		}
		if !strings.Contains(err.Error(), "no se encontró una release") {
			t.Errorf("el error debe mencionar la release no publicada, obtuve: %v", err)
		}
	})

	t.Run("HTTP 500 devuelve error de estado", func(t *testing.T) {
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, "{}", http.StatusInternalServerError)})
		_, err := obtenerUltimaRelease()
		if err == nil {
			t.Fatalf("se esperaba error con HTTP 500")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("el error debe mencionar el estado 500, obtuve: %v", err)
		}
	})

	t.Run("JSON invalido devuelve error de interpretación", func(t *testing.T) {
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, "no es json", http.StatusOK)})
		_, err := obtenerUltimaRelease()
		if err == nil {
			t.Fatalf("se esperaba error con JSON inválido")
		}
		if !strings.Contains(err.Error(), "interpretar la respuesta") {
			t.Errorf("el error debe mencionar la interpretación del JSON, obtuve: %v", err)
		}
	})

	t.Run("sin tag_name devuelve error", func(t *testing.T) {
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, `{"assets":[]}`, http.StatusOK)})
		_, err := obtenerUltimaRelease()
		if err == nil {
			t.Fatalf("se esperaba error sin tag_name")
		}
		if !strings.Contains(err.Error(), "tag_name") {
			t.Errorf("el error debe mencionar el tag_name, obtuve: %v", err)
		}
	})

	t.Run("error de red devuelve error de red", func(t *testing.T) {
		fijarClienteHTTPFalso(t, &transporteFalso{errorRed: errors.New("conexión rechazada")})
		_, err := obtenerUltimaRelease()
		if err == nil {
			t.Fatalf("se esperaba error de red")
		}
		if !strings.Contains(err.Error(), "error de red") {
			t.Errorf("el error debe mencionar la red, obtuve: %v", err)
		}
	})
}

func TestElegirAssetParaSO(t *testing.T) {
	nombre := "sentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		nombre += ".exe"
	}

	asset, err := elegirAssetParaSO([]ReleaseAsset{{Name: nombre, BrowserDownloadURL: "https://ejemplo/" + nombre}})
	if err != nil {
		t.Fatalf("elegirAssetParaSO devolvió error: %v", err)
	}
	if asset.Name != nombre {
		t.Errorf("asset.Name = %q, esperado %q", asset.Name, nombre)
	}
}

func releaseValidaConAsset() string {
	nombre := "sentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		nombre += ".exe"
	}
	return `{"tag_name":"v1.2.3","assets":[{"name":"` + nombre + `","browser_download_url":"http://127.0.0.1:1/descarga"}]}`
}

// desactivarFallbackGoInstall apaga el reintento con go install durante un
// test y lo restaura al terminar, para que los errores de red/descarga no
// ejecuten compilaciones reales.
func desactivarFallbackGoInstall(t *testing.T) {
	t.Helper()
	anterior := fallbackGoInstall
	fallbackGoInstall = false
	t.Cleanup(func() { fallbackGoInstall = anterior })
}

func TestEjecutarInstalacionCompleta(t *testing.T) {
	t.Run("error de red detiene la instalacion", func(t *testing.T) {
		desactivarFallbackGoInstall(t)
		fijarClienteHTTPFalso(t, &transporteFalso{errorRed: errors.New("conexión rechazada")})
		err := EjecutarInstalacionCompleta()
		if err == nil {
			t.Fatalf("se esperaba error de red")
		}
		if !strings.Contains(err.Error(), "error de red") {
			t.Errorf("el error debe mencionar la red, obtuve: %v", err)
		}
	})

	t.Run("sin asset para el sistema detiene la instalacion", func(t *testing.T) {
		cuerpo := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-plan9-amd64","browser_download_url":"https://ejemplo/x"}]}`
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, cuerpo, http.StatusOK)})
		err := EjecutarInstalacionCompleta()
		if err == nil {
			t.Fatalf("se esperaba error sin asset para el sistema")
		}
		if !strings.Contains(err.Error(), "no se encontró un asset") {
			t.Errorf("el error debe mencionar la selección de asset, obtuve: %v", err)
		}
	})

	t.Run("descarga fallida detiene la instalacion", func(t *testing.T) {
		desactivarFallbackGoInstall(t)
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, releaseValidaConAsset(), http.StatusOK)})
		err := EjecutarInstalacionCompleta()
		if err == nil {
			t.Fatalf("se esperaba error de descarga")
		}
		if !strings.Contains(err.Error(), "descargar") {
			t.Errorf("el error debe mencionar la descarga, obtuve: %v", err)
		}
	})
}

func TestEjecutarUpgradeDesdeGitHub(t *testing.T) {
	t.Run("error de red detiene la actualizacion", func(t *testing.T) {
		desactivarFallbackGoInstall(t)
		fijarClienteHTTPFalso(t, &transporteFalso{errorRed: errors.New("conexión rechazada")})
		err := EjecutarUpgradeDesdeGitHub()
		if err == nil {
			t.Fatalf("se esperaba error de red")
		}
		if !strings.Contains(err.Error(), "error de red") {
			t.Errorf("el error debe mencionar la red, obtuve: %v", err)
		}
	})

	t.Run("sin asset para el sistema detiene la actualizacion", func(t *testing.T) {
		cuerpo := `{"tag_name":"v1.2.3","assets":[{"name":"sentinel-plan9-amd64","browser_download_url":"https://ejemplo/x"}]}`
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, cuerpo, http.StatusOK)})
		err := EjecutarUpgradeDesdeGitHub()
		if err == nil {
			t.Fatalf("se esperaba error sin asset para el sistema")
		}
		if !strings.Contains(err.Error(), "no se encontró un asset") {
			t.Errorf("el error debe mencionar la selección de asset, obtuve: %v", err)
		}
	})

	t.Run("descarga fallida detiene la actualizacion antes de reemplazar", func(t *testing.T) {
		desactivarFallbackGoInstall(t)
		fijarClienteHTTPFalso(t, &transporteFalso{respuesta: respuestaJSON(t, releaseValidaConAsset(), http.StatusOK)})
		err := EjecutarUpgradeDesdeGitHub()
		if err == nil {
			t.Fatalf("se esperaba error de descarga")
		}
		if !strings.Contains(err.Error(), "descargar") {
			t.Errorf("el error debe mencionar la descarga, obtuve: %v", err)
		}
	})
}
