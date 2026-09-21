@echo off
REM vcSentinel - release script (Windows)
REM Genera los assets multiplataforma y publica la release en GitHub.
REM La versión se lee de release.yml (fuente de verdad); se puede forzar con SENTINEL_VERSION.
setlocal
cd /d "%~dp0\.."

set VERSION=0.1.0
if exist release.yml (
    for /f "usebackq tokens=1,* delims=:" %%a in (`findstr /b /c:"version:" release.yml`) do set "VERSION=%%b"
)
set "VERSION=%VERSION: =%"
set "VERSION=%VERSION:"=%"
if defined SENTINEL_VERSION set VERSION=%SENTINEL_VERSION%

echo === go vet ===
go vet ./...
if errorlevel 1 goto :fail

echo === generate multiplatform assets ===
go run ./tools/release
if errorlevel 1 goto :fail

echo === publish GitHub release v%VERSION% ===
gh release create "v%VERSION%" "bin\%VERSION%\vcsentinel-windows-amd64.exe" "bin\%VERSION%\vcsentinel-linux-amd64" "bin\%VERSION%\vcsentinel-linux-arm64" --title "vcSentinel v%VERSION%" --generate-notes
if errorlevel 1 goto :fail

echo.
echo OK: release v%VERSION% published
exit /b 0

:fail
echo.
echo ERROR: release failed
exit /b 1
