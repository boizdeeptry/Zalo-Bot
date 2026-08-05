package daemon

// Tự mở lại. CHỈ có trong bản đóng gói.
//
// Vì sao cần: mô hình được truyền cho `claude` bằng tham số dòng lệnh, dựng từ biến môi trường đọc
// ĐÚNG MỘT LẦN lúc daemon khởi động. Nên đổi mô hình mà không khởi động lại thì không có hiệu lực.
//
// Cách SAI, và là cách bản đầu làm: hiện một dòng "có hiệu lực khi mở lại phần mềm" rồi để người
// dùng tự đóng tự mở. Với người vận hành không chuyên thì đó là một câu đố, và cái giá của việc
// đoán sai là tưởng phần mềm hỏng.
//
// Cách này: ghi cấu hình, chạy Restart.vbs rồi tự tắt. Restart.vbs chờ 3 giây cho cổng được nhả
// rồi gọi Start.vbs. Trang portal thăm dò tới khi máy chủ trả lời lại rồi tự tải lại.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// envAppRoot là thư mục gốc của gói, nơi có Start.vbs. run.bat đặt biến này.
//
// Không suy ra từ os.Executable(): binary nằm trong app\, nên suy ra được — nhưng nó sẽ đúng chỉ
// với bố cục hiện tại và sai lặng lẽ nếu bố cục đổi. Một biến tường minh thì hỏng thành một thông
// báo đọc được.
const envAppRoot = "AGENTDC_APP_ROOT"

// restartDelay là khoảng daemon còn sống sau khi đã hẹn Restart.vbs.
//
// Đủ để trả lời HTTP đang dở cho portal biết việc đã nhận. Ngắn hơn thì trang nhận một kết nối bị
// cắt và hiện lỗi, dù việc khởi động lại vẫn diễn ra đúng.
const restartDelay = 600 * time.Millisecond

// scheduleRestart hẹn khởi động lại rồi trả về ngay.
//
// Trả lỗi thay vì tự xoay: không tìm được Restart.vbs thì thà nói ra và giữ nguyên phần mềm đang
// chạy, hơn là tự tắt rồi không có gì bật nó lên.
func (a *api) scheduleRestart() error {
	root := os.Getenv(envAppRoot)
	if root == "" {
		return fmt.Errorf("chưa đặt %s nên không tự mở lại được", envAppRoot)
	}
	vbs := filepath.Join(root, "app", "Restart.vbs")
	if _, err := os.Stat(vbs); err != nil {
		return fmt.Errorf("không thấy %s", vbs)
	}
	// wscript, không cscript: cscript mở một cửa sổ console.
	cmd := exec.Command("wscript.exe", vbs)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("chạy Restart.vbs: %w", err)
	}
	// KHÔNG Wait: tiến trình này sắp chết, và Wait sẽ chặn tới khi Restart.vbs xong — tức tới sau
	// khi nó đã gọi Start.vbs, và lúc đó cổng vẫn do ta giữ nên bản mới không bind được.
	//
	// Không Wait nghĩa là để lại một tiến trình zombie, và điều đó không sao ĐÚNG Ở ĐÂY: tiến
	// trình cha biến mất sau nửa giây, và Windows không giữ bảng con như Unix.
	go func() {
		time.Sleep(restartDelay)
		a.logger.Info("tự mở lại theo yêu cầu từ trang cấu hình")
		a.shutdown()
	}()
	return nil
}
