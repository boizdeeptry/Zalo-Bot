// "Đã kết nối provider này chưa?" là MỘT luật, dùng ở hai nơi: nhãn trạng thái ở gallery Providers
// (providers.js galleryStatus) và bộ lọc model ở picker Combos (combos.js openModelPicker). Tách ra
// đây để hai nơi không trôi khỏi nhau — cùng luật với hasAnyConnectedProvider bên Go.

// Kind thuê bao — mirror của PROVIDER_CATALOG group="subscription" (providers.js) và subscriptionKinds
// bên Go. Object provider từ /llm/providers KHÔNG mang trường `group` (chỉ có ở catalogue phía trang),
// nên "thuê bao hay không" phải suy ra từ kind. Chuẩn hoá `_`→`-` như normalizeKind của providers.js.
const SUBSCRIPTION_KINDS = new Set(["claude-code", "codex"]);
const normalizeKind = (kind) => String(kind ?? "").replace(/_/g, "-");

// isProviderConnected: người mua đã nối provider này chưa? Đúng luật galleryStatus:
//   - credential_unreadable → CHƯA (khoá hỏng, cần đăng nhập lại) — xét trước mọi nhánh khác.
//   - kind thuê bao (codex/claude-code) → nối = có ≥1 account đang BẬT. KHÔNG xét credential: CLI giữ
//     phiên riêng của account, không có credential đi qua daemon; cũng KHÔNG xét system: một claude-code
//     hệ thống mà 0 account đăng nhập thì im, không trả lời được.
//   - còn lại (API) → nối = last_check_status "ok" HOẶC system HOẶC đã cấu hình credential.
// Lưu ý: KHÔNG xét provider.enabled — "đã nối" là chuyện có khoá/account, không phải hàng có đang bật;
// cùng cách hasAnyConnectedProvider bên Go tính (credential_configured bất kể enabled).
export function isProviderConnected(provider) {
  if (!provider || provider.credential_unreadable) return false;
  if (SUBSCRIPTION_KINDS.has(normalizeKind(provider.kind))) {
    const accounts = Array.isArray(provider.accounts)
      ? provider.accounts.filter((account) => account.enabled !== false)
      : [];
    return accounts.length > 0;
  }
  return provider.last_check_status === "ok"
    || Boolean(provider.system)
    || Boolean(provider.credential_configured);
}
