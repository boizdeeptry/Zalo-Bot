@echo off
rem ============================================================================
rem  run.bat — dat cau hinh roi chay daemon. Start.vbs goi tep nay.
rem
rem  KHONG nhan doi tep nay: no mo mot cua so den o lai suot. Dung Start.vbs.
rem
rem  Moi duong dan duoi day TUONG DOI theo thu muc app: copy ca thu muc sang may
rem  khac, hoac sang o khac, va no chay khong sua gi.
rem
rem  Ten tep trong brain\reference\persona la ASCII co chu dich: .bat doc theo
rem  codepage OEM chu khong UTF-8, nen mot duong dan tieng Viet o day se bien dang
rem  va daemon khong doc duoc persona. Da tung gap dung loi do.
rem ============================================================================

rem ROOT la thu muc cha, duoc chuan hoa thanh duong dan tuyet doi. Dung "%~dp0.."
rem tho thi moi duong dan sinh ra mang mot ".." o giua, va no hien nhu the trong
rem log va trong moi thong bao loi.
for %%I in ("%~dp0..") do set "ROOT=%%~fI"
cd /d "%ROOT%"

rem Goc goi, cho trang cau hinh biet Restart.vbs o dau khi no tu mo lai phan mem.
set "AGENTDC_APP_ROOT=%ROOT%"

set "AGENTDC_HOME=%ROOT%\data"
set "AGENTDC_PORT=8770"

rem Portal mo: khong phai chay `agentdc portal`, khong co link het han.
rem Bo dong nay la quay ve co che cookie mot-lan.
set "AGENTDC_PORTAL_OPEN=1"

rem Tri thuc nganh. HAI thu muc nay giao cho ban rong -- do bo vao day thi bot tra
rem loi duoc bang tri thuc cua ban. De rong thi bot van chay, van dung van phong,
rem chi la khong co du kien nganh de dan.
set "AGENTDC_ZALO_KB_DIRS=%ROOT%\brain\wiki;%ROOT%\brain\raw"

rem Van phong va nhan dien. KHONG duoc nam trong hai thu muc tren: mot tep nam
rem trong goc KB thi bot trich dan duoc no, va van phong khong phai can cu.
set "AGENTDC_ZALO_PERSONA=%ROOT%\brain\reference\persona\persona.md"
set "AGENTDC_ZALO_ROSTER=%ROOT%\brain\reference\persona\roster.md"
set "AGENTDC_ZALO_OVERLAY_DIR=%ROOT%\brain\reference\persona\overlay"
set "AGENTDC_ZALO_PERSONA_DEFAULT_DIR=%ROOT%\app\defaults\persona"

rem Thu muc brain, cho trang Knowledge biet no upload vao dau va bien soan o dau.
rem Tuong minh chu khong suy ra tu KB_DIRS: suy ra cha chung cua hai duong dan thi
rem dung voi bo cuc nay va sai lang le voi moi bo cuc khac.
set "AGENTDC_BRAIN_DIR=%ROOT%\brain"

rem Mo hinh: doc tu data\model.txt neu trang Models da luu mot lua chon.
rem
rem Vi sao mot tep text chu khong mot bang trong database: mo hinh duoc truyen cho
rem claude bang tham so dong lenh, va tham so do dung tu bien moi truong doc DUNG
rem MOT LAN luc daemon khoi dong. Nen no phai duoc dat O DAY, truoc khi daemon
rem chay -- va `set /p` doc mot dong tu tep la cach duy nhat .bat lam duoc viec do.
rem
rem Khong co tep thi giu mac dinh cua daemon (haiku).
if exist "%ROOT%\data\model.txt" set /p AGENTDC_ZALO_MODEL=<"%ROOT%\data\model.txt"

set "AGENTDC_ZALO_WORKDIR=%ROOT%\data\zalo-work"
set "AGENTDC_ZALO_TRANSPORT=%~dp0transport"

rem node.exe + npm di kem, khong can cai Node. Chen len dau PATH nen no thang ban
rem node cua may neu may do cai san mot ban khac.
set "PATH=%~dp0node;%PATH%"

rem npm cai codex vao dau, va tim lai o dau. HAI dong nay giu ca hai o TRONG goi:
rem   prefix -> `npm install -g` (buoc "Ket noi" codex trong Portal) cai vao
rem     data\cli, va `npm root -g` cung tra dung do nen daemon tim thay codex.js.
rem     data\ ghi duoc (daemon da ghi SQLite vao do); app\node co the chi-doc.
rem   cache  -> npm khong ghi ra %LocalAppData%\npm-cache ngoai goi. Giu dung loi
rem     hua "khong dat gi ngoai thu muc nay" -- copy ca goi sang USB van chay.
set "npm_config_prefix=%ROOT%\data\cli"
set "npm_config_cache=%ROOT%\data\npm-cache"

rem Mo trinh duyet sau 4 giay. Dong duoi cung CHAN cho toi khi daemon dung, nen
rem viec mo trinh duyet phai di truoc va o mot tien trinh khac.
rem
rem KHONG mo khi duoc goi voi "nobrowser". Do la duong Restart.vbs dung: luc do
rem nguoi dung DANG mo mot tab, va tab do tu tai lai khi may chu song lai. Mo them
rem mot tab nua thi ho co hai site giong nhau va khong biet nhin cai nao -- va cai
rem tab cu con o dung muc Models ho vua bam.
if /i "%~1"=="nobrowser" goto skipbrowser
start "" /min cmd /c "timeout /t 4 >nul & start "" http://127.0.0.1:8770/"
:skipbrowser

rem Duong dan TUONG MINH, khong dua "agentdc.exe" tran.
rem
rem Do duoc: cmd bao 'agentdc.exe' is not recognized du tep nam ngay canh va da cd
rem vao thu muc do. Nguyen nhan la viec cmd tim lenh trong thu muc hien tai co the
rem bi tat bang bien NoDefaultCurrentDirectoryInExePath, va mot so moi truong dat
rem no. Dua ca duong dan thi khong phu thuoc vao dieu do nua.
rem Ghi stdout + stderr cua daemon ra tep.
rem
rem Vi sao can: daemon chay trong cua so AN cua Start.vbs, nen truoc day khi no chet thi
rem KHONG con dau vet nao. Do duoc hai lan trong mot buoi -- daemon tu tat, va cach duy
rem nhat de biet la thay cong 8770 khong tra loi. Log theo tung session van co, nhung log
rem cua chinh daemon thi khong ton tai, nen khong the doc ra vi sao.
rem
rem >> chu khong >: moi lan khoi dong lai ghi TIEP vao cung tep. Ghi de nghia la lan chet
rem thu hai xoa mat bang chung cua lan thu nhat, va do dung la luc can so sanh hai lan.
rem
rem Tep nam trong data\ vi day la thu muc ghi duoc (SQLite da o do) va nam TRONG goi --
rem giu dung loi hua "khong dat gi ngoai thu muc nay".
"%~dp0agentdc.exe" daemon >>"%ROOT%\data\daemon.log" 2>&1
