@echo off
REM VAS Sentinel - format and build script
setlocal

cd /d "%~dp0"

echo === gofmt ===
gofmt -w .
if errorlevel 1 goto :fail

echo === go vet ===
go vet ./...
if errorlevel 1 goto :fail

echo === build ===
go build -ldflags="-s -w" -o sentinel.exe cmd/main.go
if errorlevel 1 goto :fail

echo.
echo OK: sentinel.exe generated
exit /b 0

:fail
echo.
echo ERROR: build failed
exit /b 1
