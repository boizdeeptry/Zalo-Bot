//go:build windows

package daemon

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestProviderSecretRequiresApplicationEntropy kiểm rằng entropy phụ thật sự nằm trong đường
// mã hoá, chứ không chỉ nằm trong lời bình luận nói rằng nó có.
//
// Không có test này thì bỏ hẳn entropy đi vẫn xanh cả bộ: round-trip vẫn chạy vì cùng một mã
// bỏ nó ở cả hai đầu. Nhưng đó đúng là ranh giới app_secret_windows.go tự nhận mình đang giữ —
// khác nhau giữa "mọi tiến trình dưới cùng tài khoản Windows đọc được cột credential_cipher là
// mở được khoá" và "phải cố ý nhắm vào ứng dụng này". Ở đây dựng lại đúng kẻ tấn công đó: một
// lời gọi CryptUnprotectData không entropy, và nó phải thất bại.
func TestProviderSecretRequiresApplicationEntropy(t *testing.T) {
	key := []byte("sk-entropy-probe")
	cipher, err := protectProviderSecret(key)
	if err != nil {
		t.Fatalf("protectProviderSecret(%q) error = %v; want nil", key, err)
	}

	in := newDataBlob(cipher)
	var out windows.DataBlob
	err = windows.CryptUnprotectData(
		&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	if err == nil {
		// Dọn sạch trước khi báo hỏng: takeDataBlob xoá bản rõ vừa lộ ra rồi trả bộ nhớ lại.
		takeDataBlob(&out)
		t.Fatal("CryptUnprotectData không entropy đã mở được ciphertext; entropy không nằm trong đường mã hoá")
	}
}
