@echo off
setlocal
set "ROOT=%~dp0.."
set "PYTHON=%ROOT%\.venv\Scripts\python.exe"

if not exist "%PYTHON%" (
  echo [FAIL] 未找到项目虚拟环境: %PYTHON%
  exit /b 2
)

pushd "%ROOT%"
"%PYTHON%" qa\run.py --all --ci --no-report
if errorlevel 1 goto :failed

"%PYTHON%" qa\next.py --ci
if errorlevel 1 goto :failed

popd
echo [OK] 全部质量门禁通过
exit /b 0

:failed
set "CODE=%ERRORLEVEL%"
popd
echo [FAIL] 质量门禁失败，退出码 %CODE%
exit /b %CODE%
