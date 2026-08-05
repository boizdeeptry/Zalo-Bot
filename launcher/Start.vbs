' Start.vbs — nhan doi tep nay de mo phan mem.
'
' Viec duy nhat no lam la chay app\run.bat voi cua so AN. Tham so 0 la "khong hien
' cua so".
'
' Vi sao can no: nhan doi mot .bat mo mot cua so den, va cua so do phai o lai suot
' vi daemon chay trong no. Voi mot phan mem ban ra thi do trong nhu loi.
'
' Khong dung Electron hay mot trinh dong goi nao: chung them hang tram MB va mot
' vong build, de giai quyet dung mot dong nay.
Dim fso, sh, here
Set fso = CreateObject("Scripting.FileSystemObject")
Set sh  = CreateObject("WScript.Shell")
here = fso.GetParentFolderName(WScript.ScriptFullName)
sh.Run """" & here & "\app\run.bat""", 0, False
