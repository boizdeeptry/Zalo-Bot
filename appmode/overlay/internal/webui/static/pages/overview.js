import { element, pageHeader, statusPanel } from "../core/ui.js";

const foundationLinks = Object.freeze([
  { href: "#agents", label: "Trợ lý AI", detail: "Danh tính, văn phong và mức sẵn sàng" },
  { href: "#knowledge", label: "Tri thức", detail: "Tài liệu nguồn và lượt biên soạn" },
  { href: "#models", label: "Mô hình", detail: "Chọn cân bằng tốc độ và chất lượng" },
  { href: "/zalo", label: "Hội thoại", detail: "Mở không gian trực Zalo hiện tại" },
]);

function linkCard(item) {
  return element(
    "a",
    { className: "launch-card", attributes: { href: item.href } },
    element("span", { className: "launch-card__label", text: item.label }),
    element("span", { className: "launch-card__detail", text: item.detail }),
    element("span", { className: "launch-card__arrow", text: "→", attributes: { "aria-hidden": "true" } }),
  );
}

export function mount(container) {
  container.append(
    pageHeader(
      "Tổng quan",
      "Một nơi để cấu hình trợ lý, quản lý tri thức và đi vào các cuộc hội thoại đang hoạt động.",
    ),
    statusPanel({
      tone: "progress",
      title: "Đang hoàn thiện nền Portal",
      body: "Điều hướng, kết nối API và vòng đời trang đã sẵn sàng cho các mô-đun vận hành.",
    }),
    element(
      "section",
      { className: "section-block", attributes: { "aria-labelledby": "portal-areas" } },
      element("div", { className: "section-heading" },
        element("h2", { text: "Khu vực làm việc", attributes: { id: "portal-areas" } }),
        element("p", { text: "Đi thẳng tới việc bạn cần xử lý." }),
      ),
      element("div", { className: "launch-grid" }, foundationLinks.map(linkCard)),
    ),
  );

  return { dispose() {} };
}

export function createFoundationPage({ title, description }) {
  return Object.freeze({
    mount(container) {
      container.append(
        pageHeader(title, description),
        statusPanel({
          tone: "progress",
          title: `Đang chuẩn bị ${title}`,
          body: "Khung trang đã hoạt động và sẽ nhận mô-đun nghiệp vụ trong các bước kế tiếp.",
        }),
      );
      return { dispose() {} };
    },
  });
}
