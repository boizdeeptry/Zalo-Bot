@echo off
rem Stop.bat — dung phan mem.
rem
rem Can no vi cua so bi an: khong co gi de dong bang chuot.
for %%I in ("%~dp0.") do set "ROOT=%%~fI"
set "AGENTDC_HOME=%ROOT%\data"
set "AGENTDC_PORT=8770"
"%ROOT%\app\agentdc.exe" daemon stop --force
echo.
echo Da dung. Nhan phim bat ky de dong cua so nay.
pause >nul
