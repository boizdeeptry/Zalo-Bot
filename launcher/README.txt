════════════════════════════════════════════════════════════════════════════════
  CAI DAT - DOC HET TRANG NAY TRUOC KHI CHAY
════════════════════════════════════════════════════════════════════════════════

Ca thu muc nay LA phan mem. Khong co trinh cai dat, khong ghi gi vao Registry,
khong dat gi ngoai thu muc nay. Copy no di dau cung chay, ke ca USB.

Go bo = xoa thu muc.


────────────────────────────────────────────────────────────────────────────────
 CHAY
────────────────────────────────────────────────────────────────────────────────

  Nhan doi   "Start.vbs"

Sau khoang 4 giay, Start.vbs mo Portal quan ly tai http://127.0.0.1:8770/.
Day la trang quan ly, khong phai trang Zalo. Lan chay dau chua co Provider,
hay route; hay lam phan THIET LAP BOT ben duoi truoc khi ket noi Zalo.

  Dung lai:  nhan doi "Stop.bat"

Khong co cua so nao hien ra khi chay -- do la co y. Muon xem no co song khong
thi mo http://127.0.0.1:8770/


────────────────────────────────────────────────────────────────────────────────
 THIET LAP BOT TRUOC KHI KET NOI ZALO
────────────────────────────────────────────────────────────────────────────────

Lam theo DUNG thu tu nay trong Portal:

  1. Chon Provider. Bat ON roi bam Connect hoac Bam de cai tren dung dong do.
     Co the bat nhieu Provider; Portal cai tung dong va bao dong nao dang cho.

  2. Khi Provider cuoi cung san sang, Portal tu hien "Dang chuan bi tro ly" va tu hoan tat.
     Khong co buoc Persona, Test Chat hay Complete de bam.

  3. Khi Portal bao "San sang", vao Portal de quan ly hoac mo trang Zalo de ket
     noi bang ma QR.

Sau khi bot San sang, trang Knowledge la tuy chon; tri thuc trong do khong chan Done.
Neu cai dat hoac chuan bi that bai, he thong khong tao route; bot co y im lang.
Portal va Zalo van co the mo, nhung bot se khong tu y roi ve mot Provider khac.


────────────────────────────────────────────────────────────────────────────────
 TRI THUC NGANH: THU MUC brain\ DA TO CHUC SAN, NHUNG RONG
────────────────────────────────────────────────────────────────────────────────

Bot di kem CACH NOI, khong di kem tri thuc nganh cua nguoi ban. Nen brain\ giao
day du cau truc va tai lieu, nhung khong co noi dung:

  brain\
    raw\              <- TEP NGUON, khong sua. PDF, bai viet, ghi chu, transcript
      assets\            anh va tep dinh kem
    wiki\             <- TRANG DA BIEN SOAN. Day la thu bot doc de tra loi
      sources\           tom tat tung nguon
      entities\          nguoi, san pham, to chuc
      concepts\          khai niem
      topics\            chu de tong hop nhieu nguon
      analyses\          so sanh, phan tich
    reference\        <- cho NGUOI doc, bot KHONG trich dan tu day
      methodology.md     phuong phap
      persona\           van phong cua bot
    CLAUDE.md         <- schema: quy uoc dat ten, viet trang the nao
    index.md          <- muc luc: trang nao dang co
    log.md            <- so tay: da nap gi, ngay nao
    Home.md, README.md

HAI TANG, va phan biet duoc chung la quan trong:

  raw\    la thu ban BO VAO. Khong sua, khong xoa. Bot doc duoc nhung no la van
          ban tho, dai, lan.
  wiki\   la thu da BIEN SOAN thanh trang ngan, mot chu de mot trang. Bot tra
          loi tot nhat tu tang nay.

CACH DI TU raw\ SANG wiki\:

UU TIEN dung luong da dong goi: mo trang Knowledge trong Portal, upload tep va
chay ingest. Portal se dung runtime duoc quan ly; khong can lenh Claude global
de nap tri thuc hoac de bot tra loi Zalo.

Cach Command Prompt duoi day la TUY CHON va tach rieng. No chi chay neu ban da
co san lenh  claude  global tren PATH:

  1. Bo tep nguon vao  brain\raw\
  2. Mo Command Prompt, go:   cd /d "duong-dan-app\brain"   roi   claude
  3. Noi: "doc raw\ va viet trang wiki cho nhung nguon moi"

Claude do Portal quan ly khong duoc them lau dai vao PATH cua shell nguoi dung.
Vi vay, bam Connect thanh cong khong co nghia lenh  claude  se xuat hien trong
Command Prompt. Khong cai Claude global cung khong anh huong onboarding cua bot.

CLAUDE.md trong thu muc do la ban chi dan san co cho viec nay: no noi ro dat ten
trang the nao, moi trang gom gi, cap nhat index.md va log.md ra sao. Khong phai
tu bay ra quy uoc.

DE RONG THI SAO: bot van chay, van dung van phong, van tra loi -- chi la khong co
du kien nganh de dan, nen no tra loi chung chung va noi thang la chua co can cu.
Do la hanh vi dung, khong phai loi.

Bot doc duoc ngay khi co tep moi, KHONG can khoi dong lai.


────────────────────────────────────────────────────────────────────────────────
 VAN PHONG: DA DONG GOI SAN
────────────────────────────────────────────────────────────────────────────────

Goi da co Persona hoan chinh de chay ngay; khong can dien ten hay cau hinh tri
thuc truoc lan chay dau. Neu muon doi cach noi sau nay, dung trang Agents trong
Portal de sua co kiem soat.

  reference\persona\roster.md   so tay nhan dien thanh vien. Tuy chon, de rong duoc.
  reference\persona\overlay\    luat rieng cho tung nhom. Doc README trong do.

Vi sao persona nam trong reference\ chu khong trong wiki\: mot tep trong wiki\ hay
raw\ thi bot TRICH DAN duoc no. Van phong khong phai can cu de tra loi khach, nen
no phai o ngoai hai thu muc do. Do la mot ranh gioi, khong phai mot cach xep tep.


────────────────────────────────────────────────────────────────────────────────
 CANH BAO CUA WINDOWS
────────────────────────────────────────────────────────────────────────────────

Lan dau chay, Windows co the hien "Windows protected your PC". Do la vi phan
mem chua mua chung chi ky so, khong phai vi no co van de.

  Bam  More info  ->  Run anyway

Chi can lam mot lan.


────────────────────────────────────────────────────────────────────────────────
 DU LIEU CUA BAN NAM O DAU
────────────────────────────────────────────────────────────────────────────────

  data\agentdc.db        hoi thoai, danh ba, ghi chu bot tu viet
  data\zalo\             phien dang nhap Zalo -- COI NHU MAT KHAU
  data\zalo-files\       tep khach gui, tu xoa sau 7 ngay
  data\logs\             ban ghi

Sao luu = copy thu muc  data\  . Chuyen may = copy ca thu muc app.

Ai doc duoc  data\zalo\  thi vao duoc Zalo cua ban. Dung dua thu muc do cho ai.


────────────────────────────────────────────────────────────────────────────────
 MOT DIEU VE BAO MAT, NOI THANG
────────────────────────────────────────────────────────────────────────────────

Phan mem chi nghe o 127.0.0.1, tuc khong may nao khac trong mang vao duoc.

Nhung tren CHINH may nay, portal mo ma khong hoi mat khau. Do la doi lay viec
khong phai lam thu tuc dang nhap moi lan. Tren may mot nguoi dung thi khong mat
gi. Neu may nay co nhieu tai khoan Windows va ban khong muon tai khoan khac doc
duoc hoi thoai Zalo, thi mo  app\run.bat  bang Notepad va xoa dong:

  set "AGENTDC_PORTAL_OPEN=1"

Sau do vao portal phai chay  agentdc.exe portal  moi lan.
