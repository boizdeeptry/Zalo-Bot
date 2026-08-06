//go:build windows

package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"unsafe"

	"golang.org/x/sys/windows"
)

// providerSecretEntropy là entropy phụ gắn cứng vào ứng dụng.
//
// Không có nó, bất kỳ tiến trình nào chạy dưới cùng tài khoản Windows chỉ cần đọc cột
// credential_cipher rồi gọi CryptUnprotectData là mở được khoá. Có nó, kẻ đọc trộm database còn
// phải biết thêm chuỗi này. Đây KHÔNG phải một bí mật — nó nằm ngay trong binary — nó chỉ buộc
// việc giải mã phải cố ý nhắm vào ứng dụng này thay vì là tác dụng phụ của việc chạy cùng tài
// khoản. Đổi chuỗi = mọi khoá đang lưu thành không đọc được, nên hậu tố phiên bản ở đây là để
// lần sau còn đổi được một cách có ý thức.
var providerSecretEntropy = []byte("agentdc/provider-credential/v1")

// protectProviderSecret mã hoá khoá API bằng DPAPI, buộc vào đúng tài khoản Windows đang chạy.
//
// CRYPTPROTECT_UI_FORBIDDEN vì daemon là dịch vụ nền: nếu DPAPI cần hỏi người dùng thì nó phải
// hỏng ngay tại đây, chứ không phải treo một hộp thoại không ai nhìn thấy giữa một lượt trả lời.
func protectProviderSecret(plain []byte) ([]byte, error) {
	if len(plain) == 0 || uint64(len(plain)) > math.MaxUint32 {
		return nil, errors.New("protect provider credential: khoá rỗng hoặc dài quá mức")
	}
	in := newDataBlob(plain)
	entropy := newDataBlob(providerSecretEntropy)
	var out windows.DataBlob
	if err := windows.CryptProtectData(
		&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("protect provider credential: %w", err)
	}
	return takeDataBlob(&out), nil
}

// unprotectProviderSecret mở khoá API ra bản rõ, chỉ thành công dưới đúng tài khoản đã mã hoá.
//
// MỌI thất bại đều thành ErrCredentialUnreadable và errno của Windows bị bỏ hẳn: "data invalid",
// "key not found" và "sai người dùng" dẫn tới cùng một việc phải làm — nhập lại API key — nên
// giữ lại chuỗi lỗi hệ thống chỉ thêm một chuỗi nữa phải soi mà không đổi được gì cho người
// dùng. (Bản thân syscall.Errno là một con số, không mang được byte khoá; thứ phải cân nhắc là
// câu chữ Windows dựng quanh nó.)
func unprotectProviderSecret(cipher []byte) ([]byte, error) {
	// Ciphertext dài quá mức cũng là không đọc được: uint32 bên dưới sẽ cắt cụt nó, và một blob
	// bị cắt thì DPAPI từ chối — nói thẳng ở đây đỡ phải đoán qua errno.
	if len(cipher) == 0 || uint64(len(cipher)) > math.MaxUint32 {
		return nil, fmt.Errorf("unprotect provider credential: %w", ErrCredentialUnreadable)
	}
	in := newDataBlob(cipher)
	entropy := newDataBlob(providerSecretEntropy)
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(
		&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, fmt.Errorf("unprotect provider credential: %w", ErrCredentialUnreadable)
	}
	return takeDataBlob(&out), nil
}

func newDataBlob(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// takeDataBlob chép nội dung blob do DPAPI cấp phát sang bộ nhớ Go, xoá sạch vùng cũ rồi trả nó
// lại cho hệ điều hành.
//
// Xoá kể cả khi blob đang là bản mã: nhánh giải mã trả BẢN RÕ về trong chính vùng nhớ này, và
// một helper chỉ-xoá-đôi-khi là một helper sẽ có ngày bị gọi ở nhánh còn lại. LocalFree không
// xoá giúp — nó trả heap về nguyên trạng, để bản rõ nằm đó chờ lần cấp phát sau đọc trúng.
func takeDataBlob(b *windows.DataBlob) []byte {
	raw := unsafe.Slice(b.Data, b.Size)
	out := bytes.Clone(raw)
	clear(raw)
	// Lỗi LocalFree không xử lý được gì: dữ liệu đã chép xong và đã xoá xong, còn một handle
	// hỏng ở đây nghĩa là tiến trình đã hỏng từ trước lời gọi này.
	windows.LocalFree(windows.Handle(unsafe.Pointer(b.Data))) //nolint:errcheck
	return out
}
