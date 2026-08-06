//go:build !windows

package daemon

import "fmt"

// Ngoài Windows không có kho khoá nào tương đương DPAPI mà không kéo thêm phụ thuộc, nên hai
// hàm dưới đây từ chối thẳng thay vì bịa ra một lớp mã hoá tự chế.
//
// Từ chối chứ KHÔNG lưu thô: một bản build Linux vẫn cần compile để `go build` mọi GOOS còn
// xanh, và ở đó cách hỏng nguy hiểm nhất là im lặng nhận khoá rồi cất nguyên văn xuống SQLite.
// Người gọi thấy ErrCredentialUnsupported thì biết ngay việc cần làm là bổ sung một kho khoá
// cho nền tảng đó, chứ không phải đi tìm xem khoá của mình đang nằm ở đâu.

func protectProviderSecret(plain []byte) ([]byte, error) {
	return nil, fmt.Errorf("protect provider credential: %w", ErrCredentialUnsupported)
}

func unprotectProviderSecret(cipher []byte) ([]byte, error) {
	return nil, fmt.Errorf("unprotect provider credential: %w", ErrCredentialUnsupported)
}
