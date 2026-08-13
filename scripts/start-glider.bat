@echo off
setlocal
cd /d "%~dp0"

set "GLIDER_CA=%USERPROFILE%\.glider\mitm\ca.crt"
set "NODE_EXTRA_CA_CERTS=%GLIDER_CA%"

if not exist "glider.exe" (
  echo Building glider.exe...
  where go >nul 2>&1
  if errorlevel 1 (
    echo Go not found on PATH. Build manually then re-run.
    exit /b 1
  )
  go build -o glider.exe ./cmd/glider
)

echo Starting Glider...
echo   Gateway:  http://localhost:8080/v1
echo   MITM:     http://127.0.0.1:8082
echo   Dashboard: http://localhost:8081
echo   CA:       %GLIDER_CA%
echo.
glider.exe --config configs\glider.yaml
