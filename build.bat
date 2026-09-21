@echo off
REM vcSentinel - format and build script
setlocal

cd /d "%~dp0"

set VERSION=0.1.0
if exist release.yml (
    for /f "usebackq tokens=1,* delims=:" %%a in (`findstr /b /c:"version:" release.yml`) do set "VERSION=%%b"
)
set "VERSION=%VERSION: =%"
set "VERSION=%VERSION:"=%"
if defined VCSENTINEL_VERSION set VERSION=%VCSENTINEL_VERSION%

echo === gofmt ===
gofmt -w .
if errorlevel 1 goto :fail

echo === go vet ===
go vet ./...
if errorlevel 1 goto :fail

echo === build ===
if not exist "bin\%VERSION%" mkdir "bin\%VERSION%"
go build -ldflags="-s -w -X main.version=%VERSION%" -o "bin\%VERSION%\vcsentinel.exe" ./cmd/vcsentinel
if errorlevel 1 goto :fail

echo.
echo OK: bin\%VERSION%\vcsentinel.exe generated
exit /b 0

:fail
echo.
echo ERROR: build failed
exit /b 1
