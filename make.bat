@echo off
setlocal EnableDelayedExpansion

set CMD=%~1
set BINARY=bin\rago.exe
set COVER_OUT=coverage.out
set COVER_HTML=coverage.html

:: Packages that can be unit-tested without a running database.
:: The postgres package (Connect/Migrate/repository) requires an integration DB — excluded here.
set UNIT_PKGS=rago/internal/handler rago/internal/lmstudio rago/internal/service rago/internal/postgres

if "%CMD%"==""             goto help
if "%CMD%"=="build"        goto build
if "%CMD%"=="run"          goto run
if "%CMD%"=="test"         goto test
if "%CMD%"=="coverage"     goto coverage
if "%CMD%"=="cover-html"   goto cover_html
if "%CMD%"=="vet"          goto vet
if "%CMD%"=="clean"        goto clean
if "%CMD%"=="all"          goto all
goto help

:: ── build ──────────────────────────────────────────────────────────────────────
:build
echo [build] Compiling...
if not exist bin mkdir bin
go build -o %BINARY% .
if %errorlevel% neq 0 ( echo [build] FAILED & exit /b 1 )
echo [build] OK ^-^- %BINARY%
goto end

:: ── run ────────────────────────────────────────────────────────────────────────
:run
echo [run] Starting application (reads .env automatically)...
go run .
goto end

:: ── test ───────────────────────────────────────────────────────────────────────
:test
echo [test] Running unit tests...
go test ./internal/...
if %errorlevel% neq 0 ( echo [test] FAILED & exit /b 1 )
echo [test] OK
goto end

:: ── coverage ───────────────────────────────────────────────────────────────────
:coverage
echo [coverage] Running unit tests with coverage...
go test -coverprofile=%COVER_OUT% -covermode=atomic ./internal/...
if %errorlevel% neq 0 ( echo [coverage] FAILED & exit /b 1 )
echo.
echo [coverage] Per-package summary:
go tool cover -func=%COVER_OUT% | findstr /r "^rago.*\.go"
echo.
go tool cover -func=%COVER_OUT% | findstr "^total"
echo.
echo [coverage] Note: postgres DB methods (Connect/Migrate/repository) require
echo [coverage] a running database and are excluded from unit test coverage.
goto end

:: ── cover-html ─────────────────────────────────────────────────────────────────
:cover_html
echo [cover-html] Generating HTML coverage report...
go test -coverprofile=%COVER_OUT% -covermode=atomic ./internal/...
if %errorlevel% neq 0 ( echo [cover-html] FAILED & exit /b 1 )
go tool cover -html=%COVER_OUT% -o %COVER_HTML%
if %errorlevel% neq 0 ( echo [cover-html] Failed to generate HTML & exit /b 1 )
echo [cover-html] Report written to %COVER_HTML%
start %COVER_HTML%
goto end

:: ── vet ────────────────────────────────────────────────────────────────────────
:vet
echo [vet] Running go vet...
go vet ./...
if %errorlevel% neq 0 ( echo [vet] FAILED & exit /b 1 )
echo [vet] OK
goto end

:: ── clean ──────────────────────────────────────────────────────────────────────
:clean
echo [clean] Removing build artifacts...
if exist bin          rmdir /s /q bin
if exist %COVER_OUT%  del /q %COVER_OUT%
if exist %COVER_HTML% del /q %COVER_HTML%
echo [clean] OK
goto end

:: ── all ────────────────────────────────────────────────────────────────────────
:all
echo [all] vet ^> test ^> build
call "%~f0" vet
if %errorlevel% neq 0 exit /b 1
call "%~f0" test
if %errorlevel% neq 0 exit /b 1
call "%~f0" build
if %errorlevel% neq 0 exit /b 1
echo [all] Done.
goto end

:: ── help ───────────────────────────────────────────────────────────────────────
:help
echo.
echo  Usage:  make.bat ^<command^>
echo.
echo  Commands:
echo    build        Compile the application to bin\rago.exe
echo    run          Run the application (loads .env automatically)
echo    test         Run all unit tests
echo    coverage     Run tests and print per-function coverage summary
echo    cover-html   Run tests and open HTML coverage report in the browser
echo    vet          Run go vet on all packages
echo    clean        Remove bin\, coverage.out and coverage.html
echo    all          Run vet + test + build in sequence
echo.
echo  Coverage targets (unit tests, no DB required):
echo    handler  ~98%%   lmstudio  ~91%%   service  ~80%%
echo.
goto end

:end
endlocal
