@echo off
REM Run one GameDrop collection cycle from the repository root.
REM
REM   scripts\run.bat                  uses .env (or real environment variables)
REM   set ENV_FILE_PATH=other.env ^& scripts\run.bat
REM
REM Variables already present in the environment win over .env entries.
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
    echo gamedrop: Go is required ^(1.27+^). Install it from https://go.dev/dl/ 1>&2
    exit /b 1
)

if not defined ENV_FILE_PATH if not exist ".env" (
    echo gamedrop: note: .env not found; using the current environment and defaults 1>&2
)

go run ./cmd/gamedrop %*
exit /b %errorlevel%
