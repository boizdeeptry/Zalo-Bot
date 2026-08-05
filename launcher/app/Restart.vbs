' Restart.vbs — de phan mem tu mo lai chinh no.
'
' Daemon chay tep nay roi TU TAT. Tep nay cho 3 giay cho cong 8770 duoc nha ra,
' roi goi Start.vbs. Nen nguoi dung chi thay trang tu tai lai, khong phai dong mo
' gi bang tay.
'
' Vi sao mot tep VBS chu khong `cmd /c timeout & ...`: cmd nhay mot cua so den len
' giua man hinh, va voi mot phan mem ban ra thi mot cua so den nhay len doc nhu
' virus. wscript chay khong cua so.
'
' Vi sao cho 3 giay: daemon phai dong listener va tat transport truoc khi tien
' trinh moi bind duoc cong. Ngan hon thi ban moi khoi dong that bai vi cong con bi
' giu, va nguoi dung mat ca phan mem thay vi doi mot muc cau hinh.
Dim fso, sh, here
Set fso = CreateObject("Scripting.FileSystemObject")
Set sh  = CreateObject("WScript.Shell")
' Goi run.bat TRUC TIEP voi "nobrowser", khong qua Start.vbs.
'
' Vi sao khong Start.vbs: no chay run.bat khong tham so, tuc mo mot tab moi o
' /zalo. Nhung luc khoi dong lai thi nguoi dung DANG mo mot tab va tab do tu tai
' lai -- them mot tab nua la ho co hai site giong nhau.
here = fso.GetParentFolderName(WScript.ScriptFullName)
WScript.Sleep 3000
sh.Run """" & here & "\run.bat"" nobrowser", 0, False
