# VAS Sentinel (Guardián de Código Local)

Orquestador y Guardián local determinista desarrollado en Go para mitigar la acumulación masiva de modificaciones en Git Worktrees cuando se opera mediante asistentes agénticos de IA por suscripción.

## Características principales

- **Control Rígido:** Monitorea y restringe los commits si el diferencial local excede el umbral crítico de 400 líneas.

- **Partición por Capas (Slice):** Fragmenta de manera determinista volúmenes grandes de código en micro-commits de un máximo de 400 líneas, organizando el historial en cascada técnica `Config` -&gt; `Backend` -&gt; `Frontend` -&gt; `Tests`).

- **Patrón AgentAdapter:** Desacopla las inteligencias. Consume las APIs de tus suscripciones locales sin consumir tokens extra de API Keys.

- **Configuración YAML:** Modifica el modelo de lenguaje y el nivel de razonamiento (*reasoning effort*) de cada agente al vuelo desde el archivo `.vassentinel.yml`.

## Comandos Disponibles

- `sentinel init`: Inyecta las directivas de detención en los prompts de tus agentes locales e instala el Git Hook global `pre-commit`.

- `sentinel check`: Realiza una auditoría matemática en microsegundos del volumen de líneas del Worktree activo.

- `sentinel slice`: Agrupa y procesa de forma secuencial la cola de modificaciones locales, delegando la semántica del mensaje de commit a tu `AgentAdapter` configurado.

## Control de Adaptadores (Variables de Entorno)

Cambia de agente instantáneamente en tu terminal Zsh/Bash antes de trocear tu código:

```bash

# Para usar Claude Pro por suscripción (Por defecto)

export MY_SUB_AGENT="claude"

# Para cambiar al panel de OpenCode con DeepSeek Gratuito

export MY_SUB_AGENT="opencode"

```

