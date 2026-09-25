# vcSentinel

[![Licencia PolyForm Noncommercial 1.0.0](https://img.shields.io/badge/license-PolyForm%20Noncommercial%201.0.0-6f42c1)](LICENSE)
[![vcSentinel 1.0.1](https://img.shields.io/badge/vcSentinel-1.0.1-2563eb)](release.yml)
[![Go 1.26.5+](https://img.shields.io/badge/Go-1.26.5%2B-00ADD8?logo=go&logoColor=white)](go.mod)

[English](README.md) · [Español](README.es.md)

vcSentinel es un guardián local determinista escrito en Go para árboles de
trabajo Git operados con agentes de IA. Mide el volumen acumulado de código
escrito, ayuda a dividir árboles de trabajo grandes en commits revisables,
instala un control local de volumen para los cambios preparados, registra las
revisiones semánticas por commit y mantiene separada la validación determinista
de la publicación de pull requests.

Preserva el control humano: nunca responde por ti a una decisión pendiente de
`slice`, `pr review` no audita silenciosamente los commits que falten y
`pr create` no inventa una revisión. Los comandos de medición local y de
validación configurada funcionan sin agente; la revisión con agente y la
generación de mensajes de commit necesitan un agente configurado.

## Requisitos previos

- **Git**, para preparar el repositorio, medir árboles de trabajo y crear
  commits.
- **Go 1.26.5 o posterior**, solo para compilar desde el código fuente,
  arrancar con `go install`, usar los planes de instalación alternativos o
  actualizar en Windows. Las releases descargadas no necesitan Go en tiempo de
  ejecución.
- Un binario de agente en `PATH` cuando uses revisión semántica o pidas a
  `slice` que genere mensajes de commit. Se configura en
  `.vcsentinel/vcsentinel.yml` o `~/.vcsentinel/vcsentinel.yml`.
- **GitHub CLI (`gh`) es opcional.** Se utiliza para publicar automáticamente
  una PR de GitHub y para recoger evidencias de GitHub Actions configuradas.
  Las comprobaciones, revisiones y validaciones locales no lo necesitan. Si
  `gh` no está disponible, `pr create` intenta usar el portapapeles en vez de
  afirmar que ha creado una PR.
- **CodeGraph es opcional** y solo aporta contexto adicional a la revisión
  semántica; nunca es necesario para validar.

## Camino rápido

Si todavía no hay ningún binario de vcSentinel instalado, puedes arrancar con
Go:

```bash
go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest
vcsentinel --version
```

Desde el repositorio Git que quieras proteger:

```bash
vcsentinel init
vcsentinel check
```

`init` crea la configuración del proyecto, instala el control local
`pre-commit` y añade las instrucciones gestionadas de vcSentinel. El
`check` normal mide todo el árbol de trabajo y es informativo, incluso cuando
el resultado es `CRITICAL`.

## Instalación y actualización

### Arranque o instalación de una release publicada

El arranque con Go anterior no necesita un binario previo y coloca el programa
en `$(go env GOPATH)/bin`; añade esa carpeta a `PATH`.

Si ya tienes disponible un binario de vcSentinel, este comando descarga la
última release publicada para la plataforma actual y la instala globalmente
para tu cuenta:

```bash
vcsentinel install
```

También crea `~/.vcsentinel/vcsentinel.yml` si hace falta. **No** inicializa el
repositorio actual; ejecuta `vcsentinel init` en cada repositorio que deba usar
vcSentinel.

El instalador usa `/usr/local/bin/vcsentinel` en Debian/Linux y
`%USERPROFILE%\.vcsentinel\bin\vcsentinel.exe` en Windows, y actualiza el
`PATH` del usuario cuando es necesario. En Debian/Linux puede pedir `sudo` para
escribir en `/usr/local/bin`. Si falla la descarga de una release, `install`
puede reintentarlo mediante `go install`, lo que requiere Go; `upgrade` no usa
ese reintento en Windows. En repositorios privados, las credenciales se buscan
en `GITHUB_TOKEN`, en una sesión autenticada de `gh` o mediante una petición
interactiva de token.

### Actualizar

```bash
vcsentinel upgrade
```

En Debian/Linux sustituye el binario instalado por la última release, con el
mismo reintento mediante el código fuente si la descarga no está disponible.
En Windows el `.exe` que está ejecutándose queda bloqueado, por lo que la
actualización automática termina con código distinto de cero **antes de
contactar con GitHub o modificar la instalación**. Muestra un comando de
PowerShell listo para copiar que establece `GOBIN` en el directorio de
instalación de vcSentinel y ejecuta:

```text
go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest
```

Ese camino manual de Windows requiere Go. `upgrade` solo modifica la
instalación del usuario, no la configuración, las instrucciones, la skill ni el
hook de ningún repositorio.

`vcsentinel uninstall` elimina la instalación global y su configuración
global. No elimina la configuración de los repositorios; para eso usa
`vcsentinel uninit` en cada uno.

## Inicializar o retirar la configuración del repositorio

`vcsentinel init` debe ejecutarse dentro de un árbol de trabajo Git. Si se
invoca desde un subdirectorio, redirige la operación a la raíz y crea o
conserva:

- `.vcsentinel/vcsentinel.yml`, la configuración del proyecto;
- las instrucciones de volumen gestionadas en `AGENTS.md`, `CLAUDE.md` o
  `.claudecode.md`;
- `.agents/skills/vcsentinel/SKILL.md`, cuando no existe ya una skill ajena o
  modificada; y
- el hook `pre-commit` en el directorio común del repositorio Git.

`vcsentinel uninit` elimina únicamente la configuración propiedad de
vcSentinel y conserva los archivos ajenos o modificados. Los comandos que
dependen de la configuración del proyecto —por ejemplo `check`, `slice`,
`review`, `gate` y `pr`— requieren haberlo inicializado antes.

## Comprobación de volumen y commits revisables

El límite de volumen es de **400 líneas de código escrito** para el candidato
preparado para commit:

| Comando | Contrato |
|---|---|
| `vcsentinel check` | Mide todo el árbol de trabajo. `CRITICAL` es informativo y el comando sigue terminando correctamente. |
| `vcsentinel check --staged` | Mide solo los cambios preparados y termina con `1` por encima de 400 líneas escritas. El hook instalado usa esta forma. |
| `vcsentinel slice` | Flujo interactivo por `stdin` para agrupar los cambios pendientes en commits revisables. |
| `vcsentinel slice plan --json` | Propuesta no interactiva; no crea commits y termina con `3` cuando `pending_decisions` necesita una respuesta humana. |
| `vcsentinel slice apply --plan plan.json --answers answers.json` | Aplica un plan solo si coinciden su árbol y `plan_id`, y cada decisión pendiente tiene una respuesta explícita. |

En una sesión conducida por un agente, usa el flujo plan/apply en lugar del
REPL interactivo:

```bash
vcsentinel slice plan --json > plan.json
```

Si el plan termina con `3`, muestra sus `pending_decisions` a una persona. No
elijas `bypass` ni `abort` en su nombre. Escribe las respuestas literales de
esa persona (los únicos valores de decisión son `bypass` y `abort`) en
`answers.json`:

```json
{
  "plan_id": "<plan_id de plan.json>",
  "answers": {
    "<decision id>": "bypass"
  }
}
```

Si no hay decisiones pendientes, usa un mapa de respuestas vacío:

```json
{
  "plan_id": "<plan_id de plan.json>",
  "answers": {}
}
```

```bash
vcsentinel slice apply --plan plan.json --answers answers.json
```

El flujo plan/apply solo transporta la pregunta; la decisión sigue siendo
humana. `apply` rechaza contenido obsoleto, un `plan_id` distinto o decisiones
pendientes sin respuesta. Un archivo demasiado grande puede generar una
decisión explícita en lugar de forzarla silenciosamente dentro de un commit.

## De varios commits a una pull request

Este es un flujo realista para varios commits. Mantiene separadas las
responsabilidades: volumen del árbol, división controlada por una persona,
revisión semántica por commit, validación determinista, evidencias guardadas de
la rama y publicación.

```bash
# Antes y durante el trabajo: medición informativa de todo el árbol.
vcsentinel check

# Si el resultado es CRITICAL, crea y responde un plan como se explica arriba.
vcsentinel slice plan --json > plan.json
vcsentinel slice apply --plan plan.json --answers answers.json

# Audita cada commit aún no revisado hasta HEAD; --gate termina con 1 ante un hallazgo crítico.
vcsentinel review --all --gate

# Ejecuta la validación configurada sin agente.
vcsentinel gate --stage pr

# Redacta y guarda el juicio/evidencias de la rama; no publica.
vcsentinel pr review --base main --overview

# Publica únicamente la revisión guardada para la rama y HEAD actuales.
vcsentinel pr create --base main
```

Este es el árbol de llamadas resumido:

```text
check (informativo)
└─ slice plan → respuestas humanas → slice apply → commits revisables
   └─ review --all → gate --stage pr
      └─ pr review → juicio/evidencias guardados de la rama
         └─ pr create → validación determinista → publicación con gh o portapapeles
```

El diagrama completo del flujo, redactado en inglés, se publicará en
[GitHub Pages](https://iseoane.github.io/vcSentinel/vcsentinel-multi-commit-pr.html).
Si el repositorio se transfiere de nuevo, esta URL específica del propietario
cambiará y habrá que actualizar el enlace.

`vcsentinel pr review` guarda localmente el juicio de la rama e informa de los
commits que todavía necesitan un `vcsentinel review` individual; nunca publica.
`pr create` exige esa entrada guardada, la rama y el HEAD actuales, y las
evidencias de revisión confirmadas. No hace una revisión semántica. Vuelve a
ejecutar el perfil de validación configurado: una validación determinista en
rojo bloquea la publicación, salvo que una persona indique explícitamente
`--force --reason "..."`. Un hallazgo semántico no se borra ni se vuelve a
revisar desde `pr create`.

Si `gh` no está instalado o falla la publicación, vcSentinel intenta copiar el
cuerpo generado con `clip`, `wl-copy` o `xclip`. Ese camino alternativo no crea
ninguna PR; créala manualmente usando el cuerpo copiado y el archivo temporal.
Si vcSentinel informa de que las evidencias no están confirmadas, confirma las
rutas que indique y vuelve a ejecutar `vcsentinel pr review`, porque la entrada
guardada está vinculada a HEAD.

Para una PR apilada, indica el padre explícitamente cuando sea necesario:

```bash
vcsentinel pr review --base main --parent feature-a --overview
vcsentinel pr create --base main --parent feature-a --chain-pr
```

## Revisión semántica, validación y publicación

Estas etapas responden a preguntas distintas y están separadas a propósito.

### Revisión semántica por commit

`vcsentinel review <sha> [--dims ...]` comprueba el comportamiento y la
intención del commit según seis contratos de dimensión:

- **`logic`** — condiciones, gestión de errores, casos límite y efectos
  secundarios.
- **`style`** — nombres, código idiomático, claridad y convenciones del
  repositorio.
- **`design`** — estructura, acoplamiento, cohesión y dependencias orientadas al
  dominio.
- **`tests`** — cobertura significativa, determinismo y si las pruebas
  demuestran un cambio de comportamiento real.
- **`security`** — límites de privilegio, entradas no confiables y datos
  expuestos.
- **`spec`** — si el diff hace lo que afirma el commit, sin trabajo fuera de
  alcance ni afirmaciones sin respaldo.

Sin `--dims`, vcSentinel deriva un plan según el riesgo; **no ejecuta las seis
dimensiones en cada commit**. El plan automático excluye deliberadamente
`style`, porque esa comprobación corresponde al lint determinista. Usa
`--dims logic,style,design,tests,security,spec` cuando necesites seleccionar
explícitamente las dimensiones; las dimensiones indicadas sustituyen el plan
derivado. `--gate` añade el fallo ante un resultado crítico de la revisión; no
sustituye al gate determinista.

### Validación determinista

`vcsentinel gate --stage pre-commit|pre-push|pr [--profile X]` ejecuta los
comandos configurados sin agente ni veredicto semántico. En este proyecto, el
perfil `standard` predeterminado comprueba estas capacidades: `format`
(`gofmt -l .`, falla si imprime archivos), `lint` (`go vet ./...`), `build`
(`go build ./...`) y `unit_test` (`go test ./...`). `--profile` selecciona otro
perfil de validación con nombre; `--stage` solo registra el punto del ciclo
(`pre-commit`, `pre-push` o `pr`) y es independiente del perfil.

### Revisión de la rama y publicación

`vcsentinel pr review --base main` audita el diff neto de la rama frente a su
base y guarda localmente el juicio y las evidencias de la rama. También informa
de los commits que todavía no tienen un registro individual de
`vcsentinel review`. Esa lista de pendientes es una distinción importante, no
una auditoría oculta: `pr review` no revisa silenciosamente esos commits ni
ejecuta la validación determinista.

La decisión es **una sola PR** hasta 400 líneas modificadas. Por encima de 400,
la decisión es **PR encadenadas**, salvo que se demuestre coherencia. Añade
`--overview` para solicitar la comprobación de coherencia de la rama; una rama
sobredimensionada pero coherente puede mantenerse como una sola PR, mientras
que una incoherente debería dividirse.

`vcsentinel pr create --base main` consume el juicio guardado para la rama y el
HEAD actuales, verifica sus evidencias confirmadas y vuelve a ejecutar el
perfil de validación determinista. No hace una revisión semántica ni redacta
un veredicto semántico: publica el resultado guardado solo cuando esas
comprobaciones pasan (o tras un `--force --reason "..."` indicado
explícitamente por una persona).

`pr create` solo usa GitHub Actions cuando se configura explícitamente
`ci.workflow`. Un workflow ausente o vacío desactiva sus evidencias; vcSentinel
nunca deduce un workflow solo porque exista `.github/workflows`.

## Configuración

La configuración se carga con esta precedencia:

1. Valores predeterminados integrados.
2. Configuración global `~/.vcsentinel/vcsentinel.yml`.
3. Configuración por proyecto `.vcsentinel/vcsentinel.yml`.

El archivo del proyecto prevalece en los campos que define. Las listas de
comandos como `lint_commands`, `test_commands` y `build_commands` se acumulan
entre archivos. `vcsentinel init` crea el archivo del proyecto;
`vcsentinel install` crea el global si hace falta. Los comandos que cargan la
configuración estrictamente rechazan claves YAML desconocidas en lugar de
ignorarlas.

Esta es una configuración pequeña de proyecto que habilita el perfil estándar
de validación determinista:

```yaml
version: "1.0"
active_agent: "auto"
commit_language: "en"

review:
  timeout: 900
  parallel: 2
  codegraph_context: false

validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: "output_not_empty"
    unit_test:
      command: "go test ./..."
    build:
      command: "go build ./..."
  profiles:
    standard: [format, unit_test, build]
  mode: worktree
```

Usa un modelo y un binario que sean compatibles con tu agente configurado; el
ejemplo se apoya en las recetas de agentes predeterminadas de vcSentinel. Las
listas antiguas `lint_commands`, `test_commands` y `build_commands` siguen
siendo válidas y se traducen a capacidades de validación cuando no se declaran
capacidades explícitas.

Para recoger evidencias opcionales de GitHub Actions durante `pr create`,
añade un bloque de workflow explícito:

```yaml
ci:
  workflow: "CI"
  wait_seconds: 900
  poll_seconds: 15
```

El nombre del workflow debe existir en GitHub Actions. Un bloque `ci`
configurado necesita un remoto alojado en GitHub y `gh`; su estado es evidencia
en el cuerpo de la PR, no una revisión semántica.

## Contexto opcional con CodeGraph

[CodeGraph](https://github.com/colbymchenry/codegraph) es una fuente opcional de
metadatos no confiables sobre dependencias y rutas para la revisión semántica.
Puede mejorar el contexto del revisor, pero nunca autoriza el alcance de la
validación: el análisis nativo de Go sigue siendo la única autoridad para ese
alcance, y la falta de contexto de CodeGraph no hace fallar el gate.

Actívalo solo en el archivo de proyecto versionado y concede después el
permiso local:

```yaml
request_external_agent_diff: true

review:
  codegraph_context: true
```

```bash
vcsentinel consent-diff grant
vcsentinel doctor
```

Para usar el contexto deben cumplirse todas estas condiciones:

1. `review.codegraph_context` es `true`.
2. El archivo del proyecto (no solo el global) define
   `request_external_agent_diff: true`.
3. El usuario actual tiene un permiso local concedido con
   `vcsentinel consent-diff grant`.
4. El ejecutable `codegraph` se puede resolver en `PATH`.
5. Existe un directorio `.codegraph` bajo la raíz canónica del árbol de
   trabajo.
6. Git resuelve `HEAD` y el árbol de trabajo está limpio.
7. `codegraph status --json` informa de un índice inicializado.
8. Su `projectPath` coincide exactamente con este árbol, `pendingChanges` es
   cero y `worktreeMismatch` es `null`.

`vcsentinel doctor` muestra estas comprobaciones como `binary`, `index_dir`,
`head`, `worktree_clean`, `index_initialized`, `project_path`,
`pending_changes` y `worktree_match`. Es informativo, termina con `0`, nunca
instala nada y muestra como `UNKNOWN` las comprobaciones que no se pudieron
ejecutar. Si falla cualquier condición de CodeGraph, el contexto de revisión
se omite o queda registrado como omitido; la validación determinista no cambia.

## Comandos útiles y advertencias

| Comando | Uso |
|---|---|
| `vcsentinel doctor [--check-updates]` | Comprueba agentes, herramientas de búsqueda, CodeGraph, la configuración estricta y el hook. `--check-updates` activa la comprobación de red opcional. |
| `vcsentinel status --json` | Lee el volumen, las revisiones guardadas y la actividad reciente en formato procesable. |
| `vcsentinel metrics --json` | Lee mediciones locales de revisión, remediación y ejecución; los valores desconocidos siguen siendo `null`. |
| `vcsentinel explain HEAD~3..HEAD --json` | Muestra áreas modificadas, riesgo, cohesión y una división sugerida. |
| `vcsentinel lint` | Ejecuta `lint_commands`; es independiente de los perfiles de `gate`. |
| `vcsentinel runs status --json` | Consulta las tareas de agentes registradas; la [guía de `runs`](docs/design/runs-cli.md) describe el ciclo de vida y los códigos de salida. |
| `vcsentinel tui` | Abre el panel interactivo de repositorios registrados y tareas controladas. |
| `vcsentinel consent-diff status` | Muestra el permiso local actual para compartir diffs pequeños con agentes configurados. |

Los comandos de validación, lint, test y build proceden de YAML y se ejecutan a
través del shell del sistema. Trata esa configuración como código de confianza.
Los registros de revisión y los datos de tareas duraderas viven en el
directorio Git común de vcSentinel, por lo que los árboles de trabajo enlazados
comparten los registros locales del repositorio.

## Licencia

vcSentinel se distribuye bajo la [PolyForm Noncommercial License
1.0.0](LICENSE). Esta licencia no permite el uso comercial.
