# VAS Sentinel (Guardián de Código Local)

Orquestador y Guardián local determinista desarrollado en Go para mitigar la acumulación masiva de modificaciones en Git Worktrees cuando se opera mediante asistentes agénticos de IA por suscripción.

> **Compatibilidad:** VAS Sentinel funciona en **Windows** y **Debian Linux** por igual.

## Compilación

Los binarios se generan en `bin/<version>/` (nunca se commitean). La versión se lee de `release.yml` (la fuente de verdad del proyecto) y puedes forzarla con la variable `SENTINEL_VERSION`:

```bash
# Windows: genera bin\0.1.0\sentinel.exe
build.bat

# Debian/Linux: genera bin/0.1.0/sentinel
chmod +x build.sh
./build.sh

# Override opcional de la versión (ignora la de release.yml)
export SENTINEL_VERSION=1.2.0
./build.sh
```

## Instalación

### Opción A — Bootstrap con Go (no necesitas el binario previo)

Si tienes Go instalado, puedes instalar el binario directamente desde el módulo, sin clonar el repo ni tener `sentinel` previamente:

```bash
go install github.com/ISeoane-Quental/vas.sentinel/cmd@latest
```

Esto compila el binario en `$(go env GOPATH)/bin` (o `$HOME/go/bin`). Asegúrate de que esa carpeta esté en tu `PATH`. Para comprobar: `sentinel --version`.

> **Nota 1 (versión):** si el repo aún no tiene tags publicados, `@latest` resuelve a la última versión de la rama principal una vez publicado el primer tag. También puedes pedir una versión concreta: `go install github.com/ISeoane-Quental/vas.sentinel/cmd@v0.1.0`.
>
> **Nota 2 (repo privado):** si el repositorio es privado, Go necesita credenciales para descargar el módulo. Configura `GOPRIVATE` y las credenciales de git para GitHub antes de ejecutar el comando:
>
> ```bash
> go env -w GOPRIVATE=github.com/ISeoane-Quental/*
> ```

### Opción B — Con el propio sentinel (una vez instalado)

`sentinel install` descarga la última release publicada en GitHub y la instala de forma global:

- **Windows:** copia el binario a `~/.vas_sentinel/bin/` y añade la ruta al PATH de usuario automáticamente (vía Registro de Windows).
- **Debian/Linux:** instala el binario en `/usr/local/bin/sentinel` (requiere permisos de superusuario; se reintenta con `sudo` automáticamente) y deja la ruta disponible en `~/.zshrc` / `~/.bashrc`.

> **Repos privados:** si el repositorio es privado, exporta `GITHUB_TOKEN` antes de ejecutar `sentinel install` para autenticar la petición a la API de GitHub.

## Actualizaciones automáticas

`sentinel upgrade` descarga la última release publicada y reemplaza el binario actual. En Windows el binario en ejecución está bloqueado, por lo que se renombra el actual a `.old` como respaldo durante la operación.

## Convención de nombres de assets

Para que la instalación y la actualización encuentren el binario correcto, cada release debe publicar assets con la siguiente convención de nombres:

- `sentinel-windows-amd64.exe`
- `sentinel-linux-amd64`
- `sentinel-linux-arm64` (si se publica)

## Publicar una release

1. Actualiza `version` en `release.yml`.
2. Genera los assets multiplataforma desde la raíz del repositorio:

   ```bash
   go run ./tools/release
   ```

   Esto compila `sentinel-<goos>-<goarch>` (con `.exe` en Windows) dentro de `bin/<version>/`.
3. Crea la release en GitHub con los assets generados:

   ```bash
   gh release create v<version> bin/<version>/*
   ```

## Características principales

- **Control Rígido:** Monitorea y restringe los commits si el diferencial local excede el umbral crítico de 400 líneas.

- **Partición por Capas (Slice):** Fragmenta de manera determinista volúmenes grandes de código en micro-commits de un máximo de 400 líneas, organizando el historial en cascada técnica `Config` -&gt; `Backend` -&gt; `Frontend` -&gt; `Tests`).

- **Patrón AgentAdapter:** Desacopla las inteligencias. Consume las APIs de tus suscripciones locales sin consumir tokens extra de API Keys.

- **Configuración YAML:** Modifica el modelo de lenguaje y el nivel de razonamiento (*reasoning effort*) de cada agente al vuelo desde `vassentinel.yml` (per-proyecto o global).

## Comandos Disponibles

- `sentinel init`: Inyecta las directivas de detención en los prompts de tus agentes locales, crea la configuración per-proyecto (`.vas_sentinel/vassentinel.yml`) e instala el Git Hook global `pre-commit`.

- `sentinel check`: Realiza una auditoría matemática en microsegundos del volumen de líneas del Worktree activo.

- `sentinel slice`: Agrupa y procesa de forma secuencial la cola de modificaciones locales, delegando la semántica del mensaje de commit a tu `AgentAdapter` configurado.

- `sentinel install`: Descarga e instala la última release publicada desde GitHub (ver [Instalación](#instalación)).

- `sentinel upgrade`: Actualiza el binario actual a la última release publicada (ver [Actualizaciones automáticas](#actualizaciones-automáticas)).

- `sentinel --version` / `-v`: Muestra la versión instalada.

- `sentinel --help` / `-h`: Muestra la ayuda completa con todos los subcomandos.

## Control de Adaptadores

La configuración se busca en este orden (el primero que define un campo predomina):

1. **Per-proyecto:** `.vas_sentinel/vassentinel.yml` en la raíz del worktree donde ejecutas `sentinel` — creado automáticamente por `sentinel init`.
2. **Global:** `~/.vas_sentinel/vassentinel.yml` — creado automáticamente por `sentinel install`.
3. **Defaults** integrados si no existe ninguno de los anteriores.

La selección del agente activo se configura con la clave raíz `active_agent` (`auto` es el valor por defecto):

- `auto` (default): resuelve automáticamente el primer agente configurado en `agents:` cuyo binario esté disponible en el PATH del sistema.
- Puedes fijar un agente específico con `active_agent: "claude"` o `active_agent: "opencode"`.

`MY_SUB_AGENT` sigue existiendo como override temporal en la terminal, pero ya no es el mecanismo principal de selección:

```bash
# Override temporal en la terminal (opcional)

export MY_SUB_AGENT="opencode"

# Para volver a la resolución automática

unset MY_SUB_AGENT

```

Ejemplo del archivo `.vas_sentinel/vassentinel.yml` completo:

```yaml
version: "1.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
  opencode:
    model: "deepseek-v4-flash"
    reasoning_effort: "max"
```

