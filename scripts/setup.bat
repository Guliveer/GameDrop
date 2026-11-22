@echo off
REM First-time setup for GameDrop: asks a few questions, writes .env, checks your
REM Telegram bot, and can run the first collection cycle.
REM
REM Double-click this file, or run: scripts\setup.bat
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
    echo GameDrop needs Go to run from source, and it is not installed yet.
    echo.
    echo   1. The download page will open in your browser ^(https://go.dev/dl/^).
    echo   2. Install it with the default options, then close this window.
    echo   3. Open a new Command Prompt in this folder and run scripts\setup.bat again.
    echo.
    start "" "https://go.dev/dl/"
    pause
    exit /b 1
)

go run ./cmd/gamedrop --setup %*
echo.
pause
