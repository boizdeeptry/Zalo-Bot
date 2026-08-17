//go:build windows

package daemon

import (
	"encoding/binary"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	openCodeWinTrustDLL               = windows.NewLazySystemDLL("wintrust.dll")
	openCodeWinTrustProviderData      = openCodeWinTrustDLL.NewProc("WTHelperProvDataFromStateData")
	openCodeWinTrustProviderSigner    = openCodeWinTrustDLL.NewProc("WTHelperGetProvSignerFromChain")
	openCodeWinVerifyTrust            = windows.WinVerifyTrustEx
	openCodeWinTrustSignerSubject     = queryOpenCodeWinTrustSignerSubject
	openCodeAuthenticodeSignerSubject = queryOpenCodeAuthenticodeSigner
)

type openCodeLockedBinary struct {
	handle windows.Handle
}

type openCodeLockedSmokeRoot struct {
	handle windows.Handle
}

func (locked *openCodeLockedBinary) Close() error {
	if locked == nil || locked.handle == 0 || locked.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(locked.handle)
	locked.handle = windows.InvalidHandle
	return err
}

func (locked *openCodeLockedSmokeRoot) remove() error {
	if locked == nil || locked.handle == 0 || locked.handle == windows.InvalidHandle {
		return ErrOpenCodeCleanupFailed
	}
	deleteOnClose := byte(1)
	if err := windows.SetFileInformationByHandle(
		locked.handle,
		windows.FileDispositionInfo,
		&deleteOnClose,
		uint32(unsafe.Sizeof(deleteOnClose)),
	); err != nil {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

func (locked *openCodeLockedSmokeRoot) entries() ([]string, error) {
	if locked == nil || locked.handle == 0 || locked.handle == windows.InvalidHandle {
		return nil, ErrOpenCodeCleanupFailed
	}
	const (
		bufferSize       = 64 << 10
		nameLengthOffset = 60
		nameOffset       = 68
		entryLimit       = 4096
	)
	result := make([]string, 0, 4)
	restart := true
	for {
		buffer := make([]byte, bufferSize)
		informationClass := uint32(windows.FileFullDirectoryInfo)
		if restart {
			informationClass = windows.FileFullDirectoryRestartInfo
		}
		err := windows.GetFileInformationByHandleEx(
			locked.handle,
			informationClass,
			&buffer[0],
			uint32(len(buffer)),
		)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, ErrOpenCodeCleanupFailed
		}
		restart = false
		readAny := false
		for offset := 0; ; {
			if offset < 0 || offset+nameOffset > len(buffer) {
				return nil, ErrOpenCodeCleanupFailed
			}
			nextOffset := int(binary.LittleEndian.Uint32(buffer[offset : offset+4]))
			nameByteLength := int(binary.LittleEndian.Uint32(
				buffer[offset+nameLengthOffset : offset+nameLengthOffset+4],
			))
			if nameByteLength <= 0 || nameByteLength%2 != 0 || offset+nameOffset+nameByteLength > len(buffer) {
				return nil, ErrOpenCodeCleanupFailed
			}
			characters := make([]uint16, nameByteLength/2)
			for index := range characters {
				start := offset + nameOffset + index*2
				characters[index] = binary.LittleEndian.Uint16(buffer[start : start+2])
				if characters[index] == 0 {
					return nil, ErrOpenCodeCleanupFailed
				}
			}
			name := windows.UTF16ToString(characters)
			if name != "." && name != ".." {
				if name == "" || strings.ContainsAny(name, `\/:`) || filepath.Base(name) != name ||
					len(result) >= entryLimit {
					return nil, ErrOpenCodeCleanupFailed
				}
				result = append(result, name)
			}
			readAny = true
			if nextOffset == 0 {
				break
			}
			if nextOffset < nameOffset+nameByteLength || offset+nextOffset <= offset || offset+nextOffset >= len(buffer) {
				return nil, ErrOpenCodeCleanupFailed
			}
			offset += nextOffset
		}
		if !readAny {
			return nil, ErrOpenCodeCleanupFailed
		}
	}
	sort.Strings(result)
	return result, nil
}

func (locked *openCodeLockedSmokeRoot) Close() error {
	if locked == nil || locked.handle == 0 || locked.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(locked.handle)
	locked.handle = windows.InvalidHandle
	if err != nil {
		return ErrOpenCodeCleanupFailed
	}
	return nil
}

// These layouts mirror CRYPT_PROVIDER_SGNR and CRYPT_PROVIDER_CERT. Only the
// verified signer's first chain certificate is read while WinTrust owns state.
type openCodeCryptProviderSigner struct {
	size               uint32
	verifyAsOf         windows.Filetime
	certChainCount     uint32
	certChain          *openCodeCryptProviderCert
	signerType         uint32
	signerInfo         unsafe.Pointer
	errorCode          uint32
	counterSignerCount uint32
	counterSigners     unsafe.Pointer
	certificateChain   unsafe.Pointer
}

type openCodeCryptProviderCert struct {
	size                uint32
	cert                *windows.CertContext
	commercial          uint32
	trustedRoot         uint32
	selfSigned          uint32
	testCertificate     uint32
	revokedReason       uint32
	confidence          uint32
	errorCode           uint32
	trustListContext    unsafe.Pointer
	trustListSignerCert uint32
	controlContext      unsafe.Pointer
	controlError        uint32
	cyclic              uint32
	chainElement        unsafe.Pointer
}

func openCodePlatformBinaryPathSafe(path string) bool {
	volume := filepath.VolumeName(path)
	if strings.Contains(strings.TrimPrefix(path, volume), ":") {
		return false
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := windows.GetFileAttributes(pathUTF16)
	return err == nil && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

// lockOpenCodeVerifiedBinary prevents replacement, writes and deletion of the
// app-owned candidate between hashing, signature verification and execution.
func lockOpenCodeVerifiedBinary(path string) (io.Closer, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, openCodeBinaryRejectedError("path")
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, openCodeBinaryRejectedError("lock")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = windows.CloseHandle(handle)
		return nil, openCodeBinaryRejectedError("file type")
	}
	return &openCodeLockedBinary{handle: handle}, nil
}

// lockOpenCodeSmokeRoot denies delete sharing for the owned directory. This
// prevents rename/replacement until remove marks that exact handle for deletion.
func lockOpenCodeSmokeRoot(path string) (openCodeSmokeRootLock, error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		windows.DELETE|windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, ErrOpenCodeCleanupFailed
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, ErrOpenCodeCleanupFailed
	}
	return &openCodeLockedSmokeRoot{handle: handle}, nil
}

func verifyOpenCodePlatformSignature(path string) error {
	if runtime.GOARCH != "amd64" {
		return ErrOpenCodeContainmentUnavailable
	}
	subject, err := openCodeAuthenticodeSignerSubject(path)
	if err != nil || !strings.Contains(subject, openCodeExpectedSignerSubject) {
		return openCodeBinaryRejectedError("signature")
	}
	return nil
}

// queryOpenCodeAuthenticodeSigner verifies trust without UI or network and
// obtains the actual verified signer from the returned WinTrust StateData.
func queryOpenCodeAuthenticodeSigner(path string) (subject string, resultErr error) {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", openCodeBinaryRejectedError("signature path")
	}
	fileInfo := windows.WinTrustFileInfo{
		Size:     uint32(unsafe.Sizeof(windows.WinTrustFileInfo{})),
		FilePath: pathUTF16,
	}
	data := windows.WinTrustData{
		Size:                            uint32(unsafe.Sizeof(windows.WinTrustData{})),
		UIChoice:                        windows.WTD_UI_NONE,
		RevocationChecks:                windows.WTD_REVOKE_NONE,
		UnionChoice:                     windows.WTD_CHOICE_FILE,
		StateAction:                     windows.WTD_STATEACTION_VERIFY,
		FileOrCatalogOrBlobOrSgnrOrCert: unsafe.Pointer(&fileInfo),
		ProvFlags: windows.WTD_CACHE_ONLY_URL_RETRIEVAL |
			windows.WTD_REVOCATION_CHECK_NONE,
		UIContext: windows.WTD_UICONTEXT_EXECUTE,
	}
	verifyErr := openCodeWinVerifyTrust(
		windows.InvalidHWND,
		&windows.WINTRUST_ACTION_GENERIC_VERIFY_V2,
		&data,
	)
	defer func() {
		data.StateAction = windows.WTD_STATEACTION_CLOSE
		if closeErr := openCodeWinVerifyTrust(
			windows.InvalidHWND,
			&windows.WINTRUST_ACTION_GENERIC_VERIFY_V2,
			&data,
		); closeErr != nil {
			subject = ""
			resultErr = openCodeBinaryRejectedError("signature cleanup")
		}
		runtime.KeepAlive(pathUTF16)
		runtime.KeepAlive(fileInfo)
		runtime.KeepAlive(data)
	}()
	if verifyErr != nil {
		return "", openCodeBinaryRejectedError("signature trust")
	}
	subject, err = openCodeWinTrustSignerSubject(data.StateData)
	if err != nil || subject == "" {
		return "", openCodeBinaryRejectedError("signature signer")
	}
	return subject, nil
}

func queryOpenCodeWinTrustSignerSubject(state windows.Handle) (string, error) {
	if state == 0 || state == windows.InvalidHandle {
		return "", openCodeBinaryRejectedError("signature state")
	}
	providerData, _, _ := openCodeWinTrustProviderData.Call(uintptr(state))
	if providerData == 0 {
		return "", openCodeBinaryRejectedError("signature provider")
	}
	signerPointer, _, _ := openCodeWinTrustProviderSigner.Call(providerData, 0, 0, 0)
	if signerPointer == 0 {
		return "", openCodeBinaryRejectedError("signature chain")
	}
	signer := (*openCodeCryptProviderSigner)(unsafe.Pointer(signerPointer))
	if !validOpenCodeCryptProviderSigner(signer) || signer.certChainCount == 0 || signer.certChain == nil ||
		!validOpenCodeCryptProviderCert(signer.certChain) || signer.certChain.cert == nil {
		return "", openCodeBinaryRejectedError("signature certificate")
	}
	certificate := signer.certChain.cert
	characters := windows.CertGetNameString(
		certificate,
		windows.CERT_NAME_SIMPLE_DISPLAY_TYPE,
		0,
		nil,
		nil,
		0,
	)
	if characters <= 1 || characters > 4096 {
		return "", openCodeBinaryRejectedError("signature subject")
	}
	buffer := make([]uint16, characters)
	written := windows.CertGetNameString(
		certificate,
		windows.CERT_NAME_SIMPLE_DISPLAY_TYPE,
		0,
		nil,
		&buffer[0],
		uint32(len(buffer)),
	)
	if written != characters {
		return "", openCodeBinaryRejectedError("signature subject")
	}
	result := windows.UTF16ToString(buffer)
	if result == "" {
		return "", openCodeBinaryRejectedError("signature subject")
	}
	runtime.KeepAlive(signer)
	runtime.KeepAlive(certificate)
	return result, nil
}

func validOpenCodeCryptProviderSigner(signer *openCodeCryptProviderSigner) bool {
	if signer == nil {
		return false
	}
	minimumSize := unsafe.Offsetof(signer.certChain) + unsafe.Sizeof(signer.certChain)
	return uintptr(signer.size) >= minimumSize
}

func validOpenCodeCryptProviderCert(certificate *openCodeCryptProviderCert) bool {
	if certificate == nil {
		return false
	}
	minimumSize := unsafe.Offsetof(certificate.cert) + unsafe.Sizeof(certificate.cert)
	return uintptr(certificate.size) >= minimumSize
}

var _ io.Closer = (*openCodeLockedBinary)(nil)
var _ openCodeSmokeRootLock = (*openCodeLockedSmokeRoot)(nil)
