import io
import sys

# 1. run.bat: bo qua viec mo trinh duyet khi duoc goi voi tham so nobrowser.
P = r'F:\dist\_build\launcher\app\run.bat'
s = io.open(P, encoding='utf-8').read()
OLD = """rem Mo trinh duyet sau 4 giay. Dong duoi cung CHAN cho toi khi daemon dung, nen
rem viec mo trinh duyet phai di truoc va o mot tien trinh khac.
start "" /min cmd /c "timeout /t 4 >nul & start "" http://127.0.0.1:8770/zalo\""""
NEW = """rem Mo trinh duyet sau 4 giay. Dong duoi cung CHAN cho toi khi daemon dung, nen
rem viec mo trinh duyet phai di truoc va o mot tien trinh khac.
rem
rem KHONG mo khi duoc goi voi "nobrowser". Do la duong Restart.vbs dung: luc do
rem nguoi dung DANG mo mot tab, va tab do tu tai lai khi may chu song lai. Mo them
rem mot tab nua thi ho co hai site giong nhau va khong biet nhin cai nao -- va cai
rem tab cu con o dung muc Models ho vua bam.
if /i "%~1"=="nobrowser" goto skipbrowser
start "" /min cmd /c "timeout /t 4 >nul & start "" http://127.0.0.1:8770/zalo"
:skipbrowser"""
if OLD not in s:
    sys.exit('khong khop khoi mo trinh duyet trong run.bat')
io.open(P, 'w', encoding='utf-8', newline='\r\n').write(s.replace(OLD, NEW, 1))
print('run.bat: them duong nobrowser')

# 2. Restart.vbs: goi run.bat nobrowser, khong goi Start.vbs.
P2 = r'F:\dist\_build\launcher\app\Restart.vbs'
s2 = io.open(P2, encoding='utf-8').read()
OLD2 = '''here = fso.GetParentFolderName(fso.GetParentFolderName(WScript.ScriptFullName))
WScript.Sleep 3000
sh.Run """" & here & "\\Start.vbs""", 0, False'''
NEW2 = '''\' Goi run.bat TRUC TIEP voi "nobrowser", khong qua Start.vbs.
\'
\' Vi sao khong Start.vbs: no chay run.bat khong tham so, tuc mo mot tab moi o
\' /zalo. Nhung luc khoi dong lai thi nguoi dung DANG mo mot tab va tab do tu tai
\' lai -- them mot tab nua la ho co hai site giong nhau.
here = fso.GetParentFolderName(WScript.ScriptFullName)
WScript.Sleep 3000
sh.Run """" & here & "\\run.bat"" nobrowser", 0, False'''
if OLD2 not in s2:
    sys.exit('khong khop Restart.vbs')
io.open(P2, 'w', encoding='utf-8', newline='\r\n').write(s2.replace(OLD2, NEW2, 1))
print('Restart.vbs: goi run.bat nobrowser')
