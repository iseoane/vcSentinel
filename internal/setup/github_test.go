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
		if err := descargarBinario(ReleaseAsset{BrowserDownloadURL: servidor.URL}, destino); err != nil {
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

	t.Run("usa la URL API del asset cuando está disponible", func(t *testing.T) {
		const contenidoEsperado = "VIA-API"
		var urlRecibida string
		servidor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			urlRecibida = r.URL.Path
			if _, err := w.Write([]byte(contenidoEsperado)); err != nil {
				t.Errorf("no se pudo escribir la respuesta: %v", err)
			}
		}))
		defer servidor.Close()

		destino := filepath.Join(t.TempDir(), "sentinel.exe")
		asset := ReleaseAsset{
			BrowserDownloadURL: "https://github.com/descarga-que-no-se-usa",
			URL:                servidor.URL + "/assets/123",
		}
		if err := descargarBinario(asset, destino); err != nil {
			t.Fatalf("descargarBinario devolvió error: %v", err)
		}

		if urlRecibida != "/assets/123" {
			t.Errorf("se debió descargar desde la URL API del asset, se usó %q", urlRecibida)
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
		if err := descargarBinario(ReleaseAsset{BrowserDownloadURL: servidor.URL}, destino); err != nil {
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
		err := descargarBinario(ReleaseAsset{BrowserDownloadURL: servidor.URL}, destino)
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

// TestPreguntarTokenGitHub_LeeSinEco comprueba que el token entra por la
// costura de inyección (sin pasar por bufio.Stdin en el test) y que jamás
// aparece en la salida estándar del proceso.
func TestPreguntarTokenGitHub_LeeSinEco(t *testing.T) {
	t.Run("token inyectado se resuelve sin aparecer en stdout", func(t *testing.T) {
		tokenOriginal := os.Getenv("GITHUB_TOKEN")
		os.Unsetenv("GITHUB_TOKEN")
		defer os.Setenv("GITHUB_TOKEN", tokenOriginal)

		terminalOriginal := esTerminalStdin
		esTerminalStdin = func() bool { return true }
		defer func() { esTerminalStdin = terminalOriginal }()

		const tokenFalso = "token-secreto-xyz-789"
		lectorOriginal := leerTokenSinEco
		leerTokenSinEco = func() (string, error) { return tokenFalso, nil }
		defer func() { leerTokenSinEco = lectorOriginal }()

		stdoutOriginal := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("no se pudo crear el pipe: %v", err)
		}
		os.Stdout = w

		token := preguntarTokenGitHub()

		w.Close()
		os.Stdout = stdoutOriginal
		salida, _ := io.ReadAll(r)

		if token != tokenFalso {
			t.Errorf("token = %q, esperado %q", token, tokenFalso)
		}
		if os.Getenv("GITHUB_TOKEN") != tokenFalso {
			t.Errorf("GITHUB_TOKEN no quedó fijado con el token inyectado")
		}
		if strings.Contains(string(salida), tokenFalso) {
			t.Errorf("el token no debe aparecer en la salida estandar, salida: %q", salida)
		}
	})

	t.Run("error del lector no fija token y devuelve vacio", func(t *testing.T) {
		tokenOriginal := os.Getenv("GITHUB_TOKEN")
		os.Unsetenv("GITHUB_TOKEN")
		defer os.Setenv("GITHUB_TOKEN", tokenOriginal)

		terminalOriginal := esTerminalStdin
		esTerminalStdin = func() bool { return true }
		defer func() { esTerminalStdin = terminalOriginal }()

		lectorOriginal := leerTokenSinEco
		leerTokenSinEco = func() (string, error) { return "", errors.New("sin terminal disponible") }
		defer func() { leerTokenSinEco = lectorOriginal }()

		if token := preguntarTokenGitHub(); token != "" {
			t.Errorf("token = %q, esperado vacio ante error del lector", token)
		}
		if os.Getenv("GITHUB_TOKEN") != "" {
			t.Errorf("GITHUB_TOKEN no debe quedar fijado cuando el lector falla")
		}
	})

	t.Run("mecanismo real falla cerrado cuando stdin no es una terminal", func(t *testing.T) {
		// No inyecta leerTokenSinEco: ejercita leerTokenSinEcoDelSistema real
		// (term.ReadPassword) contra un stdin que no es una terminal, que es
		// justo el caso que antes leía con eco en silencio en vez de fallar.
		stdinOriginal := os.Stdin
		lector, escritor, err := os.Pipe()
		if err != nil {
			t.Fatalf("no se pudo crear el pipe: %v", err)
		}
		os.Stdin = lector
		defer func() { os.Stdin = stdinOriginal }()

		if _, err := escritor.WriteString("token-que-no-debe-leerse\n"); err != nil {
			t.Fatalf("no se pudo escribir en el pipe: %v", err)
		}
		escritor.Close()

		token, err := leerTokenSinEcoDelSistema()
		if err == nil {
			t.Fatalf("se esperaba error al no poder desactivar el eco, token=%q", token)
		}
		if token != "" {
			t.Errorf("token = %q, se esperaba vacio cuando falla el mecanismo sin eco", token)
		}
	})

	t.Run("sin terminal interactiva no invoca al lector", func(t *testing.T) {
		terminalOriginal := esTerminalStdin
		esTerminalStdin = func() bool { return false }
		defer func() { esTerminalStdin = terminalOriginal }()

		lectorOriginal := leerTokenSinEco
		invocado := false
		leerTokenSinEco = func() (string, error) { invocado = true; return "no-deberia-usarse", nil }
		defer func() { leerTokenSinEco = lectorOriginal }()

		preguntarTokenGitHub()

		if invocado {
			t.Errorf("no debe invocarse el lector sin terminal interactiva")
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
