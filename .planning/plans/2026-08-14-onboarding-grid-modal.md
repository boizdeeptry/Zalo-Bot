# Onboarding modal trên nền lưới — implementation plan

> **For automated executors:** invoke `:subagent-driven-development` (recommended) or `:executing-plans` to run this plan task-by-task. Steps use `- [ ]` checkboxes.

**Goal:** Tạm ẩn toàn bộ khung trang trí onboarding ngoài modal, chỉ giữ nền lưới và nội dung mang nhận diện Tư Vấn Zalo.

**TDD mode:** yes

**Architecture:** Giữ nguyên DOM dashboard để thay đổi có thể đảo ngược, nhưng CSS scoped sẽ ẩn topbar, sidebar và summary card đồng thời mở canvas lưới phủ viewport. Các creator hiện có chỉ đổi copy hiển thị; controller, service và backend contract không đổi.

**Tech stack:** Vanilla JavaScript, CSS, Node `node:test` DOM harness.

**Spec:** `.planning/specs/2026-08-14-onboarding-grid-modal-design.md`

**Research:** skipped — thay đổi trình bày nhỏ, đã khảo sát trực tiếp DOM/CSS/test hiện tại.

---

### Task 1: Chỉ hiển thị modal trên nền lưới của Tư Vấn Zalo

**Files:**
- Modify: `appmode/tests/onboarding-ags-setup.test.mjs`
- Modify: `appmode/tests/onboarding-early.test.mjs`
- Modify: `appmode/tests/shell.test.mjs`
- Modify: `appmode/overlay/internal/webui/static/pages/onboarding-early-view.js`
- Modify: `appmode/overlay/internal/webui/static/portal.css`

**Public behavior to verify:** Khi onboarding mở, người dùng chỉ thấy modal bắt buộc trên lưới toàn màn hình; không thấy topbar/sidebar/summary trang trí và không thấy nội dung nhắc tới sản phẩm tham chiếu.

- [ ] **Step 1: Viết behavior tests thất bại**

  Cập nhật DOM test để xác nhận một dialog có focus, dashboard vẫn `aria-hidden`, copy có “Tư Vấn Zalo”, và source UI onboarding không chứa tên sản phẩm tham chiếu. Cập nhật CSS test để yêu cầu topbar/sidebar/summary có `display:none`, canvas có `inset:0`, còn modal giữ kích thước, focus ring và responsive rule.

- [ ] **Step 2: Chạy RED**

  Run:

  ```powershell
  node --test appmode/tests/onboarding-ags-setup.test.mjs appmode/tests/onboarding-early.test.mjs appmode/tests/shell.test.mjs
  ```

  Expected: FAIL vì copy cũ còn tên sản phẩm tham chiếu, các phần trang trí chưa ẩn và canvas còn chừa topbar/sidebar.

- [ ] **Step 3: Cài đặt tối thiểu**

  Trong `onboarding-early-view.js`, đổi eyebrow và mô tả sang Tư Vấn Zalo nhưng giữ nguyên hành vi các phase. Trong `portal.css`, thêm quy tắc scoped ẩn ba lớp trang trí và đổi canvas thành `inset:0`; không xoá DOM hay thay đổi controller.

- [ ] **Step 4: Chạy GREEN và regression**

  Run focused command ở Step 2, sau đó:

  ```powershell
  npm --prefix appmode test
  ```

  Expected: tất cả test pass, không có warning mới.

- [ ] **Step 5: QA và commit**

  Chạy `git diff --check`, kiểm tra giới hạn 800 dòng, build preview sạch, chụp desktop/mobile và xác nhận chỉ còn modal + lưới. Commit bằng thông điệp `fix: simplify onboarding backdrop`.
